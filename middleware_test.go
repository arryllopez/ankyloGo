package ankylogo

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// helper to create a test router with the middleware applied
func setupTestRouter(config Config, policies ...map[string]Config) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(RateLimiterMiddleware(NewMemoryStore(), config, policies...))
	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "pong"})
	})
	return router
}

// helper to make a GET request to /ping and return the response
func makeRequest(router *gin.Engine) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/ping", nil)
	router.ServeHTTP(w, req)
	return w
}

/*
Testing with both algorithms enabled (default config)
Using a small capacity of 2 for token bucket so it triggers quickly
Sliding window has a limit of 100 so token bucket will be the bottleneck
First 2 requests should pass, 3rd should be denied by the token bucket
*/
func TestMiddlewareBothAlgorithms(t *testing.T) {
	config := Config{
		Window:            60,
		Limit:             100,
		Capacity:          2,
		TokensPerInterval: 0,
		RefillRate:        time.Second,
	}
	router := setupTestRouter(config)

	// First 2 requests should succeed (token bucket capacity is 2)
	for i := 0; i < 2; i++ {
		w := makeRequest(router)
		if w.Code != http.StatusOK {
			t.Errorf("Request %d should return 200, got %d", i+1, w.Code)
		}
	}

	// 3rd request should be denied (token bucket empty, no refill)
	w := makeRequest(router)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Request 3 should return 429, got %d", w.Code)
	}
}

/*
Testing with only sliding window enabled
Capacity is 0 so token bucket is skipped entirely
Sliding window allows 3 requests in a 60 second window
4th request should be denied
*/
func TestMiddlewareSlidingWindowOnly(t *testing.T) {
	config := Config{
		Window: 60,
		Limit:  3,
		// Capacity is 0 so token bucket is skipped
	}
	router := setupTestRouter(config)

	// First 3 requests should succeed
	for i := 0; i < 3; i++ {
		w := makeRequest(router)
		if w.Code != http.StatusOK {
			t.Errorf("Request %d should return 200, got %d", i+1, w.Code)
		}
	}

	// 4th request should be denied by sliding window
	w := makeRequest(router)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Request 4 should return 429, got %d", w.Code)
	}
}

/*
Testing with only token bucket enabled
Window and Limit are 0 so sliding window is skipped entirely
Token bucket has a capacity of 2 with no refill
3rd request should be denied
*/
func TestMiddlewareTokenBucketOnly(t *testing.T) {
	config := Config{
		// Window and Limit are 0 so sliding window is skipped
		Capacity:          2,
		TokensPerInterval: 0,
		RefillRate:        time.Second,
	}
	router := setupTestRouter(config)

	// First 2 requests should succeed
	for i := 0; i < 2; i++ {
		w := makeRequest(router)
		if w.Code != http.StatusOK {
			t.Errorf("Request %d should return 200, got %d", i+1, w.Code)
		}
	}

	// 3rd request should be denied by token bucket
	w := makeRequest(router)
	if w.Code != http.StatusTooManyRequests {
		t.Errorf("Request 3 should return 429, got %d", w.Code)
	}
}

/*
Testing with neither algorithm configured (empty config)
All values are 0 so both algorithms are skipped
Every request should pass through with no rate limiting
*/
func TestMiddlewareNoRateLimiting(t *testing.T) {
	config := Config{
		// Everything is 0, no rate limiting
	}
	router := setupTestRouter(config)

	// All 20 requests should succeed since nothing is configured
	for i := 0; i < 20; i++ {
		w := makeRequest(router)
		if w.Code != http.StatusOK {
			t.Errorf("Request %d should return 200 (no rate limiting), got %d", i+1, w.Code)
		}
	}
}

// mock ScoreReader for testing enforcement tiers
type mockScoreReader struct {
	scores map[string]int
}

func (m *mockScoreReader) GetScore(ip string) int {
	if score, ok := m.scores[ip]; ok {
		return score
	}
	return 0
}

