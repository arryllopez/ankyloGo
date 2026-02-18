package ankylogo

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// gather pulls a single metric family from a custom registry by name.
func gatherMetric(t *testing.T, reg *prometheus.Registry, name string) []*dto.Metric {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather error: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() == name {
			return mf.GetMetric()
		}
	}
	return nil
}

// counterFor returns the value of a counter metric matching the given label set.
func counterFor(metrics []*dto.Metric, labels map[string]string) float64 {
	for _, m := range metrics {
		got := map[string]string{}
		for _, lp := range m.GetLabel() {
			got[lp.GetName()] = lp.GetValue()
		}
		match := true
		for k, v := range labels {
			if got[k] != v {
				match = false
				break
			}
		}
		if match {
			return m.GetCounter().GetValue()
		}
	}
	return 0
}

/*
Test that WithPrometheusMetrics records the correct counts for allowed and
denied requests via ankylosaur_requests_total{action, endpoint}.
*/
func TestPrometheusRequestsCounter(t *testing.T) {
	reg := prometheus.NewRegistry()
	config := NewConfig(
		WithSlidingWindow(60, 2),
		WithPrometheusMetrics(reg),
	)

	router := setupTestRouter(config)

	// 2 requests within the limit — both allowed
	makeRequest(router)
	makeRequest(router)

	// 3rd request — denied by sliding window
	makeRequest(router)

	metrics := gatherMetric(t, reg, "ankylosaur_requests_total")
	if metrics == nil {
		t.Fatal("ankylosaur_requests_total not found in registry")
	}

	allowed := counterFor(metrics, map[string]string{"action": "ALLOWED", "endpoint": "GET /ping"})
	denied := counterFor(metrics, map[string]string{"action": "DENIED_WINDOW", "endpoint": "GET /ping"})

	if allowed != 2 {
		t.Errorf("expected 2 ALLOWED, got %.0f", allowed)
	}
	if denied != 1 {
		t.Errorf("expected 1 DENIED_WINDOW, got %.0f", denied)
	}
}

/*
Test that NewPrometheusThresholdNotifier increments
ankylosaur_threshold_crossings_total on each Notify call.
*/
func TestPrometheusThresholdCrossings(t *testing.T) {
	reg := prometheus.NewRegistry()
	notifier := NewPrometheusThresholdNotifier(reg)

	notifier.Notify("1.2.3.4", 5)
	notifier.Notify("5.6.7.8", 3)

	metrics := gatherMetric(t, reg, "ankylosaur_threshold_crossings_total")
	if metrics == nil || len(metrics) == 0 {
		t.Fatal("ankylosaur_threshold_crossings_total not found in registry")
	}

	count := metrics[0].GetCounter().GetValue()
	if count != 2 {
		t.Errorf("expected 2 crossings, got %.0f", count)
	}
}

/*
Test that WithMiddlewareLatency records at least one observation in
ankylosaur_middleware_duration_seconds after a request.
*/
func TestPrometheusMiddlewareLatency(t *testing.T) {
	reg := prometheus.NewRegistry()

	gin_config := NewConfig(
		WithSlidingWindow(60, 100),
		WithMiddlewareLatency(reg),
	)

	router := setupTestRouter(gin_config)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/ping", nil)
	router.ServeHTTP(w, req)

	metrics := gatherMetric(t, reg, "ankylosaur_middleware_duration_seconds")
	if metrics == nil || len(metrics) == 0 {
		t.Fatal("ankylosaur_middleware_duration_seconds not found in registry")
	}

	sampleCount := metrics[0].GetHistogram().GetSampleCount()
	if sampleCount == 0 {
		t.Error("expected at least 1 latency observation, got 0")
	}
}
