package server

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/preining/parkrr/internal/auth"
)

// Metrics must remain bounded even for extension methods rejected by the limiter.
func TestMetricsMiddlewareBoundsArbitraryMethods(t *testing.T) {
	httpRequests.Reset()
	httpDuration.Reset()
	defer httpRequests.Reset()
	defer httpDuration.Reset()
	reg := prometheus.NewRegistry()
	reg.MustRegister(httpRequests, httpDuration)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMethodNotAllowed)
	})
	stop := make(chan struct{})
	defer close(stop)
	wrapped := metricsMiddleware(mux, rateLimit(&auth.Manager{}, 1, stop, mux))
	throttled := 0
	for i := 0; i < 200; i++ {
		r := httptest.NewRequest(fmt.Sprintf("CUSTOM%d", i), "http://localhost/", nil)
		r.RemoteAddr = "192.0.2.1:12345"
		w := httptest.NewRecorder()
		wrapped.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests {
			throttled++
		}
	}
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 2 || throttled == 0 {
		t.Fatalf("missing metrics or limiter coverage: families=%d throttled=%d", len(families), throttled)
	}
	for _, f := range families {
		t.Logf("%s: %d unique label combinations (%d requests rate-limited)",
			f.GetName(), len(f.Metric), throttled)
		if len(f.Metric) > 2 {
			t.Errorf("untrusted HTTP methods created unbounded metric cardinality: %d", len(f.Metric))
		}
	}
}

func TestRegisterObservabilityMetricsAccess(t *testing.T) {
	const token = "test-metrics-token"
	tests := []struct {
		name           string
		token          string
		requireAuth    bool
		authorization  string
		expectedStatus int
	}{
		{
			name:           "missing token",
			token:          token,
			requireAuth:    true,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "wrong token",
			token:          token,
			requireAuth:    true,
			authorization:  "Bearer wrong-token",
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "valid bearer token",
			token:          token,
			requireAuth:    true,
			authorization:  "Bearer " + token,
			expectedStatus: http.StatusOK,
		},
		{
			name:           "configured token is enforced without require auth",
			token:          token,
			expectedStatus: http.StatusUnauthorized,
		},
		{
			name:           "required authentication without configured token fails closed",
			requireAuth:    true,
			authorization:  "Bearer " + token,
			expectedStatus: http.StatusNotFound,
		},
		{
			name:           "explicitly unauthenticated metrics remain available",
			expectedStatus: http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := http.NewServeMux()
			registerObservability(
				mux,
				newMetricsTestPool(t),
				tt.token,
				tt.requireAuth,
			)
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.Header.Set("Authorization", tt.authorization)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != tt.expectedStatus {
				t.Fatalf("metrics status = %d, want %d", rec.Code, tt.expectedStatus)
			}
			hasMetrics := strings.Contains(rec.Body.String(), "parkrr_db_pool_max_conns 1\n")
			if hasMetrics != (tt.expectedStatus == http.StatusOK) {
				t.Errorf("metrics exposed = %t for status %d", hasMetrics, rec.Code)
			}
			if strings.Contains(rec.Body.String(), token) {
				t.Error("response exposed the metrics token")
			}
			// Disabling metrics must not disable the database-independent health check.
			health := httptest.NewRecorder()
			mux.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			if health.Code != http.StatusOK {
				t.Errorf("health status = %d, want 200", health.Code)
			}
		})
	}
}

