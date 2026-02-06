package main

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// token bucket algo
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

// implementing custom middleware
// this Logging middleware will be where the algorithm that has rate limiting via token bucket algo
func Logger() gin.HandlerFunc {
	tb := NewTokenBucket(10, 1, time.Second) // capacity 10, refill 1 token per second

	return func(c *gin.Context) {
		if allowed := tb.TakeTokens(); !allowed {
			// if no tokens are available, we return 429 status code
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "Too many requests. Please try again later.",
			})
			return
		}
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
