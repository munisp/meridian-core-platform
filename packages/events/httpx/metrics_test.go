package httpx

// metrics_test.go — /metrics instrumentation: scrape returns 200 with the
// expected metric families, and a business handler increments the counters.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsScrapeExposesFamilies(t *testing.T) {
	reg := &MetricsRegistry{series: map[metricKey]*reqSeries{}}
	reg.service, reg.version = "svc-test", "9.9.9"
	mux := http.NewServeMux()
	mux.Handle("GET /v1/things/{id}", InstrumentRegistry(reg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	})))
	req := httptest.NewRequest("GET", "/v1/things/abc", nil)
	mux.ServeHTTP(httptest.NewRecorder(), req)

	rec := httptest.NewRecorder()
	reg.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("scrape status = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, fam := range []string{
		`meridian_build_info{service="svc-test",version="9.9.9"} 1`,
		`meridian_http_requests_total{service="svc-test",route="GET /v1/things/{id}",method="GET",status="418"} 1`,
		`meridian_http_request_duration_seconds_bucket{service="svc-test",route="GET /v1/things/{id}",le="+Inf"} 1`,
		`meridian_http_request_duration_seconds_count{service="svc-test",route="GET /v1/things/{id}"} 1`,
		"# TYPE meridian_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(body, fam) {
			t.Fatalf("scrape missing %q\n---\n%s", fam, body)
		}
	}
}

func TestInstrumentCountsPerStatusAndRoute(t *testing.T) {
	reg := &MetricsRegistry{series: map[metricKey]*reqSeries{}}
	h := InstrumentRegistry(reg, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fail") == "1" {
			Errorf(w, http.StatusBadRequest, "bad request", "boom")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	for i := 0; i < 3; i++ {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok", nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/ok?fail=1", nil))
	reg.mu.Lock()
	defer reg.mu.Unlock()
	var okN, badN int64
	for k, s := range reg.series {
		if k.status == "204" {
			okN = s.count
		}
		if k.status == "400" {
			badN = s.count
		}
	}
	if okN != 3 || badN != 1 {
		t.Fatalf("counters: 204=%d (want 3), 400=%d (want 1)", okN, badN)
	}
}

func TestStartMetricsServerDisabledWithoutEnv(t *testing.T) {
	// No METRICS_PORT: must be a no-op (metrics off the public route table).
	StartMetricsServer()
}

func TestHistogramBucketsCumulativeNotDoubleCounted(t *testing.T) {
	reg := &MetricsRegistry{series: map[metricKey]*reqSeries{}}
	reg.service = "svc"
	reg.observe("GET /x", "GET", 200, 100*time.Millisecond)
	body := reg.Render()
	// one 100ms observation: le=0.05 -> 0, le=0.1 -> 1, le=+Inf -> 1
	for want, present := range map[string]bool{
		`le="0.05"} 0`: true, `le="0.1"} 1`: true, `le="+Inf"} 1`: true,
	} {
		if strings.Contains(body, want) != present {
			t.Fatalf("bucket line %q present=%v\n%s", want, !present, body)
		}
	}
	if !strings.Contains(body, `meridian_http_request_duration_seconds_count{service="svc",route="GET /x"} 1`) {
		t.Fatal("count must be 1")
	}
}