func TestRegisterObservabilityMetricsNegotiation(t *testing.T) {
	// The application collectors are shared with the middleware test: keep these tests serial.
	httpRequests.Reset()
	httpDuration.Reset()
	t.Cleanup(httpRequests.Reset)
	t.Cleanup(httpDuration.Reset)
	mux := http.NewServeMux()
	registerObservability(mux, newMetricsTestPool(t), "test-metrics-token", true)
	mux.HandleFunc("GET /example", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	metricsMiddleware(mux, mux).ServeHTTP(
		httptest.NewRecorder(),
		httptest.NewRequest(http.MethodGet, "/example", nil),
	)
	tests := []struct {
		name             string
		accept           string
		acceptEncoding   string
		expectedEncoding string
	}{
		{name: "default plain text"},
		{name: "wildcard plain text", accept: "*/*"},
		{name: "explicit plain text", accept: "text/plain; version=0.0.4"},
		{
			name:             "gzip plain text",
			accept:           "text/plain; version=0.0.4",
			acceptEncoding:   "gzip",
			expectedEncoding: "gzip",
		},
		{
			name:           "refused gzip stays uncompressed",
			acceptEncoding: "gzip;q=0, identity;q=1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
			req.Header.Set("Authorization", "Bearer test-metrics-token")
			req.Header.Set("Accept", tt.accept)
			req.Header.Set("Accept-Encoding", tt.acceptEncoding)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("metrics status = %d, want 200", rec.Code)
			}
			mediaType, params, err := mime.ParseMediaType(rec.Header().Get("Content-Type"))
			if err != nil {
				t.Fatalf("parse content type: %v", err)
			}
			if mediaType != "text/plain" || params["version"] != "0.0.4" {
				t.Errorf("content type = %q, want Prometheus text format 0.0.4", rec.Header().Get("Content-Type"))
			}
			if got := rec.Header().Get("Content-Encoding"); got != tt.expectedEncoding {
				t.Fatalf("content encoding = %q, want %q", got, tt.expectedEncoding)
			}
			for _, name := range []string{"Accept", "Accept-Encoding"} {
				var found bool
				for _, value := range rec.Header().Values("Vary") {
					for field := range strings.SplitSeq(value, ",") {
						found = found || strings.EqualFold(strings.TrimSpace(field), name)
					}
				}
				if !found {
					t.Errorf("Vary %q does not include %s", rec.Header().Values("Vary"), name)
				}
			}
			body := rec.Body.String()
			if tt.expectedEncoding == "gzip" {
				reader, err := gzip.NewReader(rec.Body)
				if err != nil {
					t.Fatalf("open gzip response: %v", err)
				}
				defer reader.Close()
				decoded, err := io.ReadAll(reader)
				if err != nil {
					t.Fatalf("decode gzip response: %v", err)
				}
				body = string(decoded)
			}
			for _, sample := range []string{
				"parkrr_http_requests_total{",
				"parkrr_http_request_duration_seconds_bucket{",
				"parkrr_db_pool_max_conns 1\n",
				"parkrr_db_pool_total_conns 0\n",
				"parkrr_audit_retention_failed ",
				"go_goroutines ",
				"go_info{",
			} {
				if !strings.Contains(body, "\n"+sample) {
					t.Errorf("metrics response is missing sample %q", sample)
				}
			}
			// The Linux process collector underpins the shipped restart-loop alert.
			if runtime.GOOS == "linux" && !strings.Contains(body, "\nprocess_start_time_seconds ") {
				t.Error("metrics response is missing process_start_time_seconds")
			}
		})
	}
}

func newMetricsTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	config, err := pgxpool.ParseConfig("host=127.0.0.1 port=1 user=metrics_test dbname=metrics_test sslmode=disable")
	if err != nil {
		t.Fatalf("parse metrics test pool: %v", err)
	}
	config.MinConns = 0
	config.MinIdleConns = 0
	config.MaxConns = 1
	var dialAttempts atomic.Int64
	// Collectors read pool statistics, never the database; even a regression cannot dial a real server.
	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		dialAttempts.Add(1)
		return nil, errors.New("metrics test: database connections are disabled")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), config)
	if err != nil {
		t.Fatalf("create metrics test pool: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if got := dialAttempts.Load(); got != 0 {
			t.Errorf("metrics tried to connect to the database %d times", got)
		}
	})
	return pool
}
