package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// The following code block implements the Token Bucket Algoritm
type TokenBucket struct {
	tokens       int
	capacity     int
	refillRate   time.Duration
	stopRefiller chan struct{} //signal to stop refilling
	mu           sync.Mutex    // handling race conditions (two processes trying to access tokens simultaneously)
}

func NewTokenBucket(capacity, tokensPerInterval int, refillRate time.Duration) *TokenBucket {
	tb := &TokenBucket{
		capacity:     capacity,
		refillRate:   refillRate,
		stopRefiller: make(chan struct{}),
	}
	go tb.refillTokens(tokensPerInterval) // start with a full bucket
	return tb
}

func (tb *TokenBucket) refillTokens(tokensPerInterval int) {
	// ticker is a great way to do something repeatedly to know more
	// check this out - https://gobyexample.com/tickers
	ticker := time.NewTicker(tb.refillRate)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// handle race conditions
			tb.mu.Lock()
			if tb.tokens+tokensPerInterval <= tb.capacity {
				// if we won't exceed the capacity add tokensPerInterval
				// tokens into our bucket
				tb.tokens += tokensPerInterval
			} else {
				// as we cant add more than capacity tokens, set
				// current tokens to bucket's capacity
				tb.tokens = tb.capacity
			}
			tb.mu.Unlock()
		case <-tb.stopRefiller:
			// let's stop refilling
			return
		}
	}
}

func (tb *TokenBucket) TakeTokens() bool {
	// handle race conditions
	tb.mu.Lock()
	defer tb.mu.Unlock()

	// if there are tokens available in the bucket, we take one out
	// in this case request goes through, thus we return true.
	if tb.tokens > 0 {
		tb.tokens--
		return true
	}
	// in the case where tokens are unavailable, this request won't
	// go through, so we return false
	return false
}

func (tb *TokenBucket) StopRefiller() {
	// close the channel
	close(tb.stopRefiller)
}

// The following block implements the sliding window algorithm

type RateLimiter interface {
	SetRate(rate float64)
	SetWindow(window time.Duration)
	Allow() bool
}

type SlidingWindow struct {
	mu      sync.Mutex
	count   int
	window  time.Duration
	history []int
}

// increment method increments the request counter and adds the current ocunt to the history
func (sw *SlidingWindow) increment() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.count++
	sw.history = append(sw.history, sw.count)
}

// removeExpired method removes expired ocunts
func (sw *SlidingWindow) removeExpired(now time.Time) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	for len(sw.history) > 0 && now.Sub(time.Unix(0, int64(sw.history[0]))) >= sw.window {
		sw.history = sw.history[1:]
	}
}

func (sw *SlidingWindow) countRequests(now time.Time) int {
	sw.removeExpired(now)
	return len(sw.history)
}

func (sw *SlidingWindow) Allow() bool {
	now := time.Now()
	count := sw.countRequests(now)
	if count >= sw.count {
		return false
	}
	sw.increment()
	return true
}

func (sw *SlidingWindow) SetRate(rate float64) {
	sw.count = int(rate * float64(sw.window) / float64(time.Second))
}

func (sw *SlidingWindow) SetWindow(window time.Duration) {
	sw.window = window
}

// implementing custom middleware
// this Logging middleware will be where the algorithm that has rate limiting via token bucket algo and sliding window
func Logger() gin.HandlerFunc {
	var bucketPerIp sync.Map

	return func(c *gin.Context) {
		//load the ip
		ip := c.ClientIP()
		// look up ip in the map
		value, ok := bucketPerIp.Load(ip)
		var bucket *TokenBucket
		if ok {
			bucket = value.(*TokenBucket)
		} else {
			bucket = NewTokenBucket(10, 1, time.Second)
			bucketPerIp.Store(ip, bucket)
		}
		if allowed := bucket.TakeTokens(); !allowed {
			// if no tokens are available, we return 429 status code
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many requests. Please try again later.",
			})
			return
		}
		c.Next()
	}
}

func main() {
	router := gin.Default()
	router.Use(Logger()) // applying the middleware

	// LoggerWithFormatter middleware will write the logs to gin.DefaultWriter
	// By default gin.DefaultWriter = os.Stdout
	router.Use(gin.LoggerWithFormatter(func(param gin.LogFormatterParams) string {
		// your custom format
		return fmt.Sprintf("%s - [%s] \"%s %s %s %d %s \"%s\" %s\"\n",
			param.ClientIP,
			param.TimeStamp.Format(time.RFC1123),
			param.Method,
			param.Path,
			param.Request.Proto,
			param.StatusCode,
			param.Latency,
			param.Request.UserAgent(),
			param.ErrorMessage,
		)
	}))
	router.Use(gin.Recovery())

	router.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	router.POST("/login", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "login successful",
		})
	})

	router.GET("/search", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "search completed",
		})
	})

	router.POST("/purchase", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "purchase successful",
		})
	})

	router.GET("/test", func(c *gin.Context) {
		example := c.MustGet("example").(string)
		// it would print: "12345"
		log.Println(example)
	})

	router.Run() // listen and serve on 0.0.0.0:8080
}
