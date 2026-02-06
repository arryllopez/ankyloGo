# ankyloGo

Abuse-aware rate limiting middleware for [Gin](https://gin-gonic.com). Not just a rate limiter — it watches how traffic behaves and adapts.

Traditional rate limiters treat all requests equally. ankyloGo applies context: a failed login costs more than a successful search, a sudden geo jump raises suspicion, and limits tighten automatically when behavior looks automated.

```go
import "github.com/arryllopez/ankylosaur/ankylogo"

router.Use(ankylogo.New(ankylogo.Config{
    Storage: redisStore,
}))
```

## What It Does

A token bucket + sliding window rate limiter with a feedback loop. The rate limiter enforces limits. A separate risk engine watches access patterns via Kafka and adjusts those limits per actor in real time.

```
Request → Extract Identity (IP / API Key)
        → Check Local Cache (L1)
        → Check Redis (L2)
        → Token Bucket + Sliding Window → ALLOW or DENY
        → Async publish access log to Kafka
                    ↓
              Risk Engine (Kafka consumer, separate process)
                    ↓
              Writes risk score back to Redis
                    ↓
              Gateway reads score on next request → adjusts limits
```

## Rate Limiting

Two algorithms, both must pass:

- **Token Bucket** — handles bursts. Tokens refill at a steady rate. If the bucket is empty, request is denied. Allows legitimate traffic spikes while capping sustained abuse.
- **Sliding Window Counter** — enforces sustained rate caps. Uses weighted interpolation across current and previous windows for accuracy without storing every timestamp.

Limits are tracked **per IP** and **per API key** independently.

## Endpoint Risk Profiles

Different endpoints get different treatment:

| Endpoint | Risk | Bucket Size | Refill | Window Limit | Fail Mode | Cost |
|---|---|---|---|---|---|---|
| `GET /ping` | Low | 1000 | 100/s | — | Open | 1 |
| `POST /login` | High | 20 | 2/s | 10/min | Closed | 2 (+5 on failure) |
| `GET /search` | Medium | 50 | 5/s | — | Open | 1 |
| `POST /purchase` | Critical | 10 | 1/s | 5/min | Closed | 5 |

**Fail-open:** If Redis is unreachable, `/ping` and `/search` still serve traffic.
**Fail-closed:** `/login` and `/purchase` deny requests when state can't be verified.

## Storage

Two-tier architecture:

- **L1 — In-memory LRU cache** for hot keys. 1-second TTL. Only caches "comfortably allowed" decisions (tokens > 20% capacity). Reduces Redis roundtrips ~80%.
- **L2 — Redis** is the source of truth. All writes go to Redis. Atomic check-and-decrement via Lua scripts. Singleflight prevents cache stampedes on L1 miss.

## Kafka Pipeline

The middleware async-publishes an access log event for every request. A bounded in-memory queue sits between the middleware and the Kafka producer. If the queue fills up, events are dropped — the request path is never blocked by observability.

Access log events include: actor hash, endpoint, method, status code, user agent, decision, timestamps.

## Risk Engine

A separate Kafka consumer process that scores actors based on five pattern detectors:

| Pattern | Weight | What It Detects |
|---|---|---|
| Failed auth storms | 0.35 | Rapid 401/403 responses — credential stuffing |
| Geo jumps | 0.25 | Impossible travel between consecutive requests |
| User-Agent churn | 0.15 | Frequent UA rotation — bot behavior |
| Endpoint cardinality | 0.15 | Too many unique endpoints — API scraping |
| Request spikes | 0.10 | Current rate vs historical baseline |

Final score = weighted sum, range 0.0–1.0. Scores decay with a 30-minute half-life — no permanent bans.

## Dynamic Enforcement

The gateway reads the actor's risk score from Redis on each request and adjusts limits:

| Score | Action |
|---|---|
| < 0.3 | Normal limits |
| 0.3 – 0.5 | Reduce burst capacity (0.7x) |
| 0.5 – 0.7 | Increase request cost (2x) |
| 0.7 – 0.85 | Require step-up auth on high/critical endpoints |
| >= 0.85 | Cooldown — deny all requests for 5 minutes |

Graduated response. Requires 2+ correlated signals before any action. Grace period for new actors.

## Replay Tool

CLI tool to simulate traffic patterns against the gateway and observe decisions in logs:

- **Credential stuffing** — rapid failed logins from rotating IPs
- **Scraper** — steady enumeration of search endpoints
- **Legitimate burst** — short spike from a single user

## Infrastructure

Requires Redis and Kafka. Docker Compose config included for local dev:

```
redis:7          — distributed rate limit state
zookeeper+kafka  — access log pipeline
gateway          — your Gin app with ankyloGo middleware
consumer         — risk engine (can run multiple replicas)
```

## Status

Work in progress. Stub product API endpoints are set up. Core middleware implementation is next.

## Key Tradeoffs

These are the design decisions that make this more than a toy:

- **Redis vs memory** — hot reads cached locally, all writes and near-limit decisions go to Redis
- **Stampede avoidance** — singleflight deduplicates concurrent Redis fetches for the same key
- **Idempotency** — `/purchase` retries with the same idempotency key don't double-count against limits
- **At-least-once delivery** — Kafka may duplicate events; risk engine tolerates this because scoring is aggregate
- **Backpressure** — bounded queue, drop on overflow, never block the request path
- **False positives** — graduated response, auto-decay, allowlists, multi-signal requirement
