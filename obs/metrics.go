package obs

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/Syluxso/byz-platform/httpx"
)

var (
	metricsOnce     sync.Once
	requestDuration *prometheus.HistogramVec
	authRejects     *prometheus.CounterVec
)

func initMetrics() {
	metricsOnce.Do(func() {
		requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "byz_http_request_duration_seconds",
			Help:    "HTTP request latency by service, method, route pattern and status.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"service", "method", "route", "status"})
		authRejects = prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "byz_auth_rejects_total",
			Help: "Bearer tokens rejected by iam.FromRequest, by reason.",
		}, []string{"service", "reason"})
		prometheus.MustRegister(requestDuration, authRejects)
	})
}

// Metrics serves the Prometheus text exposition for the default registry.
// Mount at /metrics. Go runtime and process collectors are included.
func Metrics() http.Handler {
	initMetrics()
	return promhttp.Handler()
}

// AuthRejected returns the hook to pass as iam.Options.OnReject. Rejected
// tokens are counted here and never reported as faults.
func AuthRejected(service string) func(r *http.Request, reason string) {
	initMetrics()
	return func(_ *http.Request, reason string) {
		authRejects.WithLabelValues(service, reason).Inc()
	}
}

// HTTPMetrics records a latency histogram per request. The route label is the
// ServeMux pattern that matched (Go 1.22+), so cardinality stays bounded even
// with ids in paths. Unmatched requests are labelled "unmatched".
func HTTPMetrics(service string) httpx.Middleware {
	initMetrics()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			route := r.Pattern
			if route == "" {
				route = "unmatched"
			}
			requestDuration.WithLabelValues(service, r.Method, route, strconv.Itoa(sw.status())).
				Observe(time.Since(start).Seconds())
		})
	}
}

// RequestLogger writes one line per request with method, route, status,
// duration, request_id, org and tenant. Skips health and metrics paths.
func RequestLogger(logger *slog.Logger) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/healthz", "/readyz", "/actuator/health", "/metrics":
				next.ServeHTTP(w, r)
				return
			}
			start := time.Now()
			sw := &statusWriter{ResponseWriter: w}
			next.ServeHTTP(sw, r)
			route := r.Pattern
			if route == "" {
				route = r.URL.Path
			}
			With(r.Context(), logger).Info("http",
				"method", r.Method,
				"route", route,
				"path", r.URL.Path,
				"status", sw.status(),
				"duration_ms", time.Since(start).Milliseconds(),
			)
		})
	}
}

// statusWriter captures the status code while staying transparent to
// streaming (Flush) and hijacking so SSE and websockets keep working.
type statusWriter struct {
	http.ResponseWriter
	code    int
	written bool
}

func (s *statusWriter) status() int {
	if !s.written {
		return http.StatusOK
	}
	return s.code
}

func (s *statusWriter) WriteHeader(code int) {
	if !s.written {
		s.code = code
		s.written = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if !s.written {
		s.code = http.StatusOK
		s.written = true
	}
	return s.ResponseWriter.Write(b)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		if !s.written {
			s.code = http.StatusOK
			s.written = true
		}
		f.Flush()
	}
}

func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("obs: underlying ResponseWriter does not support hijacking")
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (s *statusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
