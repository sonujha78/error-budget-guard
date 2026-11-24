package sources

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/budget"
	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

const nginxLine = `203.0.113.7 - alice [23/Nov/2026:10:00:01 +0000] "GET /pay?token=tok_SECRET123 HTTP/1.1" 200 512 "https://shop.example/cart?sid=SESS999" "Mozilla/5.0 (X11; Linux x86_64)" rt=0.120`

var forbidden = []string{"203.0.113", "198.51.100", "172.17.0.1", "tok_SECRET123", "user-4242", "SESS999", "alice", "bob", "4242"}

func shippedSLOs(t *testing.T) []slo.SLO {
	t.Helper()
	f, err := slo.Load("../../configs/slos.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return f.SLOs
}

func openSample(t *testing.T, name string) *os.File {
	t.Helper()
	fh, err := os.Open("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fh.Close() })
	return fh
}

func sums(evs []event.Event, service, sli string) budget.Counts {
	return budget.Sum(evs, service, sli,
		time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC))
}

func noSecrets(t *testing.T, what, s string) {
	t.Helper()
	for _, f := range forbidden {
		if strings.Contains(s, f) {
			t.Errorf("%s leaks %q", what, f)
		}
	}
}

func TestNormalizePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/pay?token=tok_SECRET123", "/pay"},
		{"/pay#frag", "/pay"},
		{"/orders/98765/items", "/orders/:id/items"},
		{"/users/550e8400-e29b-41d4-a716-446655440000", "/users/:id"},
		{"/files/0123456789abcdef0123", "/files/:id"},
		{"/pay;jsessionid=ABC123", "/pay"},
		{"/v2/cart", "/v2/cart"},
		{"/search/", "/search/"},
		{"/", "/"},
		{"/" + strings.Repeat("a", 100), "/:id"},
		{"", "(invalid)"},
		{"pay", "(invalid)"},
		{"-", "(invalid)"},
	}
	for _, c := range cases {
		if got := NormalizePath(c.in); got != c.want {
			t.Errorf("NormalizePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got := NormalizePath(strings.Repeat("/ab", 50)); len(got) > 80 {
		t.Errorf("path not truncated, len=%d", len(got))
	}
}

func TestParseNginx(t *testing.T) {
	r, err := ParseNginx(nginxLine, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Time.Equal(time.Date(2026, 11, 23, 10, 0, 1, 0, time.UTC)) {
		t.Errorf("time = %v", r.Time)
	}
	if r.Service != "checkout" || r.Path != "/pay" || r.Status != 200 || !r.HasLatency || r.LatencyMS != 120 {
		t.Errorf("unexpected request: %+v", r)
	}
}

func TestParseNginxTimezoneAndBoundary(t *testing.T) {
	line := `203.0.113.7 - - [23/Nov/2026:15:30:01 +0530] "GET /cart HTTP/1.1" 200 1 "-" "curl/8.5.0" rt=0.300`
	r, err := ParseNginx(line, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Time.Equal(time.Date(2026, 11, 23, 10, 0, 1, 0, time.UTC)) || r.Time.Location() != time.UTC {
		t.Errorf("time = %v, want 10:00:01 UTC", r.Time)
	}
	if r.LatencyMS != 300 {
		t.Errorf("latency = %v, want exactly 300", r.LatencyMS)
	}
}

func TestParseNginxDefaultDockerFormatHasNoLatency(t *testing.T) {
	line := `172.17.0.1 - - [24/Nov/2026:06:12:01 +0000] "GET /?token=tok_SECRET123 HTTP/1.1" 200 615 "-" "curl/8.5.0" "-"`
	r, err := ParseNginx(line, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if r.HasLatency || r.Path != "/" || r.Status != 200 {
		t.Errorf("unexpected request: %+v", r)
	}
}

func TestParseNginxBadRequestLine(t *testing.T) {
	line := `172.17.0.1 - - [24/Nov/2026:06:12:01 +0000] "-" 400 0 "-" "-" rt=0.000`
	r, err := ParseNginx(line, "checkout")
	if err != nil {
		t.Fatal(err)
	}
	if r.Path != "(invalid)" || r.Status != 400 {
		t.Errorf("unexpected request: %+v", r)
	}
}

func TestParseNginxGarbageErrorHasNoContent(t *testing.T) {
	_, err := ParseNginx("garbage from 203.0.113.7 tok_SECRET123", "checkout")
	if err == nil {
		t.Fatal("expected error")
	}
	noSecrets(t, "error text", err.Error())
}

func TestParseJSON(t *testing.T) {
	line := `{"ts":"2026-11-23T10:00:01Z","service":"checkout","method":"GET","path":"/pay?token=tok_SECRET123","status":200,"latency_ms":120,"request_id":"r-1","client_ip":"203.0.113.7","user_id":"user-4242"}`
	r, err := ParseJSON(line, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Service != "checkout" || r.Path != "/pay" || r.Status != 200 || !r.HasLatency || r.LatencyMS != 120 {
		t.Errorf("unexpected request: %+v", r)
	}
	if r.DedupKey != "id:checkout:r-1" {
		t.Errorf("dedup key = %q", r.DedupKey)
	}
}

func TestParseJSONUnixTimestamp(t *testing.T) {
	r, err := ParseJSON(`{"ts":1795428002,"service":"checkout","path":"/cart","status":503}`, "")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Time.Equal(time.Date(2026, 11, 23, 10, 0, 2, 0, time.UTC)) {
		t.Errorf("time = %v", r.Time)
	}
	r, err = ParseJSON(`{"ts":1795428002.5,"service":"checkout","path":"/cart","status":503}`, "")
	if err != nil || r.Time.Nanosecond() != 500000000 {
		t.Errorf("fractional ts: err=%v time=%v", err, r.Time)
	}
}

func TestParseJSONDefaultService(t *testing.T) {
	line := `{"ts":"2026-11-23T10:00:03Z","path":"/cart","status":200}`
	r, err := ParseJSON(line, "checkout")
	if err != nil || r.Service != "checkout" || r.HasLatency {
		t.Errorf("r=%+v err=%v", r, err)
	}
}

func TestParseJSONErrors(t *testing.T) {
	cases := []struct{ name, line, want string }{
		{"not json", `not json 203.0.113.7`, "invalid json"},
		{"no status", `{"ts":"2026-11-23T10:00:01Z","service":"checkout","path":"/a"}`, "status is required"},
		{"status string", `{"ts":"2026-11-23T10:00:01Z","service":"checkout","path":"/a","status":"oops"}`, "invalid json"},
		{"status range", `{"ts":"2026-11-23T10:00:01Z","service":"checkout","path":"/a","status":99}`, "status out of range"},
		{"no ts", `{"service":"checkout","path":"/a","status":200}`, "ts is required"},
		{"ts in milliseconds", `{"ts":1795428002000,"service":"checkout","path":"/a","status":200}`, "ts out of range"},
		{"ts bad string", `{"ts":"yesterday","service":"checkout","path":"/a","status":200}`, "RFC3339"},
		{"negative latency", `{"ts":"2026-11-23T10:00:01Z","service":"checkout","path":"/a","status":200,"latency_ms":-5}`, "latency_ms"},
		{"no service", `{"ts":"2026-11-23T10:00:01Z","path":"/a","status":200}`, "service is required"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseJSON(c.line, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err=%v, want substring %q", err, c.want)
			}
			noSecrets(t, "error text", err.Error())
		})
	}
}

func TestParseProbe(t *testing.T) {
	head := `{"ts":"2026-11-23T10:00:20Z","service":"checkout","target":"/healthz",`
	cases := []struct {
		name    string
		line    string
		status  int
		latency bool
		ms      float64
	}{
		{"ok without status means 200", head + `"ok":true}`, 200, false, 0},
		{"failure without status", head + `"ok":false,"error":"connection refused"}`, 0, false, 0},
		{"failure with status", head + `"ok":false,"status":503,"latency_ms":900}`, 503, true, 900},
		{"status wins over ok flag", head + `"ok":true,"status":500,"latency_ms":20}`, 500, true, 20},
		{"status zero means no response", head + `"ok":false,"status":0}`, 0, false, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, err := ParseProbe(c.line, "")
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != c.status || r.HasLatency != c.latency || r.LatencyMS != c.ms || r.Path != "/healthz" {
				t.Errorf("unexpected request: %+v", r)
			}
		})
	}
	if _, err := ParseProbe(head+`"x":1}`, ""); err == nil || !strings.Contains(err.Error(), "ok is required") {
		t.Errorf("missing ok: err=%v", err)
	}
	if _, err := ParseProbe(`{"ts":"2026-11-23T10:00:20Z","service":"checkout","ok":true}`, ""); err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Errorf("missing target: err=%v", err)
	}
}

func TestRequestEvents(t *testing.T) {
	slos := shippedSLOs(t)
	at := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	mk := func(service string, status int, ms float64, has bool) Request {
		return Request{Time: at, Service: service, Path: "/pay", Status: status, LatencyMS: ms, HasLatency: has}
	}
	type want struct {
		sli         string
		good, valid int64
	}
	avail, lat := event.SLIAvailability, event.SLILatency
	cases := []struct {
		name  string
		req   Request
		want  []want
		noLat bool
	}{
		{"fast ok", mk("checkout", 200, 120, true), []want{{avail, 1, 1}, {lat, 1, 1}}, false},
		{"slow ok", mk("checkout", 200, 500, true), []want{{avail, 1, 1}, {lat, 0, 1}}, false},
		{"300ms boundary is good", mk("checkout", 200, 300, true), []want{{avail, 1, 1}, {lat, 1, 1}}, false},
		{"server error", mk("checkout", 502, 10, true), []want{{avail, 0, 1}, {lat, 0, 1}}, false},
		{"client error is ignored", mk("checkout", 404, 10, true), nil, false},
		{"ok but latency unknown", mk("checkout", 200, 0, false), []want{{avail, 1, 1}}, true},
		{"failure with unknown latency is still bad", mk("checkout", 502, 0, false), []want{{avail, 0, 1}, {lat, 0, 1}}, false},
		{"service without latency slo", mk("search", 200, 5000, true), []want{{avail, 1, 1}}, false},
		{"unknown service", mk("nobody", 200, 10, true), nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			evs, noLat := RequestEvents(c.req, "test-src", slos)
			if noLat != c.noLat {
				t.Errorf("noLatency = %v, want %v", noLat, c.noLat)
			}
			if len(evs) != len(c.want) {
				t.Fatalf("got %d events, want %d: %+v", len(evs), len(c.want), evs)
			}
			for i, w := range c.want {
				e := evs[i]
				if e.SLI != w.sli || e.Good != w.good || e.Valid != w.valid || e.Source != "test-src" {
					t.Errorf("event %d = %+v, want %+v", i, e, w)
				}
				if wantLabel := event.MakeLabel("/pay", slo.StatusClass(c.req.Status)); e.Label != wantLabel {
					t.Errorf("label = %q, want %q", e.Label, wantLabel)
				}
				if err := e.Validate(); err != nil {
					t.Errorf("invalid event: %v", err)
				}
			}
		})
	}
}

func TestCollectNginxSample(t *testing.T) {
	res, err := Collect(openSample(t, "nginx_sample.log"), FormatNginx, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.Lines != 7 || st.Blank != 0 || st.Malformed != 1 || st.Requests != 6 || st.Events != 10 || st.NoLatency != 0 {
		t.Errorf("stats = %+v", st)
	}
	if len(st.Errors) != 1 || !strings.HasPrefix(st.Errors[0], "line 6:") {
		t.Errorf("errors = %v", st.Errors)
	}
	if a := sums(res.Events, "checkout", event.SLIAvailability); a.Good != 4 || a.Valid != 5 {
		t.Errorf("availability counts = %+v, want good=4 valid=5", a)
	}
	if l := sums(res.Events, "checkout", event.SLILatency); l.Good != 3 || l.Valid != 5 {
		t.Errorf("latency counts = %+v, want good=3 valid=5", l)
	}
	for _, e := range res.Events {
		if e.Source != "nginx-log" || e.Validate() != nil {
			t.Errorf("bad event %+v", e)
		}
	}
}

func TestCollectJSONSample(t *testing.T) {
	res, err := Collect(openSample(t, "json_sample.log"), FormatJSON, "checkout", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.Lines != 5 || st.Malformed != 1 || st.Duplicates != 1 || st.Requests != 3 || st.Events != 5 || st.NoLatency != 1 {
		t.Errorf("stats = %+v", st)
	}
	if a := sums(res.Events, "checkout", event.SLIAvailability); a.Good != 2 || a.Valid != 3 {
		t.Errorf("availability counts = %+v, want good=2 valid=3", a)
	}
	if l := sums(res.Events, "checkout", event.SLILatency); l.Good != 1 || l.Valid != 2 {
		t.Errorf("latency counts = %+v, want good=1 valid=2", l)
	}
}

func TestCollectProbeSample(t *testing.T) {
	res, err := Collect(openSample(t, "probe_sample.log"), FormatProbe, "", shippedSLOs(t))
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats
	if st.Lines != 4 || st.Malformed != 0 || st.Requests != 4 || st.Events != 7 || st.NoLatency != 1 {
		t.Errorf("stats = %+v", st)
	}
	if a := sums(res.Events, "checkout", event.SLIAvailability); a.Good != 2 || a.Valid != 4 {
		t.Errorf("availability counts = %+v, want good=2 valid=4", a)
	}
	if l := sums(res.Events, "checkout", event.SLILatency); l.Good != 1 || l.Valid != 3 {
		t.Errorf("latency counts = %+v, want good=1 valid=3", l)
	}
	for _, e := range res.Events {
		if e.Source != "probe" {
			t.Errorf("source = %q", e.Source)
		}
	}
}

func TestNoSecretsInOutput(t *testing.T) {
	cases := []struct{ file, format string }{
		{"nginx_sample.log", FormatNginx},
		{"json_sample.log", FormatJSON},
		{"probe_sample.log", FormatProbe},
	}
	for _, c := range cases {
		t.Run(c.file, func(t *testing.T) {
			res, err := Collect(openSample(t, c.file), c.format, "checkout", shippedSLOs(t))
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(res.Events)
			if err != nil {
				t.Fatal(err)
			}
			noSecrets(t, "events", string(b))
			noSecrets(t, "stats errors", strings.Join(res.Stats.Errors, "\n"))
		})
	}
}

func TestTooLongLineIsAnErrorWithoutContent(t *testing.T) {
	_, _, err := ReadRequests(strings.NewReader(strings.Repeat("a", 2<<20)), FormatNginx, "checkout")
	if err == nil {
		t.Fatal("expected error for a 2 MB line")
	}
	if strings.Contains(err.Error(), "aaaa") {
		t.Error("error text contains line content")
	}
}

func TestBadFormatAndMissingService(t *testing.T) {
	if _, _, err := ReadRequests(strings.NewReader(""), "xml", "svc"); err == nil {
		t.Error("unknown format must be an error")
	}
	if _, _, err := ReadRequests(strings.NewReader(""), FormatNginx, ""); err == nil {
		t.Error("nginx without a service name must be an error")
	}
}
