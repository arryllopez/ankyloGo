package main

import (
	"fmt"
	"log"
	"net/http"
	"time"

	ankylogo "github.com/arryllopez/ankyloGo"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Example using in-memory rate limiting (MemoryStore)
// For Redis-based rate limiting (distributed), see redis/main.go

func main() {
	router := gin.Default()

	// Using MemoryStore - stores rate limit data in application memory
	// Good for single-server deployments, data is lost on restart
	memoryStore := ankylogo.NewMemoryStore()
	// Kafka event publisher - publishes rate limit events to Kafka
	kafkaClient, err := kgo.NewClient(kgo.SeedBrokers("localhost:9092"))
	if err != nil {
		log.Fatalf("failed to create kafka client: %v", err)
	}
	defer kafkaClient.Close()

	ankyConfig := ankylogo.NewConfig(
		ankylogo.WithSlidingWindow(60, 100),
		ankylogo.WithTokenBucket(10, 1, time.Second),
		ankylogo.WithEventPublisher(ankylogo.NewKafkaPublisher(kafkaClient, "rate-limit-events")),
		ankylogo.WithPrometheusMetrics(prometheus.DefaultRegisterer),
		ankylogo.WithMiddlewareLatency(prometheus.DefaultRegisterer),
	)

	router.Use(ankylogo.RateLimiterMiddleware(memoryStore, ankyConfig))
	router.GET("/metrics", gin.WrapH(promhttp.Handler())) // applying the middleware

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