/*
Testing that an IP with a risk score at or above DenyScore gets 403 Forbidden
regardless of rate limit capacity. The request should never reach the algorithms.
*/
func TestMiddlewareRiskDenyAll(t *testing.T) {
	config := Config{
		Window:   60,
		Limit:    100,
		Capacity: 100,
		ScoreReader: &mockScoreReader{
			// all IPs get score 10 (empty string key since httptest has no RemoteAddr)
			scores: map[string]int{"": 10},
		},
		DenyScore: 10,
	}
	router := setupTestRouter(config)

	// Even the first request should be denied with 403 (not 429)
	w := makeRequest(router)
	if w.Code != http.StatusForbidden {
		t.Errorf("Request should return 403 Forbidden when score >= DenyScore, got %d", w.Code)
	}
}

/*
Testing that an IP with a moderate risk score gets reduced limits
Normal capacity is 10, with score at 50% of DenyScore the capacity should
be reduced to ~5 (factor 0.5), so the 6th request should be denied
*/
func TestMiddlewareRiskReducedLimits(t *testing.T) {
	config := Config{
		Capacity:          10,
		TokensPerInterval: 0,
		RefillRate:        time.Second,
		ScoreReader: &mockScoreReader{
			scores: map[string]int{"": 5},
		},
		DenyScore: 10,
	}
	router := setupTestRouter(config)

	// With score 5 out of DenyScore 10, factor = 0.5
	// Capacity 10 * 0.5 = 5 tokens
	passCount := 0
	for i := 0; i < 10; i++ {
		w := makeRequest(router)
		if w.Code == http.StatusOK {
			passCount++
		}
	}

	// Should allow exactly 5 requests (capacity 10 * 0.5 factor)
	if passCount != 5 {
		t.Errorf("With 50%% risk score, should allow 5 requests, allowed %d", passCount)
	}
}

/*
Testing that an IP with zero risk score gets normal limits (no reduction)
ScoreReader is set but score is 0 — limits should be unaffected
*/
func TestMiddlewareRiskZeroScore(t *testing.T) {
	config := Config{
		Capacity:          3,
		TokensPerInterval: 0,
		RefillRate:        time.Second,
		ScoreReader: &mockScoreReader{
			scores: map[string]int{"": 0},
		},
		DenyScore: 10,
	}
	router := setupTestRouter(config)

	// Score is 0, so full capacity of 3 should be available
	passCount := 0
	for i := 0; i < 5; i++ {
		w := makeRequest(router)
		if w.Code == http.StatusOK {
			passCount++
		}
	}

	if passCount != 3 {
		t.Errorf("With zero risk score, should allow 3 requests (full capacity), allowed %d", passCount)
	}
}

/*
Testing that risk reduction does not re-enable a disabled token bucket
User sets Capacity=0 (token bucket disabled), only sliding window active
Risk score should reduce the sliding window limit but NOT activate token bucket
*/
func TestMiddlewareRiskDoesNotEnableDisabledAlgorithm(t *testing.T) {
	config := Config{
		Window: 60,
		Limit:  10,
		// Capacity is 0 — token bucket intentionally disabled
		ScoreReader: &mockScoreReader{
			scores: map[string]int{"": 5},
		},
		DenyScore: 10,
	}
	router := setupTestRouter(config)

	// factor = 1.0 - 5/10 = 0.5, so Limit 10 * 0.5 = 5
	// Capacity should stay 0 (disabled), not get floored to 1
	passCount := 0
	for i := 0; i < 10; i++ {
		w := makeRequest(router)
		if w.Code == http.StatusOK {
			passCount++
		}
	}

	// Should allow exactly 5 requests (sliding window only, reduced by risk)
	if passCount != 5 {
		t.Errorf("With Capacity=0 and risk score, should allow 5 requests (sliding window only), allowed %d", passCount)
	}
}
