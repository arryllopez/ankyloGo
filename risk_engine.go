package ankylogo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

type RiskScore struct {
	score       int
	lastUpdated time.Time
	notified    bool
	mu          sync.Mutex
}

type ThresholdNotifier interface {
	Notify(ip string, score int)
}

// riskEngineConfig holds all shared configuration for risk engine implementations.
type riskEngineConfig struct {
	threshold                   int
	topic                       string
	decayRate                   time.Duration
	useHalfLife                 bool
	keyTTL                      time.Duration // Redis only: how long before an idle risk key expires (0 = never)
	redisTimeout                time.Duration // Redis only: per-call timeout for processEvent (0 = no timeout)
	OnThreshold                 ThresholdNotifier
	customWeightAllowed         int
	customWeightWindow          int
	customWeightBucket          int
	customWeightPassedThreshold int
}

// RiskEngineOption configures a riskEngineConfig, applying to any engine implementation.
type RiskEngineOption func(*riskEngineConfig)

// WithWeightAllowed sets the weight for ALLOWED events (default: 0)
func WithWeightAllowed(weight int) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.customWeightAllowed = weight
	}
}

// WithWeightWindow sets the weight for DENIED_WINDOW events (default: 1)
func WithWeightWindow(weight int) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.customWeightWindow = weight
	}
}

// WithWeightBucket sets the weight for DENIED_BUCKET events (default: 4)
func WithWeightBucket(weight int) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.customWeightBucket = weight
	}
}

// WithWeightPassedThreshold sets the weight for DENIED_RISK events (default: 10)
func WithWeightPassedThreshold(weight int) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.customWeightPassedThreshold = weight
	}
}

// WithThresholdNotifier sets a custom threshold notifier
func WithThresholdNotifier(notifier ThresholdNotifier) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.OnThreshold = notifier
	}
}

// WithDecayRate sets the decay rate (default: 0 - no decay)
func WithDecayRate(rate time.Duration) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.decayRate = rate
	}
}

// WithHalfLifeDecay switches decay from linear to exponential half-life mode.
func WithHalfLifeDecay() RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.useHalfLife = true
	}
}

// WithKeyTTL sets how long a Redis risk key persists after its last update (0 = never expires).
// Only has effect on RedisRiskEngine.
func WithKeyTTL(ttl time.Duration) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.keyTTL = ttl
	}
}

// WithRedisTimeout sets a per-call timeout for Redis operations in processEvent (0 = no timeout).
// Only has effect on RedisRiskEngine.
func WithRedisTimeout(timeout time.Duration) RiskEngineOption {
	return func(c *riskEngineConfig) {
		c.redisTimeout = timeout
	}
}

type RiskEngine struct {
	client   *kgo.Client
	ipScores sync.Map
	riskEngineConfig
}

// NewRiskEngine creates a new RiskEngine with default weights (0, 1, 4, 10).
// Required parameters: client, threshold, topic.
// Optional parameters: use With* option functions.
func NewRiskEngine(client *kgo.Client, threshold int, topic string, opts ...RiskEngineOption) *RiskEngine {
	cfg := riskEngineConfig{
		threshold:                   threshold,
		topic:                       topic,
		customWeightAllowed:         0,
		customWeightWindow:          1,
		customWeightBucket:          4,
		customWeightPassedThreshold: 10,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &RiskEngine{
		client:           client,
		riskEngineConfig: cfg,
	}
}

func NewRiskScore(score int, lastUpdated time.Time) *RiskScore {
	return &RiskScore{
		score:       score,
		lastUpdated: lastUpdated,
	}
}

// GetScore returns the current effective risk score for an IP,
func (r *RiskEngine) GetScore(ip string) int {
	val, ok := r.ipScores.Load(ip)
	if !ok {
		return 0
	}
	riskScore := val.(*RiskScore)
	riskScore.mu.Lock()
	defer riskScore.mu.Unlock()
	current := riskScore.score
	if r.decayRate > 0 {
		now := time.Now()
		elapsed := now.Sub(riskScore.lastUpdated)
		if r.useHalfLife {
			factor := math.Pow(0.5, elapsed.Seconds()/r.decayRate.Seconds())
			current = int(float64(current) * factor)
		} else {
			intervals := int(elapsed / r.decayRate)
			current -= intervals
		}
	}

	if current < 0 {
		current = 0
	}
	return current
}

// This function takes a the failed events for a specific ip and increments its risk score
// for each failed attempt, if no failed attempts happen over a period of time, an interval system
// is in place, so for example if interval was 30 minutes then if no failed api calls happen within 2 hours
// the specific ip's risk score gets deducted by 4 points since there are 120 minutes in 2 hours and
// 120 / 30 =  4
// half life decay halves the score after one decay rate interval if the option is enabled
func (r *RiskEngine) processEvent(event RateLimitEvent) (int, bool) {
	// bump the score for the ip for each denied event
	newScore := &RiskScore{lastUpdated: time.Now()}
	score, _ := r.ipScores.LoadOrStore(event.IP, newScore)
	riskScore := score.(*RiskScore)
	riskScore.mu.Lock()
	now := time.Now()
	if r.decayRate > 0 {
		elapsed := now.Sub(riskScore.lastUpdated)
		if r.useHalfLife {
			factor := math.Pow(0.5, elapsed.Seconds()/r.decayRate.Seconds())
			riskScore.score = int(float64(riskScore.score) * factor)
		} else {
			intervals := int(elapsed / r.decayRate)
			riskScore.score -= intervals
		}
	}
	if riskScore.score < 0 {
		riskScore.score = 0
	}
	// re-arm notification if score decayed back to or below threshold
	if riskScore.score <= r.threshold {
		riskScore.notified = false
	}

	switch event.Action {
	case "ALLOWED":
		riskScore.score += r.customWeightAllowed
	case "DENIED_WINDOW":
		riskScore.score += r.customWeightWindow
	case "DENIED_BUCKET":
		riskScore.score += r.customWeightBucket
	case "DENIED_RISK":
		riskScore.score += r.customWeightPassedThreshold
	}

	riskScore.lastUpdated = now
	currentScore := riskScore.score

	// only signal notification on the first crossing
	shouldNotify := false
	if currentScore > r.threshold && !riskScore.notified {
		riskScore.notified = true
		shouldNotify = true
	}
	riskScore.mu.Unlock()
	return currentScore, shouldNotify
}

func (r *RiskEngine) EventReader(ctx context.Context) {
	for {
		//poll fetches, this blocks until records do arrive
		fetches := r.client.PollFetches(ctx)

		//if case for cancelled context
		if ctx.Err() != nil {
			fmt.Println("context cancelled")
			return
		}

		//errors while fetching
		if errs := fetches.Errors(); len(errs) > 0 {
			fmt.Printf("Errors while fetching: %v\n", errs)
			continue
		}

		// populating a new instance of ratelimitevent by unmarshalling the record
		fetches.EachRecord(func(record *kgo.Record) {
			var event RateLimitEvent
			err := json.Unmarshal(record.Value, &event)
			if err != nil {
				return
			}
			currentScore, shouldNotify := r.processEvent(event)

			if shouldNotify && r.OnThreshold != nil {
				r.OnThreshold.Notify(event.IP, currentScore)
			}
		})

		// when client closes end the loop
		if fetches.IsClientClosed() {
			return
		}

		fmt.Println("Fetched a batch of records...")
	}
}
