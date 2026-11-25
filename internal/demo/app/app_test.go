package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/sources"
)

func do(h http.Handler, path string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func metrics(h http.Handler) string { return do(h, "/metrics", nil).Body.String() }

func TestWorkAndFailHeader(t *testing.T) {
	h := New().Handler()
	if c := do(h, "/api/pay?token=tok_SECRET123", nil).Code; c != 200 {
		t.Errorf("normal request = %d", c)
	}
	if c := do(h, "/api/pay", map[string]string{"X-Demo-Fail": "1"}).Code; c != 500 {
		t.Errorf("failing request = %d", c)
	}
	if c := do(h, "/api/pay", map[string]string{"X-Demo-Fail": "0"}).Code; c != 200 {
		t.Errorf("X-Demo-Fail: 0 = %d, want 200", c)
	}
}

func TestMetricsCounts(t *testing.T) {
	h := New().Handler()
	do(h, "/api/pay?token=tok_SECRET123", nil)
	do(h, "/api/pay", nil)
	do(h, "/api/pay", map[string]string{"X-Demo-Fail": "1"})
	m := metrics(h)
	for _, want := range []string{
		`http_requests_total{handler="/api/pay",code="200"} 2`,
		`http_requests_total{handler="/api/pay",code="500"} 1`,
		`http_request_duration_seconds_bucket{handler="/api/pay",code="200",le="0.3"} 2`,
		`http_request_duration_seconds_bucket{handler="/api/pay",code="500",le="0.3"} 1`,
		`http_request_duration_seconds_bucket{handler="/api/pay",code="200",le="+Inf"} 2`,
		`http_request_duration_seconds_count{handler="/api/pay",code="200"} 2`,
	} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics do not contain %q:\n%s", want, m)
		}
	}
	if strings.Contains(m, "tok_SECRET123") {
		t.Error("the query string must never reach the metrics")
	}
}

func TestHealthzAndMetricsAreNotCounted(t *testing.T) {
	h := New().Handler()
	do(h, "/healthz", nil)
	do(h, "/metrics", nil)
	if m := metrics(h); strings.Contains(m, "handler=") {
		t.Errorf("no work was done, yet metrics has series:\n%s", m)
	}
}

func TestDelayHeaderShowsUpInTheHistogram(t *testing.T) {
	h := New().Handler()
	start := time.Now()
	do(h, "/api/search", map[string]string{"X-Demo-Delay-Ms": "120"})
	if time.Since(start) < 120*time.Millisecond {
		t.Error("the request did not wait")
	}
	m := metrics(h)
	for _, want := range []string{`le="0.1"} 0`, `le="0.3"} 1`, `le="1"} 1`} {
		if !strings.Contains(m, want) {
			t.Errorf("metrics do not contain %q:\n%s", want, m)
		}
	}
}

func TestDelayFrom(t *testing.T) {
	cases := map[string]time.Duration{"": 0, "abc": 0, "-5": 0, "0": 0, " 250 ": 250 * time.Millisecond, "999999": 5 * time.Second}
	for in, want := range cases {
		if got := delayFrom(in); got != want {
			t.Errorf("delayFrom(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestMetricsAreReadableByTheScrapeParser(t *testing.T) {
	h := New().Handler()
	do(h, "/api/pay", nil)
	do(h, "/api/pay", map[string]string{"X-Demo-Fail": "1"})
	scrapes, st, err := sources.ParseScrapes(strings.NewReader("# ebg-scrape ts=2026-11-25T10:00:00Z\n" + metrics(h)))
	if err != nil {
		t.Fatal(err)
	}
	// 2 series x (1 counter + 4 buckets + Inf + count) = 14 kept samples; _sum is not read.
	if st.Malformed != 0 || len(scrapes) != 1 || len(scrapes[0].Samples) != 14 {
		t.Fatalf("malformed=%d scrapes=%d samples=%d, want 0, 1, 14", st.Malformed, len(scrapes), len(scrapes[0].Samples))
	}
	found := false
	for _, s := range scrapes[0].Samples {
		if s.Metric == sources.MetricRequests && s.Handler == "/api/pay" && s.Code == "500" && s.Value == 1 {
			found = true
		}
	}
	if !found {
		t.Error("the 500 counter of /api/pay was not parsed")
	}
}

func TestMetricsOutputIsDeterministic(t *testing.T) {
	h := New().Handler()
	for _, p := range []string{"/b", "/a", "/c"} {
		do(h, p, nil)
	}
	if metrics(h) != metrics(h) {
		t.Error("two reads of the same state differ")
	}
}
