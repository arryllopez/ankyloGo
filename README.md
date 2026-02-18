
<p align="center">
<img width="200" height="200" alt="d96bebf9-b1f8-4c67-bb2c-00e715141de4-removebg-preview" src="https://github.com/user-attachments/assets/36a7c8bb-7b81-422f-8f0b-9762830c33c1" />
</p>

# ankyloGo

A rate limiting middleware for [Gin](https://github.com/gin-gonic/gin) that enforces per-IP limits using a **token bucket** and **sliding window** working in tandem. Both algorithms must pass for a request to proceed.

Optionally integrates a Kafka-backed **risk engine** that accumulates an abuse score per IP based on traffic patterns and dynamically tightens limits — no static rules required.

```
go get github.com/arryllopez/ankyloGo
```

---

## How It Works

```
Request → IP extraction
        → [Optional] Risk score lookup → adjust limits or deny immediately
        → Sliding window check → deny or continue
        → Token bucket check  → deny or continue
        → c.Next()
        → [Optional] Publish event to Kafka
```

- **Sliding window** — enforces a sustained request cap over a rolling time period (e.g. 100 req/min).
- **Token bucket** — handles burst control. Tokens refill at a fixed rate; an empty bucket denies the request.
- **Risk engine** — Kafka consumer running as a goroutine. Accumulates an integer score per IP from rate limit events. Higher scores progressively reduce effective limits. At the configured threshold, the IP is blocked until the score decays.

Scores decay automatically — no IP is punished indefinitely.

---

## Stores

| Store | When to use |
|---|---|
| `MemoryStore` | Single-server. Fast, zero dependencies. State lost on restart. |
| `RedisStore` | Distributed / multi-instance. Atomic Lua scripts. Fails open on Redis errors. |

---

## Quick Start

**Memory store, no external dependencies:**

```go
import (
    "time"
    ankylogo "github.com/arryllopez/ankyloGo"
    "github.com/gin-gonic/gin"
)

func main() {
    router := gin.Default()

    store  := ankylogo.NewMemoryStore()
    config := ankylogo.NewConfig(
        ankylogo.WithSlidingWindow(60, 100),           // 100 requests per 60s
        ankylogo.WithTokenBucket(10, 1, time.Second),  // burst of 10, refill 1/sec
    )

    router.Use(ankylogo.RateLimiterMiddleware(store, config))
    router.Run(":8080")
}
```

**Redis store:**

```go
redisClient := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
store  := ankylogo.NewRedisStore(redisClient)
config := ankylogo.NewConfig(
    ankylogo.WithSlidingWindow(60, 100),
    ankylogo.WithTokenBucket(10, 1, time.Second),
)

router.Use(ankylogo.RateLimiterMiddleware(store, config))
```

---

## Per-Endpoint Policies

Pass a policy map as the third argument. Routes not in the map fall back to the global config. Keys match `"METHOD /path"`.

```go
policies := map[string]ankylogo.Config{
    "POST /login": ankylogo.NewConfig(
        ankylogo.WithSlidingWindow(60, 10),
        ankylogo.WithTokenBucket(5, 1, time.Second),
    ),
    "POST /purchase": ankylogo.NewConfig(
        ankylogo.WithSlidingWindow(60, 5),
        ankylogo.WithTokenBucket(3, 1, time.Second),
    ),
}

router.Use(ankylogo.RateLimiterMiddleware(store, config, policies))
```

---

## Risk Engine

The risk engine consumes rate limit events from Kafka and adjusts limits per IP in real time.

**Default event weights:**

| Event | Default weight |
|---|---|
| `ALLOWED` | 0 |
| `DENIED_WINDOW` | 1 |
| `DENIED_BUCKET` | 4 |
| `DENIED_RISK` | 10 |

When an IP's score reaches `denyScore`, all its requests are denied with `403 Forbidden` until the score decays back below the threshold.

Two implementations are provided:

| Engine | Scores stored in | Use when |
|---|---|---|
| `RiskEngine` | In-process memory | Single server |
| `RedisRiskEngine` | Redis | Multi-instance — scores are shared |

**In-memory engine:**

```go
import (
    "context"
    ankylogo "github.com/arryllopez/ankyloGo"
    "github.com/twmb/franz-go/pkg/kgo"
)

kafkaClient, _ := kgo.NewClient(
    kgo.SeedBrokers("localhost:9092"),
    kgo.ConsumeTopics("rate-limit-events"),
)

engine := ankylogo.NewRiskEngine(kafkaClient, 15, "rate-limit-events")

config := ankylogo.NewConfig(
    ankylogo.WithSlidingWindow(60, 100),
    ankylogo.WithTokenBucket(10, 1, time.Second),
    ankylogo.WithEventPublisher(ankylogo.NewKafkaPublisher(kafkaClient, "rate-limit-events")),
    ankylogo.WithRiskEngine(context.Background(), engine, 15), // deny at score >= 15
)
```

`WithRiskEngine` starts the Kafka consumer goroutine automatically.

**Redis-backed engine (distributed):**

```go
engine := ankylogo.NewRedisRiskEngine(
    kafkaClient,
    redisClient,
    15,
    "rate-limit-events",
    ankylogo.WithKeyTTL(24 * time.Hour),            // auto-expire idle IPs
    ankylogo.WithRedisTimeout(100 * time.Millisecond),
)
```

### Score Decay

```go
// Linear — subtract 1 point every 10 minutes
ankylogo.WithDecayRate(10 * time.Minute)

// Half-life — halve the score every 30 minutes
ankylogo.WithHalfLifeDecay()
ankylogo.WithDecayRate(30 * time.Minute)
```

### Custom Weights

```go
engine := ankylogo.NewRiskEngine(kafkaClient, 15, "rate-limit-events",
    ankylogo.WithWeightAllowed(0),
    ankylogo.WithWeightWindow(2),
    ankylogo.WithWeightBucket(5),
    ankylogo.WithWeightPassedThreshold(15),
)
```

### Threshold Notifications

Called once when an IP first crosses the deny threshold. Re-arms automatically when the score decays back below the threshold.

```go
type myNotifier struct{}

func (n *myNotifier) Notify(ip string, score int) {
    log.Printf("IP %s hit risk threshold at score %d", ip, score)
}

engine := ankylogo.NewRiskEngine(kafkaClient, 15, "rate-limit-events",
    ankylogo.WithThresholdNotifier(&myNotifier{}),
)
```

---

## Observability

### Prometheus

Three metrics are available. All are opt-in.

| Metric | Type | Labels | Description |
|---|---|---|---|
| `ankylosaur_requests_total` | Counter | `action`, `endpoint` | Every rate limit decision |
| `ankylosaur_threshold_crossings_total` | Counter | — | Unique IPs that crossed the risk threshold |
| `ankylosaur_middleware_duration_seconds` | Histogram | `endpoint` | Middleware latency per endpoint |

`action` is one of: `ALLOWED`, `DENIED_WINDOW`, `DENIED_BUCKET`, `DENIED_RISK`.

```go
import (
    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promhttp"
)

config := ankylogo.NewConfig(
    ankylogo.WithSlidingWindow(60, 100),
    ankylogo.WithTokenBucket(10, 1, time.Second),
    ankylogo.WithPrometheusMetrics(prometheus.DefaultRegisterer),   // requests counter
    ankylogo.WithMiddlewareLatency(prometheus.DefaultRegisterer),   // latency histogram
)

router.GET("/metrics", gin.WrapH(promhttp.Handler()))
```

`WithPrometheusMetrics` wraps any existing event publisher, so Kafka and Prometheus work together:

```go
ankylogo.WithEventPublisher(ankylogo.NewKafkaPublisher(kafkaClient, "rate-limit-events")),
ankylogo.WithPrometheusMetrics(prometheus.DefaultRegisterer), // wraps the Kafka publisher
```

**Threshold crossings counter** — wire into the risk engine:

```go
notifier := ankylogo.NewPrometheusThresholdNotifier(prometheus.DefaultRegisterer)

engine := ankylogo.NewRiskEngine(kafkaClient, 15, "rate-limit-events",
    ankylogo.WithThresholdNotifier(notifier),
)
```

### Grafana + Prometheus (local dev)

A Docker Compose stack is included in `monitoring/`:

```
cd monitoring
docker compose up -d
```

| Service | URL | Credentials |
|---|---|---|
| Prometheus | http://localhost:9090 | — |
| Grafana | http://localhost:3000 | admin / admin |

Prometheus scrapes your app at `host.docker.internal:8081`. Grafana auto-provisions Prometheus as a datasource.

---

## Configuration Reference

### `NewConfig` options

| Option | Description |
|---|---|
| `WithSlidingWindow(window int64, limit int)` | Window in seconds, max requests per window |
| `WithTokenBucket(capacity, tokensPerInterval int, refillRate time.Duration)` | Burst capacity, refill amount, refill interval |
| `WithEventPublisher(publisher EventPublisher)` | Kafka or custom publisher |
| `WithRiskEngine(ctx, engine RiskScorer, denyScore int)` | Attach engine and start consumer goroutine |
| `WithRiskScoring(scoreReader ScoreReader, denyScore int)` | Attach a ScoreReader without starting a goroutine |
| `WithPrometheusMetrics(reg prometheus.Registerer)` | Enable `ankylosaur_requests_total` |
| `WithMiddlewareLatency(reg prometheus.Registerer)` | Enable `ankylosaur_middleware_duration_seconds` |

### `RiskEngineOption` options

| Option | Description |
|---|---|
| `WithWeightAllowed(w int)` | Score delta for allowed requests (default: 0) |
| `WithWeightWindow(w int)` | Score delta for window denials (default: 1) |
| `WithWeightBucket(w int)` | Score delta for bucket denials (default: 4) |
| `WithWeightPassedThreshold(w int)` | Score delta for risk denials (default: 10) |
| `WithDecayRate(d time.Duration)` | Interval between decay steps |
| `WithHalfLifeDecay()` | Exponential (half-life) decay instead of linear |
| `WithThresholdNotifier(n ThresholdNotifier)` | Callback on first threshold crossing per IP |
| `WithKeyTTL(ttl time.Duration)` | _(Redis only)_ Auto-expire idle IP keys |
| `WithRedisTimeout(t time.Duration)` | _(Redis only)_ Per-operation context timeout |

---

## Interfaces

Implement these to extend ankyloGo without modifying it.

```go
// EventPublisher receives a decision event after each request.
type EventPublisher interface {
    Publish(event RateLimitEvent)
}

// ScoreReader returns the current risk score for an IP.
type ScoreReader interface {
    GetScore(ip string) int
}

// RiskScorer is implemented by RiskEngine and RedisRiskEngine.
type RiskScorer interface {
    GetScore(ip string) int
    EventReader(ctx context.Context)
}

// ThresholdNotifier fires once when an IP first crosses the deny threshold.
type ThresholdNotifier interface {
    Notify(ip string, score int)
}

// RateLimiterStore is the storage backend for rate limit state.
type RateLimiterStore interface {
    AllowedSlidingWindow(ip string, window int64, limit int) bool
    AllowedTokenBucket(ip string, capacity, tokensPerInterval int, refillRate time.Duration) bool
}
```

`RateLimitEvent` fields: `IP`, `Endpoint`, `Action`, `Timestamp`, `UserAgent`, `StatusCode`.

---

## Requirements

| Dependency | Required for |
|---|---|
| [gin-gonic/gin](https://github.com/gin-gonic/gin) | Middleware (always required) |
| [redis/go-redis](https://github.com/redis/go-redis) | `RedisStore`, `RedisRiskEngine` |
| [twmb/franz-go](https://github.com/twmb/franz-go) | `KafkaPublisher`, `RiskEngine`, `RedisRiskEngine` |
| [prometheus/client_golang](https://github.com/prometheus/client_golang) | Prometheus metrics |

All dependencies except Gin are optional. Use only what your setup requires.

---

## License

MIT
