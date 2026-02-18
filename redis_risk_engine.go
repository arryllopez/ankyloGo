package ankylogo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kgo"
)

var redisProcessEventScript = redis.NewScript(`
local key         = KEYS[1]
local now         = tonumber(ARGV[1])
local decayRate   = tonumber(ARGV[2])
local useHalfLife = tonumber(ARGV[3])
local eventWeight = tonumber(ARGV[4])
local ttlSeconds  = tonumber(ARGV[5])

local score       = tonumber(redis.call('HGET', key, 'score'))       or 0
local lastUpdated = tonumber(redis.call('HGET', key, 'last_updated')) or now

if decayRate > 0 then
    local elapsed = now - lastUpdated
    if useHalfLife == 1 then
        local factor = math.pow(0.5, elapsed / decayRate)
        score = math.floor(score * factor)
    else
        local intervals = math.floor(elapsed / decayRate)
        score = score - intervals
    end
end

if score < 0 then score = 0 end

score = score + eventWeight

redis.call('HSET', key, 'score', score, 'last_updated', now)
if ttlSeconds > 0 then
    redis.call('EXPIRE', key, ttlSeconds)
end
return score
`)

type RedisRiskEngine struct {
	kafkaClient *kgo.Client
	redisClient *redis.Client
	notified    sync.Map
	riskEngineConfig
}

func NewRedisRiskEngine(client *kgo.Client, redisClient *redis.Client, threshold int, topic string, opts ...RiskEngineOption) *RedisRiskEngine {
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
	return &RedisRiskEngine{
		kafkaClient:      client,
		redisClient:      redisClient,
		riskEngineConfig: cfg,
	}
}

func (r *RedisRiskEngine) processEvent(ctx context.Context, event RateLimitEvent) (int, bool) {
	var weight int
	switch event.Action {
	case "ALLOWED":
		weight = r.customWeightAllowed
	case "DENIED_WINDOW":
		weight = r.customWeightWindow
	case "DENIED_BUCKET":
		weight = r.customWeightBucket
	case "DENIED_RISK":
		weight = r.customWeightPassedThreshold
	}

	useHalfLife := 0
	if r.useHalfLife {
		useHalfLife = 1
	}

	if r.redisTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.redisTimeout)
		defer cancel()
	}

	now := time.Now().UnixNano()
	currentScore, err := redisProcessEventScript.Run(ctx, r.redisClient,
		[]string{"risk:" + event.IP},
		now,
		r.decayRate.Nanoseconds(),
		useHalfLife,
		weight,
		int64(r.keyTTL.Seconds()),
	).Int()
	if err != nil {
		fmt.Printf("redis processEvent error: %v\n", err)
		return 0, false
	}

	shouldNotify := false
	if currentScore > r.threshold {
		_, alreadyNotified := r.notified.LoadOrStore(event.IP, true)
		if !alreadyNotified {
			shouldNotify = true
		}
	} else {
		r.notified.Delete(event.IP) // re-arm once score drops back below threshold
	}

	return currentScore, shouldNotify
}

// GetScore returns the current effective risk score for an IP, with decay applied client-side.
// Satisfies the ScoreReader interface.
func (r *RedisRiskEngine) GetScore(ip string) int {
	ctx := context.Background()
	vals, err := r.redisClient.HMGet(ctx, "risk:"+ip, "score", "last_updated").Result()
	if err != nil || vals[0] == nil {
		return 0
	}

	score, err := strconv.Atoi(vals[0].(string))
	if err != nil {
		return 0
	}

	if r.decayRate > 0 && vals[1] != nil {
		lastUpdatedNs, err := strconv.ParseInt(vals[1].(string), 10, 64)
		if err == nil {
			elapsed := time.Duration(time.Now().UnixNano() - lastUpdatedNs)
			if r.useHalfLife {
				factor := math.Pow(0.5, elapsed.Seconds()/r.decayRate.Seconds())
				score = int(float64(score) * factor)
			} else {
				intervals := int(elapsed / r.decayRate)
				score -= intervals
			}
		}
	}

	if score < 0 {
		return 0
	}
	return score
}

func (r *RedisRiskEngine) EventReader(ctx context.Context) {
	for {
		fetches := r.kafkaClient.PollFetches(ctx)

		if ctx.Err() != nil {
			fmt.Println("context cancelled")
			return
		}

		if errs := fetches.Errors(); len(errs) > 0 {
			fmt.Printf("Errors while fetching: %v\n", errs)
			continue
		}

		fetches.EachRecord(func(record *kgo.Record) {
			var event RateLimitEvent
			if err := json.Unmarshal(record.Value, &event); err != nil {
				return
			}
			currentScore, shouldNotify := r.processEvent(ctx, event)
			if shouldNotify && r.OnThreshold != nil {
				r.OnThreshold.Notify(event.IP, currentScore)
			}
		})

		if fetches.IsClientClosed() {
			return
		}

		fmt.Println("Fetched a batch of records...")
	}
}
