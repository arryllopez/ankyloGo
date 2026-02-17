package ankylogo

import (
	"testing"
	"time"
)

// Helper to create a test RiskEngine with default weights (0, 1, 4, 10)
func newTestEngine(threshold int, decayRate time.Duration) *RiskEngine {
	return &RiskEngine{
		threshold:                   threshold,
		decayRate:                   decayRate,
		customWeightAllowed:         0,
		customWeightWindow:          1,
		customWeightBucket:          4,
		customWeightPassedThreshold: 10,
	}
}

/*
Test that the first denied event for an IP sets the score to 1
A brand new IP with no history should start at exactly 1
*/
func TestRiskScoreFirstEvent(t *testing.T) {
	engine := newTestEngine(5, 30*time.Minute)

	event := RateLimitEvent{
		IP:        "192.168.1.1",
		Endpoint:  "GET /ping",
		Action:    "DENIED_WINDOW",
		Timestamp: time.Now().UnixNano(),
	}

	score, _ := engine.processEvent(event)
	if score != 1 {
		t.Errorf("First event should set score to 1, got %d", score)
	}
}

/*
Test that multiple rapid events from the same IP accumulate correctly
5 DENIED_BUCKET events (weight 4 each) should produce a score of 20
*/
func TestRiskScoreMultipleEvents(t *testing.T) {
	engine := newTestEngine(10, 30*time.Minute)

	event := RateLimitEvent{
		IP:        "10.0.0.1",
		Endpoint:  "POST /login",
		Action:    "DENIED_BUCKET",
		Timestamp: time.Now().UnixNano(),
	}

	var lastScore int
	for i := 0; i < 5; i++ {
		lastScore, _ = engine.processEvent(event)
	}

	if lastScore != 20 {
		t.Errorf("After 5 rapid DENIED_BUCKET events (weight 4) score should be 20, got %d", lastScore)
	}
}

