package main

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	ankylogo "github.com/arryllopez/ankyloGo"
	"github.com/gin-gonic/gin"
)

// Enforcement accuracy test - validates rate limiter correctly blocks abuse
// Run with: go run loadtest/enforcement_test.go

type TestResult struct {
	TotalRequests    int
	AllowedRequests  int
	DeniedRequests   int
	ExpectedAllowed  int
	ExpectedDenied   int
	AccuracyPercent  float64
	TestName         string
}

func main() {
	gin.SetMode(gin.ReleaseMode)

	fmt.Println("===========================================")
	fmt.Println("ankyloGo Enforcement Accuracy Test")
	fmt.Println("===========================================")
	fmt.Println()

	// Test 1: Sliding Window Enforcement
	fmt.Println("Test 1: Sliding Window Enforcement")
	fmt.Println("-------------------------------------------")
	result1 := testSlidingWindowEnforcement()
	printResult(result1)

	time.Sleep(2 * time.Second)

	// Test 2: Token Bucket Enforcement
	fmt.Println("\nTest 2: Token Bucket Enforcement")
	fmt.Println("-------------------------------------------")
	result2 := testTokenBucketEnforcement()
	printResult(result2)

	time.Sleep(2 * time.Second)

	// Test 3: Dual Algorithm Under Attack
	fmt.Println("\nTest 3: Dual Algorithm Under Attack")
	fmt.Println("-------------------------------------------")
	result3 := testDualAlgorithmUnderAttack()
	printResult(result3)

	fmt.Println("\n===========================================")
	fmt.Println("All Tests Complete!")
	fmt.Println("===========================================")
}

// Test 1: Sliding Window - Send 150 requests, limit is 100/60sec
func testSlidingWindowEnforcement() TestResult {
	memoryStore := ankylogo.NewMemoryStore()
	config := ankylogo.Config{
		Window:     60,  // 60 second window
		Limit:      100, // 100 requests max
		Capacity:   0,   // Disable token bucket
	}

	router := gin.New()
	router.Use(ankylogo.RateLimiterMiddleware(memoryStore, config))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Start server
	server := &http.Server{
		Addr:    ":8081",
		Handler: router,
	}

	go server.ListenAndServe()
	time.Sleep(500 * time.Millisecond) // Wait for server to start

	// Send requests
	totalRequests := 150
	allowed := 0
	denied := 0

	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < totalRequests; i++ {
		resp, err := client.Get("http://localhost:8081/test")
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			allowed++
		} else if resp.StatusCode == http.StatusTooManyRequests {
			denied++
		}
		resp.Body.Close()
	}

	server.Close()

	expectedAllowed := 100
	expectedDenied := 50
	accuracy := calculateAccuracy(allowed, denied, expectedAllowed, expectedDenied)

	return TestResult{
		TotalRequests:   totalRequests,
		AllowedRequests: allowed,
		DeniedRequests:  denied,
		ExpectedAllowed: expectedAllowed,
		ExpectedDenied:  expectedDenied,
		AccuracyPercent: accuracy,
		TestName:        "Sliding Window",
	}
}

// Test 2: Token Bucket - Send requests at high rate, measure burst handling
func testTokenBucketEnforcement() TestResult {
	memoryStore := ankylogo.NewMemoryStore()
	config := ankylogo.Config{
		Window:            0,                // Disable sliding window
		Limit:             0,
		Capacity:          10,               // 10 token capacity
		TokensPerInterval: 1,                // Refill 1 token per interval
		RefillRate:        time.Millisecond * 100, // Refill every 100ms
	}

	router := gin.New()
	router.Use(ankylogo.RateLimiterMiddleware(memoryStore, config))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	server := &http.Server{
		Addr:    ":8082",
		Handler: router,
	}

	go server.ListenAndServe()
	time.Sleep(500 * time.Millisecond)

	// Send burst of 20 requests immediately (should allow 10, deny 10)
	totalRequests := 20
	allowed := 0
	denied := 0

	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < totalRequests; i++ {
		resp, err := client.Get("http://localhost:8082/test")
		if err != nil {
			continue
		}

		if resp.StatusCode == http.StatusOK {
			allowed++
		} else if resp.StatusCode == http.StatusTooManyRequests {
			denied++
		}
		resp.Body.Close()
	}

	server.Close()

	expectedAllowed := 10
	expectedDenied := 10
	accuracy := calculateAccuracy(allowed, denied, expectedAllowed, expectedDenied)

	return TestResult{
		TotalRequests:   totalRequests,
		AllowedRequests: allowed,
		DeniedRequests:  denied,
		ExpectedAllowed: expectedAllowed,
		ExpectedDenied:  expectedDenied,
		AccuracyPercent: accuracy,
		TestName:        "Token Bucket",
	}
}

