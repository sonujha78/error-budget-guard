// Package sources reads raw data (access logs, probe results) and turns it into common events.
//
// Data minimisation: parsers only extract time, service, endpoint, status and latency.
// IP addresses, user ids, cookies, referers and user agents are never copied into any struct.
package sources

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

// Input formats.
const (
	FormatNginx = "nginx"
	FormatJSON  = "json"
	FormatProbe = "probe"
)

const (
	invalidPath = "(invalid)"
	maxPathLen  = 80
	maxErrors   = 10
)

// Request is one parsed request or probe, before it is turned into events.
type Request struct {
	Time       time.Time
	Service    string
	Path       string // normalised: no query, no ids
	Status     int    // 0 = no response at all
	LatencyMS  float64
	HasLatency bool
	// DedupKey is an explicit request id from the log (empty when the log has none).
	// Duplicates are dropped on it. Never printed, never copied into an Event.
	DedupKey string
	// LineKey is a hash of the raw line. It is used only with --dedupe-lines, because
	// identical lines can be genuinely different requests (one second timestamps).
	LineKey string
}

var (
	digitsRE = regexp.MustCompile(`^[0-9]+$`)
	uuidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	hexRE    = regexp.MustCompile(`^[0-9a-fA-F]{16,}$`)
)

func isID(seg string) bool {
	if seg == "" {
		return false
	}
	return digitsRE.MatchString(seg) || uuidRE.MatchString(seg) || hexRE.MatchString(seg) || len(seg) >= 32
}

// NormalizePath removes the query, fragment and path parameters and replaces obvious ids
// (numbers, UUIDs, long hex, long tokens) with ":id". Short mixed ids are not detected.
func NormalizePath(raw string) string {
	p := raw
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/") {
		return invalidPath
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if j := strings.IndexByte(s, ';'); j >= 0 {
			s = s[:j]
		}
		if isID(s) {
			s = ":id"
		}
		segs[i] = s
	}
	out := strings.Join(segs, "/")
	if len(out) > maxPathLen {
		out = out[:maxPathLen]
	}
	return out
}

func lineKey(service, line string) string {
	h := sha256.Sum256([]byte(service + "\x00" + line))
	return "line:" + hex.EncodeToString(h[:8])
}

// SourceName is the value of Event.Source for an input format.
func SourceName(format string) string {
	switch format {
	case FormatNginx:
		return "nginx-log"
	case FormatJSON:
		return "json-log"
	case FormatProbe:
		return "probe"
	}
	return format
}

// RequestEvents turns one request into events, one per matching SLO.
//
// Client errors (excluded by the SLO) give no event. If the request already failed, it is bad
// for a latency SLO even without a latency value. If it succeeded but its latency is unknown,
// the latency SLO cannot judge it: no event is made and noLatency is true. No guessing.
func RequestEvents(r Request, source string, slos []slo.SLO) (evs []event.Event, noLatency bool) {
	for _, s := range slos {
		if s.Service != r.Service || !s.IsValid(r.Status) {
			continue
		}
		if s.SLI == event.SLILatency && !r.HasLatency {
			// IsGood with latency 0 is true only if the status class is good.
			if s.IsGood(r.Status, 0) {
				noLatency = true
				continue
			}
		}
		var good int64
		if s.IsGood(r.Status, r.LatencyMS) {
			good = 1
		}
		evs = append(evs, event.Event{
			Timestamp: r.Time,
			Service:   r.Service,
			SLI:       s.SLI,
			Good:      good,
			Valid:     1,
			Source:    source,
			Label:     event.MakeLabel(r.Path, slo.StatusClass(r.Status)),
		})
	}
	return evs, noLatency
}

// ToEvents converts requests to events and counts requests that latency SLOs could not judge.
func ToEvents(reqs []Request, format string, slos []slo.SLO) ([]event.Event, int) {
	src := SourceName(format)
	var evs []event.Event
	noLat := 0
	for _, r := range reqs {
		e, nl := RequestEvents(r, src, slos)
		evs = append(evs, e...)
		if nl {
			noLat++
		}
	}
	return evs, noLat
}

// Stats says how much of the input was used. Errors hold line numbers and reasons only.
type Stats struct {
	Lines      int      `json:"lines"`
	Blank      int      `json:"blank"`
	Malformed  int      `json:"malformed"`
	Requests   int      `json:"requests"`
	Events     int      `json:"events"`
	NoLatency  int      `json:"no_latency"`
	Scrapes    int      `json:"scrapes,omitempty"`
	Resets     int      `json:"resets,omitempty"`
	NewSeries  int      `json:"new_series,omitempty"`
	Duplicates int      `json:"duplicates,omitempty"`
	OutOfOrder int      `json:"out_of_order,omitempty"`
	Clamped    int      `json:"clamped,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	Errors     []string `json:"errors,omitempty"`
}

// Result is everything Collect produces.
type Result struct {
	Events   []event.Event
	Requests []Request
	Times    []time.Time // sample times for coverage (request or scrape times, may repeat)
	Stats    Stats
}

func parserFor(format, defaultService string) (func(string) (Request, error), error) {
	switch format {
	case FormatNginx:
		if defaultService == "" {
			return nil, errors.New("nginx logs have no service field: pass a service name")
		}
		return func(l string) (Request, error) { return ParseNginx(l, defaultService) }, nil
	case FormatJSON:
		return func(l string) (Request, error) { return ParseJSON(l, defaultService) }, nil
	case FormatProbe:
		return func(l string) (Request, error) { return ParseProbe(l, defaultService) }, nil
	}
	return nil, fmt.Errorf("unknown format %q (want nginx, json or probe)", format)
}

// ReadRequests parses every line of r. Bad lines are counted, never fatal, and never echoed.
func ReadRequests(r io.Reader, format, defaultService string) ([]Request, Stats, error) {
	var st Stats
	parse, err := parserFor(format, defaultService)
	if err != nil {
		return nil, st, err
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var reqs []Request
	for sc.Scan() {
		st.Lines++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			st.Blank++
			continue
		}
		req, perr := parse(line)
		if perr != nil {
			st.Malformed++
			if len(st.Errors) < maxErrors {
				st.Errors = append(st.Errors, fmt.Sprintf("line %d: %v", st.Lines, perr))
			}
			continue
		}
		reqs = append(reqs, req)
	}
	st.Requests = len(reqs)
	if err := sc.Err(); err != nil {
		return reqs, st, fmt.Errorf("read input near line %d: %w", st.Lines+1, err)
	}
	return reqs, st, nil
}

// Collect reads one input and returns events plus statistics.
// It drops duplicates by explicit request id only. See CollectOpts and Options.
func Collect(r io.Reader, format, defaultService string, slos []slo.SLO) (Result, error) {
	return CollectOpts(r, format, defaultService, slos, Options{})
}

// CollectOpts is Collect with options.
func CollectOpts(r io.Reader, format, defaultService string, slos []slo.SLO, opt Options) (Result, error) {
	if format == FormatProm {
		return collectProm(r, defaultService, slos)
	}
	reqs, st, err := ReadRequests(r, format, defaultService)
	if err != nil {
		return Result{Stats: st}, err
	}
	reqs, dups := DedupRequests(reqs, opt.DedupeLines)
	st.Duplicates = dups
	st.OutOfOrder = CountOutOfOrder(reqs)
	st.Requests = len(reqs)
	evs, noLat := ToEvents(reqs, format, slos)
	st.Events, st.NoLatency = len(evs), noLat
	return Result{Events: evs, Requests: reqs, Times: requestTimes(reqs), Stats: st}, nil
}
