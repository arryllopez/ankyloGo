package ankylogo

import "github.com/prometheus/client_golang/prometheus"

// --- requests counter ---

type prometheusPublisher struct {
	inner    EventPublisher
	requests *prometheus.CounterVec
}

// NewPrometheusPublisher wraps an optional inner EventPublisher and records
// ankylosaur_requests_total{action, endpoint} for every rate-limit decision.
//
// action is one of: ALLOWED, DENIED_WINDOW, DENIED_BUCKET, DENIED_RISK.
// endpoint is the method+path key, e.g. "POST /login".
//
// Pass prometheus.DefaultRegisterer for the global registry, or a custom
// *prometheus.Registry for isolation (e.g. in tests).
// inner may be nil if you don't need Kafka publishing alongside metrics.
func NewPrometheusPublisher(inner EventPublisher, reg prometheus.Registerer) EventPublisher {
	requests := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "ankylosaur_requests_total",
		Help: "Total requests processed by the rate limiter, labeled by action and endpoint.",
	}, []string{"action", "endpoint"})
	reg.MustRegister(requests)
	return &prometheusPublisher{inner: inner, requests: requests}
}

func (p *prometheusPublisher) Publish(event RateLimitEvent) {
	p.requests.WithLabelValues(event.Action, event.Endpoint).Inc()
	if p.inner != nil {
		p.inner.Publish(event)
	}
}

// WithPrometheusMetrics wraps the current EventPublisher (if any) with Prometheus
// instrumentation. Safe to chain with WithEventPublisher in any order.
func WithPrometheusMetrics(reg prometheus.Registerer) ConfigOption {
	return func(c *Config) {
		c.EventPublisher = NewPrometheusPublisher(c.EventPublisher, reg)
	}
}

// --- threshold crossings counter ---

type prometheusThresholdNotifier struct {
	crossings prometheus.Counter
}

// NewPrometheusThresholdNotifier returns a ThresholdNotifier that increments
// ankylosaur_threshold_crossings_total each time a new IP crosses the risk threshold.
// Wire it up via WithThresholdNotifier on the engine constructor.
func NewPrometheusThresholdNotifier(reg prometheus.Registerer) ThresholdNotifier {
	crossings := prometheus.NewCounter(prometheus.CounterOpts{
		Name: "ankylosaur_threshold_crossings_total",
		Help: "Total number of IPs that crossed the risk score threshold for the first time.",
	})
	reg.MustRegister(crossings)
	return &prometheusThresholdNotifier{crossings: crossings}
}

func (p *prometheusThresholdNotifier) Notify(_ string, _ int) {
	p.crossings.Inc()
}

// --- middleware latency histogram ---

// WithMiddlewareLatency records ankylosaur_middleware_duration_seconds{endpoint}
// for every request. For denied requests this captures only the rate limiter
// overhead; for allowed requests it includes the downstream handler duration.
//
// Buckets are tuned for middleware latency: 0.1ms–100ms.
func WithMiddlewareLatency(reg prometheus.Registerer) ConfigOption {
	return func(c *Config) {
		h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "ankylosaur_middleware_duration_seconds",
			Help:    "Duration of the rate limiter middleware per endpoint.",
			Buckets: []float64{.0001, .00025, .0005, .001, .0025, .005, .01, .025, .05, .1},
		}, []string{"endpoint"})
		reg.MustRegister(h)
		c.latencyObserver = func(endpoint string, seconds float64) {
			h.WithLabelValues(endpoint).Observe(seconds)
		}
	}
}
