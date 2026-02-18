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

type RiskEngine struct {
	client                      *kgo.Client
	ipScores                    sync.Map
	threshold                   int
	topic                       string
	decayRate                   time.Duration
	OnThreshold                 ThresholdNotifier
	customWeightAllowed         int
	customWeightBucket          int
	customWeightWindow          int
	customWeightPassedThreshold int
	useHalfLife                 bool
}

// RiskEngineOption is a function that configures a RiskEngine
type RiskEngineOption func(*RiskEngine)

// WithWeightAllowed sets the weight for ALLOWED events (default: 0)
func WithWeightAllowed(weight int) RiskEngineOption {
	return func(r *RiskEngine) {
		r.customWeightAllowed = weight
	}
}

// WithWeightWindow sets the weight for DENIED_WINDOW events (default: 1)
func WithWeightWindow(weight int) RiskEngineOption {
	return func(r *RiskEngine) {
		r.customWeightWindow = weight
	}
}

// WithWeightBucket sets the weight for DENIED_BUCKET events (default: 4)
func WithWeightBucket(weight int) RiskEngineOption {
	return func(r *RiskEngine) {
		r.customWeightBucket = weight
	}
}

// WithWeightPassedThreshold sets the weight for DENIED_RISK events (default: 10)
func WithWeightPassedThreshold(weight int) RiskEngineOption {
	return func(r *RiskEngine) {
		r.customWeightPassedThreshold = weight
	}
}

// WithThresholdNotifier sets a custom threshold notifier
func WithThresholdNotifier(notifier ThresholdNotifier) RiskEngineOption {
	return func(r *RiskEngine) {
		r.OnThreshold = notifier
	}
}

// WithDecayRate sets the decay rate (default: 0 - no decay)
func WithDecayRate(rate time.Duration) RiskEngineOption {
	return func(r *RiskEngine) {
		r.decayRate = rate
	}
}

func WithHalfLifeDecay() RiskEngineOption {
	return func(r *RiskEngine) {
		r.useHalfLife = true
	}
}

// NewRiskEngine creates a new RiskEngine with default weights (0, 1, 4, 10)
// Required parameters: client, threshold, topic
// Optional parameters: use With* option functions
func NewRiskEngine(client *kgo.Client, threshold int, topic string, opts ...RiskEngineOption) *RiskEngine {
	// Create engine with defaults
	engine := &RiskEngine{
		client:                      client,
		threshold:                   threshold,
		topic:                       topic,
		decayRate:                   0,   // default: no decay
		OnThreshold:                 nil, // default: no notifier
		customWeightAllowed:         0,   // default: 0
		customWeightWindow:          1,   // default: 1
		customWeightBucket:          4,   // default: 4
		customWeightPassedThreshold: 10,  // default: 10
		ipScores:                    sync.Map{},
	}

	// Apply custom options
	for _, opt := range opts {
		opt(engine)
	}

	return engine
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
