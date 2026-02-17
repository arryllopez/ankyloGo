package main

import (
	"log"
	"net/http"
	"time"

	ankylogo "github.com/arryllopez/ankyloGo"
	"github.com/gin-gonic/gin"
)

// Simple test server for load testing
// Run this with: go run loadtest/server.go
func main() {
	gin.SetMode(gin.ReleaseMode) // Disable debug logging for accurate benchmarks

	// Test WITHOUT middleware (baseline)
	routerBaseline := gin.New()
	routerBaseline.GET("/baseline", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	// Test WITH middleware
	routerWithMiddleware := gin.New()
	memoryStore := ankylogo.NewMemoryStore()
	config := ankylogo.Config{
		Window:            60,              // 60 second window
		Limit:             1000000,         // 1M requests per window (won't hit limit)
		Capacity:          100000,          // 100K token capacity (won't hit limit)
		TokensPerInterval: 10000,           // Fast refill
		RefillRate:        time.Millisecond, // Refill every 1ms
		EventPublisher:    nil,             // Disable Kafka for pure middleware overhead test
	}
	routerWithMiddleware.Use(ankylogo.RateLimiterMiddleware(memoryStore, config))
	routerWithMiddleware.GET("/with-middleware", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"message": "ok"})
	})

	// Mount both on same server
	router := gin.New()
	router.Any("/baseline", gin.WrapH(routerBaseline))
	router.Any("/with-middleware", gin.WrapH(routerWithMiddleware))

	log.Println("Load test server starting on :8080")
	log.Println("Endpoints:")
	log.Println("  /baseline          - No middleware (baseline)")
	log.Println("  /with-middleware   - With ankyloGo middleware")
	log.Println("")
	log.Println("Run load tests with:")
	log.Println("  hey -n 100000 -c 100 http://localhost:8080/baseline")
	log.Println("  hey -n 100000 -c 100 http://localhost:8080/with-middleware")

	router.Run(":8080")
}