// Test 3: Dual Algorithm Under Attack - Concurrent requests from single IP
func testDualAlgorithmUnderAttack() TestResult {
	memoryStore := ankylogo.NewMemoryStore()
	config := ankylogo.Config{
		Window:            10,  // 10 second window
		Limit:             50,  // 50 requests max
		Capacity:          20,  // 20 token capacity
		TokensPerInterval: 5,   // Refill 5 tokens per interval
		RefillRate:        time.Millisecond * 100,
	}

	router := gin.New()
	router.Use(ankylogo.RateLimiterMiddleware(memoryStore, config))
	router.GET("/test", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	server := &http.Server{
		Addr:    ":8083",
		Handler: router,
	}

	go server.ListenAndServe()
	time.Sleep(500 * time.Millisecond)

	// Send 100 concurrent requests (simulating attack)
	totalRequests := 100
	allowed := 0
	denied := 0

	var mu sync.Mutex
	var wg sync.WaitGroup
	client := &http.Client{Timeout: 5 * time.Second}

	for i := 0; i < totalRequests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			resp, err := client.Get("http://localhost:8083/test")
			if err != nil {
				return
			}
			defer resp.Body.Close()

			mu.Lock()
			if resp.StatusCode == http.StatusOK {
				allowed++
			} else if resp.StatusCode == http.StatusTooManyRequests {
				denied++
			}
			mu.Unlock()
		}()
	}

	wg.Wait()
	server.Close()

	// Token bucket will stop it first at ~20 requests
	// So we expect roughly 20 allowed, 80 denied
	expectedAllowed := 20
	expectedDenied := 80

	// Allow 20% margin for timing variance
	accuracy := calculateAccuracyWithMargin(allowed, denied, expectedAllowed, expectedDenied, 0.20)

	return TestResult{
		TotalRequests:   totalRequests,
		AllowedRequests: allowed,
		DeniedRequests:  denied,
		ExpectedAllowed: expectedAllowed,
		ExpectedDenied:  expectedDenied,
		AccuracyPercent: accuracy,
		TestName:        "Dual Algorithm Under Attack",
	}
}

func calculateAccuracy(allowed, denied, expectedAllowed, expectedDenied int) float64 {
	allowedDiff := abs(allowed - expectedAllowed)
	deniedDiff := abs(denied - expectedDenied)
	totalExpected := expectedAllowed + expectedDenied
	totalDiff := allowedDiff + deniedDiff

	accuracy := float64(totalExpected-totalDiff) / float64(totalExpected) * 100
	if accuracy < 0 {
		accuracy = 0
	}
	return accuracy
}

func calculateAccuracyWithMargin(allowed, denied, expectedAllowed, expectedDenied int, margin float64) float64 {
	allowedMin := int(float64(expectedAllowed) * (1 - margin))
	allowedMax := int(float64(expectedAllowed) * (1 + margin))
	deniedMin := int(float64(expectedDenied) * (1 - margin))
	deniedMax := int(float64(expectedDenied) * (1 + margin))

	allowedInRange := allowed >= allowedMin && allowed <= allowedMax
	deniedInRange := denied >= deniedMin && denied <= deniedMax

	if allowedInRange && deniedInRange {
		return 100.0
	}

	return calculateAccuracy(allowed, denied, expectedAllowed, expectedDenied)
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func printResult(r TestResult) {
	fmt.Printf("Test: %s\n", r.TestName)
	fmt.Printf("Total Requests:    %d\n", r.TotalRequests)
	fmt.Printf("Allowed Requests:  %d (expected: %d)\n", r.AllowedRequests, r.ExpectedAllowed)
	fmt.Printf("Denied Requests:   %d (expected: %d)\n", r.DeniedRequests, r.ExpectedDenied)
	fmt.Printf("Accuracy:          %.1f%%\n", r.AccuracyPercent)

	if r.AccuracyPercent >= 95.0 {
		fmt.Println("Status:            ✓ PASS")
	} else if r.AccuracyPercent >= 85.0 {
		fmt.Println("Status:            ~ MARGINAL")
	} else {
		fmt.Println("Status:            ✗ FAIL")
	}
}
