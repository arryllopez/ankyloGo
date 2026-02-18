package ankylogo

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func newTestRedisEngine(redisClient *redis.Client, threshold int, decayRate time.Duration) *RedisRiskEngine {
	return NewRedisRiskEngine(nil, redisClient, threshold, "",
		WithDecayRate(decayRate),
	)
}

/*
Test that the first denied event for an IP sets the score to 1
*/
func TestRedisRiskScoreFirstEvent(t *testing.T) {
	client := setupRedisClient()
	if client == nil {
		t.Skip("Redis not available, skipping test")
	}
	defer client.Close()

	ctx := context.Background()
	engine := newTestRedisEngine(client, 5, 30*time.Minute)
	ip := "redis-risk-first"
	defer client.Del(ctx, "risk:"+ip)

	event := RateLimitEvent{IP: ip, Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}
	score, _ := engine.processEvent(ctx, event)
	if score != 1 {
		t.Errorf("First event should set score to 1, got %d", score)
	}
}

/*
Test that 5 DENIED_BUCKET events (weight 4 each) produce a score of 20
*/
func TestRedisRiskScoreMultipleEvents(t *testing.T) {
	client := setupRedisClient()
	if client == nil {
		t.Skip("Redis not available, skipping test")
	}
	defer client.Close()

	ctx := context.Background()
	engine := newTestRedisEngine(client, 100, 30*time.Minute)
	ip := "redis-risk-multiple"
	defer client.Del(ctx, "risk:"+ip)

	event := RateLimitEvent{IP: ip, Endpoint: "POST /login", Action: "DENIED_BUCKET", Timestamp: time.Now().UnixNano()}

	var lastScore int
	for range 5 {
		lastScore, _ = engine.processEvent(ctx, event)
	}
	if lastScore != 20 {
		t.Errorf("After 5 DENIED_BUCKET events (weight 4), score should be 20, got %d", lastScore)
	}
}

/*
Test that scores decay based on elapsed time.
Build score to 5, wait ~3 decay intervals, then +1 for next event = 3.
*/
func TestRedisRiskScoreDecay(t *testing.T) {
	client := setupRedisClient()
	if client == nil {
		t.Skip("Redis not available, skipping test")
	}
	defer client.Close()

	ctx := context.Background()
	engine := newTestRedisEngine(client, 100, 100*time.Millisecond)
	ip := "redis-risk-decay"
	defer client.Del(ctx, "risk:"+ip)

	event := RateLimitEvent{IP: ip, Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	for range 5 {
		engine.processEvent(ctx, event)
	}

	time.Sleep(350 * time.Millisecond)

	// score was 5, decay 3 intervals, +1 for new event = 3
	score, _ := engine.processEvent(ctx, event)
	if score != 3 {
		t.Errorf("After 3 decay intervals, score should be 5-3+1=3, got %d", score)
	}
}

/*
Test threshold crossing detection and one-shot notification re-arming.
*/
func TestRedisRiskScoreThresholdCrossing(t *testing.T) {
	client := setupRedisClient()
	if client == nil {
		t.Skip("Redis not available, skipping test")
	}
	defer client.Close()

	ctx := context.Background()
	engine := newTestRedisEngine(client, 3, 30*time.Minute)
	ip := "redis-risk-threshold"
	defer client.Del(ctx, "risk:"+ip)

	event := RateLimitEvent{IP: ip, Endpoint: "POST /login", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// First 3 events: scores 1, 2, 3 — all at or below threshold
	for i := range 3 {
		score, shouldNotify := engine.processEvent(ctx, event)
		if shouldNotify {
			t.Errorf("Event %d should not trigger notification (score %d <= threshold)", i+1, score)
		}
	}

	// 4th event: score 4 > threshold — first crossing, should notify
	_, shouldNotify := engine.processEvent(ctx, event)
	if !shouldNotify {
		t.Errorf("4th event should trigger notification (first threshold crossing)")
	}

	// 5th event: already notified, should not fire again
	_, shouldNotify = engine.processEvent(ctx, event)
	if shouldNotify {
		t.Errorf("5th event should NOT trigger notification (already notified)")
	}
}

/*
Test GetScore returns the correct score without modifying state.
*/
func TestRedisRiskScoreGetScore(t *testing.T) {
	client := setupRedisClient()
	if client == nil {
		t.Skip("Redis not available, skipping test")
	}
	defer client.Close()

	ctx := context.Background()
	engine := newTestRedisEngine(client, 100, 30*time.Minute)
	ip := "redis-risk-getscore"
	defer client.Del(ctx, "risk:"+ip)

	event := RateLimitEvent{IP: ip, Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	for range 3 {
		engine.processEvent(ctx, event)
	}

	score := engine.GetScore(ip)
	if score != 3 {
		t.Errorf("GetScore should return 3, got %d", score)
	}

	// Read-only — calling again should still return 3
	score2 := engine.GetScore(ip)
	if score2 != 3 {
		t.Errorf("GetScore called twice should still return 3, got %d", score2)
	}
}

/*
Test GetScore returns 0 for an IP with no recorded events.
*/
func TestRedisRiskScoreGetScoreUnknownIP(t *testing.T) {
	client := setupRedisClient()
	if client == nil {
		t.Skip("Redis not available, skipping test")
	}
	defer client.Close()

	engine := newTestRedisEngine(client, 100, 30*time.Minute)
	score := engine.GetScore("99.99.99.99")
	if score != 0 {
		t.Errorf("GetScore for unknown IP should return 0, got %d", score)
	}
}
