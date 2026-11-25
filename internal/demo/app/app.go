// Package app is the tiny web service of the demo. It does no real work. A request header tells it
// to fail or to be slow, so the load generator decides exactly which requests are bad.
// It also exposes Prometheus text metrics, the second data source of the demo.
package app

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxDelayMS caps the delay a request may ask for.
const MaxDelayMS = 5000

var buckets = []float64{0.05, 0.1, 0.3, 1}

type key struct{ handler, code string }

type stat struct {
	count uint64
	sum   float64
	le    []uint64 // le[i] = observations <= buckets[i]
}

// App holds the metrics.
type App struct {
	mu     sync.Mutex
	series map[key]*stat
}

// New makes an App.
func New() *App { return &App{series: map[key]*stat{}} }

// Handler serves /healthz, /metrics and every other path as "work".
func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/metrics", a.serveMetrics)
	mux.HandleFunc("/", a.serveWork)
	return mux
}

func delayFrom(v string) time.Duration {
	ms, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || ms <= 0 {
		return 0
	}
	if ms > MaxDelayMS {
		ms = MaxDelayMS
	}
	return time.Duration(ms) * time.Millisecond
}

func (a *App) serveWork(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	if d := delayFrom(r.Header.Get("X-Demo-Delay-Ms")); d > 0 {
		time.Sleep(d)
	}
	code := http.StatusOK
	if r.Header.Get("X-Demo-Fail") == "1" {
		code = http.StatusInternalServerError
	}
	a.observe(r.URL.Path, code, time.Since(start))
	w.WriteHeader(code)
	fmt.Fprintln(w, http.StatusText(code))
}

func (a *App) observe(handler string, code int, d time.Duration) {
	k := key{handler, strconv.Itoa(code)}
	secs := d.Seconds()
	a.mu.Lock()
	defer a.mu.Unlock()
	st := a.series[k]
	if st == nil {
		st = &stat{le: make([]uint64, len(buckets))}
		a.series[k] = st
	}
	st.count++
	st.sum += secs
	for i, b := range buckets {
		if secs <= b {
			st.le[i]++
		}
	}
}

func (a *App) serveMetrics(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	keys := make([]key, 0, len(a.series))
	snap := map[key]stat{}
	for k, st := range a.series {
		keys = append(keys, k)
		snap[k] = stat{count: st.count, sum: st.sum, le: append([]uint64(nil), st.le...)}
	}
	a.mu.Unlock()
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].handler != keys[j].handler {
			return keys[i].handler < keys[j].handler
		}
		return keys[i].code < keys[j].code
	})
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	fmt.Fprintln(w, "# HELP http_requests_total Requests handled by the demo app.")
	fmt.Fprintln(w, "# TYPE http_requests_total counter")
	for _, k := range keys {
		fmt.Fprintf(w, "http_requests_total{handler=%q,code=%q} %d\n", k.handler, k.code, snap[k].count)
	}
	fmt.Fprintln(w, "# HELP http_request_duration_seconds Request duration of the demo app.")
	fmt.Fprintln(w, "# TYPE http_request_duration_seconds histogram")
	for _, k := range keys {
		st := snap[k]
		for i, b := range buckets {
			fmt.Fprintf(w, "http_request_duration_seconds_bucket{handler=%q,code=%q,le=%q} %d\n",
				k.handler, k.code, strconv.FormatFloat(b, 'g', -1, 64), st.le[i])
		}
		fmt.Fprintf(w, "http_request_duration_seconds_bucket{handler=%q,code=%q,le=\"+Inf\"} %d\n", k.handler, k.code, st.count)
		fmt.Fprintf(w, "http_request_duration_seconds_sum{handler=%q,code=%q} %.6f\n", k.handler, k.code, st.sum)
		fmt.Fprintf(w, "http_request_duration_seconds_count{handler=%q,code=%q} %d\n", k.handler, k.code, st.count)
	}
}
