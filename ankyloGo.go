package ankylogo

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// ScoreReader returns the current risk score for an IP
type ScoreReader interface {
	GetScore(ip string) int
}

type Config struct {
	// sliding window
	Window int64
	Limit  int
	// token bucket
	Capacity          int
	TokensPerInterval int
	RefillRate        time.Duration
	// kafka
	EventPublisher EventPublisher
	// risk scoring — if ScoreReader is set, the middleware adjusts limits based on risk
	// DenyScore is the score at which all requests are denied (0 = disabled)
	ScoreReader ScoreReader
	DenyScore   int
}

// ConfigOption is a function that configures a Config
type ConfigOption func(*Config)

// WithSlidingWindow enables sliding window rate limiting
func WithSlidingWindow(window int64, limit int) ConfigOption {
	return func(c *Config) {
		c.Window = window
		c.Limit = limit
	}
}

// WithTokenBucket enables token bucket rate limiting
func WithTokenBucket(capacity, tokensPerInterval int, refillRate time.Duration) ConfigOption {
	return func(c *Config) {
		c.Capacity = capacity
		c.TokensPerInterval = tokensPerInterval
		c.RefillRate = refillRate
	}
}

// WithEventPublisher sets the Kafka event publisher
func WithEventPublisher(publisher EventPublisher) ConfigOption {
	return func(c *Config) {
		c.EventPublisher = publisher
	}
}

// WithRiskScoring enables risk-based progressive throttling
func WithRiskScoring(scoreReader ScoreReader, denyScore int) ConfigOption {
	return func(c *Config) {
		c.ScoreReader = scoreReader
		c.DenyScore = denyScore
	}
}

// NewConfig creates a new Config with optional configurations
// By default, no rate limiting is enabled. Use With* options to configure.
func NewConfig(opts ...ConfigOption) Config {
	config := Config{
		// Defaults: all disabled
		Window:            0,
		Limit:             0,
		Capacity:          0,
		TokensPerInterval: 0,
		RefillRate:        0,
		EventPublisher:    nil,
		ScoreReader:       nil,
		DenyScore:         0,
	}

	// Apply options
	for _, opt := range opts {
		opt(&config)
	}

	return config
}

// DefaultConfig returns a Config with sensible defaults for basic rate limiting
func DefaultConfig() Config {
	return Config{
		Window:            60,
		Limit:             100,
		Capacity:          10,
		TokensPerInterval: 1,
		RefillRate:        time.Second,
	}
}

// RateLimiterMiddleware returns a gin middleware that rate limits per IP
// using both a sliding window and a token bucket.
func RateLimiterMiddleware(store RateLimiterStore, config Config, endpointPolicies ...map[string]Config) gin.HandlerFunc {
	if config.Window == 0 && config.Limit == 0 && config.Capacity == 0 {
		log.Println("warning: no rate limiting configured, all requests will pass through")
	}

	// Validate token bucket configuration
	if config.Capacity > 0 && config.RefillRate <= 0 {
		log.Println("warning: token bucket enabled (Capacity > 0) but RefillRate is zero or negative - token bucket will not refill")
	}

	// Validate negative values
	if config.Limit < 0 {
		log.Println("warning: Limit is negative, which may cause unexpected behavior")
	}
	if config.Capacity < 0 {
		log.Println("warning: Capacity is negative, which may cause unexpected behavior")
	}
	if config.TokensPerInterval < 0 {
		log.Println("warning: TokensPerInterval is negative, which may cause unexpected behavior")
	}

	// Validate risk scoring configuration
	if config.DenyScore > 0 && config.ScoreReader == nil {
		log.Println("warning: DenyScore is set but ScoreReader is nil - risk scoring will not work")
	}

	return func(c *gin.Context) {
		ip := c.ClientIP()

		// Build key from method + path: "POST /login", "GET /search"
		key := c.Request.Method + " " + c.FullPath()

		// Check if this endpoint has a specific policy
		activeConfig := config // default fallback
		var policies map[string]Config
		if len(endpointPolicies) > 0 {
			policies = endpointPolicies[0]
		}
		if policies != nil {
			if policy, exists := policies[key]; exists {
				activeConfig = policy
			}
		}

		// Build the store key: include endpoint when per-endpoint policies are active
		// so different endpoints get separate rate limit counters
		storeKey := ip
		if policies != nil {
			if _, exists := policies[key]; exists {
				storeKey = ip + ":" + key
			}
		}

		// Dynamic enforcement: adjust limits based on risk score
		if config.ScoreReader != nil && config.DenyScore > 0 {
			riskScore := config.ScoreReader.GetScore(ip)
			if riskScore >= config.DenyScore {
				if config.EventPublisher != nil {
					config.EventPublisher.Publish(RateLimitEvent{
						IP:         ip,
						Endpoint:   key,
						Action:     "DENIED_RISK",
						Timestamp:  time.Now().UnixNano(),
						UserAgent:  c.Request.UserAgent(),
						StatusCode: http.StatusForbidden,
					})
				}
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
					"error": "Access temporarily restricted due to suspicious activity.",
				})
				return
			}
			if riskScore > 0 {
				// Proportionally reduce limits: higher score = tighter limits
				factor := 1.0 - (float64(riskScore) / float64(config.DenyScore))
				if factor < 0.1 {
					factor = 0.1
				}
				// Only reduce limits that were originally configured (> 0)
				// to avoid re-enabling algorithms the user intentionally disabled
				if activeConfig.Limit > 0 {
					activeConfig.Limit = int(float64(activeConfig.Limit) * factor)
					if activeConfig.Limit < 1 {
						activeConfig.Limit = 1
					}
				}
				if activeConfig.Capacity > 0 {
					activeConfig.Capacity = int(float64(activeConfig.Capacity) * factor)
					if activeConfig.Capacity < 1 {
						activeConfig.Capacity = 1
					}
				}
			}
		}

		if activeConfig.Window > 0 && activeConfig.Limit > 0 {
			var allowedWindow bool = store.AllowedSlidingWindow(storeKey, activeConfig.Window, activeConfig.Limit)

			if !allowedWindow {
				if config.EventPublisher != nil {
					config.EventPublisher.Publish(RateLimitEvent{
						IP:         ip,
						Endpoint:   key,
						Action:     "DENIED_WINDOW",
						Timestamp:  time.Now().UnixNano(),
						UserAgent:  c.Request.UserAgent(),
						StatusCode: http.StatusTooManyRequests,
					})
				}

				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": "Too many requests. Please try again later.",
				})
				return
			}
		}

		if activeConfig.Capacity > 0 {
			var allowedBucket bool = store.AllowedTokenBucket(storeKey, activeConfig.Capacity, activeConfig.TokensPerInterval, activeConfig.RefillRate)

			if !allowedBucket {
				if config.EventPublisher != nil {
					config.EventPublisher.Publish(RateLimitEvent{
						IP:         ip,
						Endpoint:   key,
						Action:     "DENIED_BUCKET",
						Timestamp:  time.Now().UnixNano(),
						UserAgent:  c.Request.UserAgent(),
						StatusCode: http.StatusTooManyRequests,
					})
				}

				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
					"error": "Too many requests. Please try again later.",
				})
				return
			}
		}

		c.Next()

		if config.EventPublisher != nil {
			config.EventPublisher.Publish(RateLimitEvent{
				IP:         ip,
				Endpoint:   key,
				Action:     "ALLOWED",
				Timestamp:  time.Now().UnixNano(),
				UserAgent:  c.Request.UserAgent(),
				StatusCode: c.Writer.Status(),
			})
		}
	}
}