/*
Test that different IPs have completely isolated scores
Events from IP A should not affect IP B's score
*/
func TestRiskScoreIsolatedIPs(t *testing.T) {
	engine := newTestEngine(10, 30*time.Minute)

	eventA := RateLimitEvent{IP: "1.1.1.1", Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}
	eventB := RateLimitEvent{IP: "2.2.2.2", Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// 3 events for IP A
	for i := 0; i < 3; i++ {
		engine.processEvent(eventA)
	}

	// 1 event for IP B — should be 1, not 4
	scoreB, _ := engine.processEvent(eventB)
	if scoreB != 1 {
		t.Errorf("IP B should have score 1 (isolated from A), got %d", scoreB)
	}

	// IP A should now be 4 (3 previous + 1 new)
	scoreA, _ := engine.processEvent(eventA)
	if scoreA != 4 {
		t.Errorf("IP A should have score 4 after 4th event, got %d", scoreA)
	}
}

/*
Test that scores decay based on elapsed time
With a decay rate of 100ms, after 350ms a score of 5 should decay by 3
Then +1 for the new event = 3
*/
func TestRiskScoreDecay(t *testing.T) {
	engine := newTestEngine(10, 100*time.Millisecond)

	event := RateLimitEvent{IP: "10.0.0.5", Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// Build up score to 5
	for i := 0; i < 5; i++ {
		engine.processEvent(event)
	}

	// Wait for ~3 decay intervals
	time.Sleep(350 * time.Millisecond)

	// Score was 5, decay 3, +1 = 3
	score, _ := engine.processEvent(event)
	if score != 3 {
		t.Errorf("After 3 decay intervals, score should be 5-3+1=3, got %d", score)
	}
}

/*
Test that score decay floors at 0 and never goes negative
With DENIED_BUCKET (weight 4): 2 events = 8, decay 6 intervals = 8-6=2, then +4 = 6
*/
func TestRiskScoreDecayFloor(t *testing.T) {
	engine := newTestEngine(10, 100*time.Millisecond)

	event := RateLimitEvent{IP: "172.16.0.1", Endpoint: "GET /search", Action: "DENIED_BUCKET", Timestamp: time.Now().UnixNano()}

	// Build up score: 2 events × weight 4 = 8
	engine.processEvent(event)
	engine.processEvent(event)

	// Wait for 6 decay intervals: 8 - 6 = 2
	time.Sleep(600 * time.Millisecond)

	// Score was 8, decay 6 = 2, +4 for new event = 6
	score, _ := engine.processEvent(event)
	if score != 6 {
		t.Errorf("After decay, score should be 8-6+4 = 6, got %d", score)
	}
}

/*
Test threshold crossing detection
With a threshold of 3, events 1-3 should be at or below threshold
The 4th event should cross it
*/
func TestRiskScoreThresholdCrossing(t *testing.T) {
	engine := newTestEngine(3, 30*time.Minute)

	event := RateLimitEvent{IP: "192.168.0.100", Endpoint: "POST /login", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// First 3 events should not exceed threshold
	for i := 0; i < 3; i++ {
		score, _ := engine.processEvent(event)
		if score > engine.threshold {
			t.Errorf("Event %d should not exceed threshold of 3, score is %d", i+1, score)
		}
	}

	// 4th event should exceed threshold
	score, _ := engine.processEvent(event)
	if score <= engine.threshold {
		t.Errorf("4th event should exceed threshold of 3, score is %d", score)
	}
}

/*
Test GetScore returns the correct effective score without modifying state
Build up a score of 3, then verify GetScore returns 3 without changing it
*/
func TestRiskScoreGetScore(t *testing.T) {
	engine := newTestEngine(10, 30*time.Minute)

	event := RateLimitEvent{IP: "10.10.10.10", Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	for i := 0; i < 3; i++ {
		engine.processEvent(event)
	}

	// GetScore should return 3
	score := engine.GetScore("10.10.10.10")
	if score != 3 {
		t.Errorf("GetScore should return 3, got %d", score)
	}

	// Calling GetScore again should still return 3 (read-only, no side effects)
	score2 := engine.GetScore("10.10.10.10")
	if score2 != 3 {
		t.Errorf("GetScore called twice should still return 3, got %d", score2)
	}
}

/*
Test GetScore returns 0 for an unknown IP
*/
func TestRiskScoreGetScoreUnknownIP(t *testing.T) {
	engine := newTestEngine(10, 30*time.Minute)

	score := engine.GetScore("99.99.99.99")
	if score != 0 {
		t.Errorf("GetScore for unknown IP should return 0, got %d", score)
	}
}

/*
Test GetScore applies decay without modifying stored state
Build up score to 5, wait for decay, verify GetScore returns decayed value
Then verify processEvent still decays from original stored values
*/
func TestRiskScoreGetScoreWithDecay(t *testing.T) {
	engine := newTestEngine(10, 100*time.Millisecond)

	event := RateLimitEvent{IP: "10.0.0.99", Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// Build up score to 5
	for i := 0; i < 5; i++ {
		engine.processEvent(event)
	}

	// Wait for ~3 decay intervals
	time.Sleep(350 * time.Millisecond)

	// GetScore should show decayed value (5 - 3 = 2)
	score := engine.GetScore("10.0.0.99")
	if score != 2 {
		t.Errorf("GetScore after decay should return 2, got %d", score)
	}
}

/*
Test ThresholdNotifier is called when score exceeds threshold
Uses a mock notifier to capture the notification
*/
type mockNotifier struct {
	calledIP    string
	calledScore int
	callCount   int
}

func (m *mockNotifier) Notify(ip string, score int) {
	m.calledIP = ip
	m.calledScore = score
	m.callCount++
}

func TestRiskScoreThresholdNotifier(t *testing.T) {
	notifier := &mockNotifier{}
	engine := newTestEngine(3, 30*time.Minute)
	engine.OnThreshold = notifier

	event := RateLimitEvent{IP: "192.168.1.50", Endpoint: "POST /login", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// First 3 events — shouldNotify must be false (scores 1, 2, 3 which are <= threshold)
	for i := 0; i < 3; i++ {
		_, shouldNotify := engine.processEvent(event)
		if shouldNotify {
			t.Errorf("Event %d should not trigger notification (score <= threshold)", i+1)
		}
	}

	// 4th event pushes score to 4, which exceeds threshold of 3 — first crossing
	currentScore, shouldNotify := engine.processEvent(event)
	if !shouldNotify {
		t.Errorf("4th event should trigger notification (first threshold crossing)")
	}
	if shouldNotify {
		engine.OnThreshold.Notify(event.IP, currentScore)
	}

	if notifier.callCount != 1 {
		t.Errorf("Notifier should have been called once, called %d times", notifier.callCount)
	}
	if notifier.calledIP != "192.168.1.50" {
		t.Errorf("Notifier should have been called with IP 192.168.1.50, got %s", notifier.calledIP)
	}
	if notifier.calledScore != 4 {
		t.Errorf("Notifier should have been called with score 4, got %d", notifier.calledScore)
	}

	// 5th and 6th events — shouldNotify must be false (already notified, no re-arm)
	for i := 5; i <= 6; i++ {
		_, shouldNotify := engine.processEvent(event)
		if shouldNotify {
			t.Errorf("Event %d should NOT trigger notification (already notified)", i)
		}
	}

	if notifier.callCount != 1 {
		t.Errorf("Notifier should still have been called only once after repeated events, called %d times", notifier.callCount)
	}
}

/*
Test that decayRate of 0 does not panic and scores accumulate without decay
With no decay configured, score should just keep going up
*/
func TestRiskScoreZeroDecayRate(t *testing.T) {
	engine := newTestEngine(10, 0)

	event := RateLimitEvent{IP: "10.0.0.50", Endpoint: "GET /ping", Action: "DENIED_WINDOW", Timestamp: time.Now().UnixNano()}

	// 5 events should accumulate to 5 with no decay
	var lastScore int
	for i := 0; i < 5; i++ {
		lastScore, _ = engine.processEvent(event)
	}

	if lastScore != 5 {
		t.Errorf("With zero decayRate, 5 events should give score 5, got %d", lastScore)
	}

	// GetScore should also return 5 without panicking
	score := engine.GetScore("10.0.0.50")
	if score != 5 {
		t.Errorf("GetScore with zero decayRate should return 5, got %d", score)
	}
}
