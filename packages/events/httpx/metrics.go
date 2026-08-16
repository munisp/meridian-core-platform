package httpx

// metrics.go — Prometheus /metrics instrumentation for every Go service.
//
// Stdlib-only, promhttp-compatible text exposition (client_golang would add
// a third-party dep + go.sum churn, which the assurance branch policy
// forbids). Exposes:
//
//	meridian_build_info{service,version}                      gauge, always 1
//	meridian_http_requests_total{service,route,method,status} counter
//	meridian_http_request_duration_seconds{service,route}     histogram
//
// Kept OFF the public route table: the handler is served on a separate
// listener started by StartMetricsServer (METRICS_PORT env); when
// METRICS_PORT is unset no listener is started and /metrics does not exist
// on the service port.

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DurationBuckets are the histogram buckets (seconds), prometheus client
// defaults minus the extremes.
var DurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type metricKey struct {
	route, method, status string
}

type reqSeries struct {
	count    int64
	sumSec   float64
	bucketCt []int64 // len(DurationBuckets)+1 (+Inf last)
}

// MetricsRegistry is the per-process registry. The zero value is unusable;
// obtain one via DefaultRegistry.
type MetricsRegistry struct {
	mu      sync.Mutex
	service string
	version string
	series  map[metricKey]*reqSeries
}

// DefaultRegistry is the process-wide registry; InitMetrics sets its
// service/version identity (idempotent, first call wins on identity).
var DefaultRegistry = &MetricsRegistry{series: map[metricKey]*reqSeries{}}

// InitMetrics labels the default registry and emits the build_info gauge.
func InitMetrics(service, version string) {
	DefaultRegistry.mu.Lock()
	DefaultRegistry.service = service
	DefaultRegistry.version = version
	DefaultRegistry.mu.Unlock()
}

func (m *MetricsRegistry) observe(route, method string, status int, dur time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.series == nil {
		m.series = map[metricKey]*reqSeries{}
	}
	k := metricKey{route: route, method: method, status: strconv.Itoa(status)}
	s, ok := m.series[k]
	if !ok {
		s = &reqSeries{bucketCt: make([]int64, len(DurationBuckets)+1)}
		m.series[k] = s
	}
	s.count++
	s.sumSec += dur.Seconds()
	d := dur.Seconds()
	for i, b := range DurationBuckets {
		if d <= b {
			s.bucketCt[i]++
		}
	}
	s.bucketCt[len(DurationBuckets)]++ // +Inf
}

// statusRecorder captures the response status (default 200 for handlers
// that never call WriteHeader).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Instrument wraps a handler (typically the service mux, inside the auth
// middleware) and records request count + duration per route. The route
// label is the Go 1.22 ServeMux pattern (r.Pattern) — low cardinality by
// construction; unrouted requests land on route="unmatched".
func Instrument(next http.Handler) http.Handler {
	return InstrumentRegistry(DefaultRegistry, next)
}

// InstrumentRegistry is Instrument against an explicit registry (tests).
func InstrumentRegistry(m *MetricsRegistry, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sr := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sr, r)
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		m.observe(route, r.Method, sr.status, time.Since(start))
	})
}

// Handler exposes the registry in Prometheus text exposition format
// (content type text/plain; version=0.0.4, like promhttp).
func (m *MetricsRegistry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(m.Render()))
	})
}

// esc escapes a label value per the exposition format.
func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// Render produces the text exposition document (deterministic order).
func (m *MetricsRegistry) Render() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "# HELP meridian_build_info Build and service identity (always 1).\n")
	fmt.Fprintf(&b, "# TYPE meridian_build_info gauge\n")
	fmt.Fprintf(&b, "meridian_build_info{service=%q,version=%q} 1\n", esc(m.service), esc(m.version))
	fmt.Fprintf(&b, "# HELP meridian_http_requests_total HTTP requests by route/method/status.\n")
	fmt.Fprintf(&b, "# TYPE meridian_http_requests_total counter\n")
	keys := make([]metricKey, 0, len(m.series))
	for k := range m.series {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].route != keys[j].route {
			return keys[i].route < keys[j].route
		}
		if keys[i].method != keys[j].method {
			return keys[i].method < keys[j].method
		}
		return keys[i].status < keys[j].status
	})
	for _, k := range keys {
		s := m.series[k]
		fmt.Fprintf(&b, "meridian_http_requests_total{service=%q,route=%q,method=%q,status=%q} %d\n",
			esc(m.service), esc(k.route), esc(k.method), esc(k.status), s.count)
	}
	fmt.Fprintf(&b, "# HELP meridian_http_request_duration_seconds HTTP request duration by route.\n")
	fmt.Fprintf(&b, "# TYPE meridian_http_request_duration_seconds histogram\n")
	for _, k := range keys {
		s := m.series[k]
		// bucketCt is already cumulative (observe increments every le-bound
		// >= d, matching the Prometheus histogram convention).
		for i, bnd := range DurationBuckets {
			fmt.Fprintf(&b, "meridian_http_request_duration_seconds_bucket{service=%q,route=%q,le=%q} %d\n",
				esc(m.service), esc(k.route), strconv.FormatFloat(bnd, 'g', -1, 64), s.bucketCt[i])
		}
		fmt.Fprintf(&b, "meridian_http_request_duration_seconds_bucket{service=%q,route=%q,le=\"+Inf\"} %d\n",
			esc(m.service), esc(k.route), s.bucketCt[len(DurationBuckets)])
		fmt.Fprintf(&b, "meridian_http_request_duration_seconds_sum{service=%q,route=%q} %s\n",
			esc(m.service), esc(k.route), strconv.FormatFloat(s.sumSec, 'g', -1, 64))
		fmt.Fprintf(&b, "meridian_http_request_duration_seconds_count{service=%q,route=%q} %d\n",
			esc(m.service), esc(k.route), s.count)
	}
	return b.String()
}

// MetricsHandler is the default registry's handler.
func MetricsHandler() http.Handler { return DefaultRegistry.Handler() }

// StartMetricsServer starts the dedicated metrics listener on METRICS_PORT
// and returns immediately. When METRICS_PORT is unset it is a no-op
// (metrics stay off the public route table). The listener serves ONLY
// GET /metrics. Call once from main after InitMetrics.
func StartMetricsServer() {
	port := os.Getenv("METRICS_PORT")
	if port == "" {
		return
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", MetricsHandler())
	go func() {
		if err := Serve(NewServer(":"+port, mux)); err != nil {
			log.Printf("metrics listener on :%s stopped: %v", port, err)
		}
	}()
}
