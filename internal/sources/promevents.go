package sources

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
	"github.com/sonujha78/error-budget-guard/internal/slo"
)

// PromReport says what had to be repaired or skipped while building events.
type PromReport struct {
	Duplicates int // scrapes with a timestamp we already had (first one kept)
	OutOfOrder int // scrapes that arrived older than one already seen (sorted back)
	Resets     int // series whose value dropped: counted from zero again
	NewSeries  int // series first seen after the baseline: assumed to start at zero
	Clamped    int // latency events where good was limited to valid
	Warnings   []string
}

// CounterDelta is the increase of a counter between two scrapes.
// A drop means the process restarted: the counter began again at zero, so the increase is cur.
// The result is never negative.
func CounterDelta(prev, cur float64) (delta float64, reset bool) {
	if cur >= prev {
		return cur - prev, false
	}
	return cur, true
}

type aggKey struct {
	handler string
	status  int
}

func sortedKeys(m map[aggKey]float64) []aggKey {
	keys := make([]aggKey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].handler != keys[j].handler {
			return keys[i].handler < keys[j].handler
		}
		return keys[i].status < keys[j].status
	})
	return keys
}

func statusOf(code string) int {
	n, err := strconv.Atoi(code)
	if err != nil {
		return 0
	}
	return n
}

func leMatches(le string, seconds float64) bool {
	f, err := strconv.ParseFloat(le, 64)
	if err != nil {
		return false
	}
	return math.Abs(f-seconds) < 1e-9
}

// PromEvents turns scrapes into events: the increase of each counter between two scrapes.
// The first scrape is only a baseline. Samples without a code label are ignored (status unknown).
func PromEvents(scrapes []Scrape, service string, slos []slo.SLO) ([]event.Event, PromReport) {
	var rep PromReport
	warn := func(format string, a ...any) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(format, a...))
	}

	seen := map[int64]bool{}
	var latest time.Time
	ordered := make([]Scrape, 0, len(scrapes))
	for _, s := range scrapes {
		key := s.Time.UnixNano()
		if seen[key] {
			rep.Duplicates++
			continue
		}
		seen[key] = true
		if s.Time.Before(latest) {
			rep.OutOfOrder++
		} else {
			latest = s.Time
		}
		ordered = append(ordered, s)
	}
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].Time.Before(ordered[j].Time) })

	var avail, lat *slo.SLO
	for i := range slos {
		s := &slos[i]
		if s.Service != service {
			continue
		}
		switch s.SLI {
		case event.SLIAvailability:
			avail = s
		case event.SLILatency:
			lat = s
		}
	}
	if avail == nil && lat == nil {
		warn("no slo defined for service %q", service)
		return nil, rep
	}
	if len(ordered) < 2 {
		warn("need at least two scrapes to compute counter deltas; no events were made")
		return nil, rep
	}

	var thr float64
	latOK := false
	if lat != nil {
		thr = lat.Good.LatencyMSLTE / 1000
		hasBuckets := false
		for _, sc := range ordered {
			for _, sm := range sc.Samples {
				if sm.Metric == MetricBucket && sm.Code != "" {
					hasBuckets = true
					if leMatches(sm.Le, thr) {
						latOK = true
					}
				}
			}
		}
		switch {
		case !hasBuckets:
			warn("slo %q: no latency histogram found in the metrics, so no latency events were made", lat.Name)
		case !latOK:
			warn("slo %q: threshold %gms is not a histogram bucket boundary; no latency events were made (no interpolation, no guessing)", lat.Name, lat.Good.LatencyMSLTE)
		}
	}

	last := map[string]float64{}
	noCode := false
	var events []event.Event
	for i, sc := range ordered {
		reqs, counts, bkts := map[aggKey]float64{}, map[aggKey]float64{}, map[aggKey]float64{}
		for _, sm := range sc.Samples {
			if sm.Code == "" {
				noCode = true
				continue
			}
			prev, known := last[sm.Series]
			last[sm.Series] = sm.Value
			if i == 0 {
				continue
			}
			var d float64
			if !known {
				d = sm.Value
				rep.NewSeries++
			} else {
				var reset bool
				d, reset = CounterDelta(prev, sm.Value)
				if reset {
					rep.Resets++
				}
			}
			if d == 0 {
				continue
			}
			k := aggKey{sm.Handler, statusOf(sm.Code)}
			switch sm.Metric {
			case MetricRequests:
				reqs[k] += d
			case MetricCount:
				counts[k] += d
			case MetricBucket:
				if latOK && leMatches(sm.Le, thr) {
					bkts[k] += d
				}
			}
		}
		if i == 0 {
			continue
		}
		if avail != nil {
			for _, k := range sortedKeys(reqs) {
				n := int64(math.Round(reqs[k]))
				if n <= 0 || !avail.IsValid(k.status) {
					continue
				}
				var good int64
				if avail.IsGood(k.status, 0) {
					good = n
				}
				events = append(events, event.Event{
					Timestamp: sc.Time, Service: service, SLI: event.SLIAvailability,
					Good: good, Valid: n, Source: sourceProm,
					Label: event.MakeLabel(k.handler, slo.StatusClass(k.status)),
				})
			}
		}
		if lat != nil && latOK {
			for _, k := range sortedKeys(counts) {
				n := int64(math.Round(counts[k]))
				if n <= 0 || !lat.IsValid(k.status) {
					continue
				}
				var good int64
				if lat.IsGood(k.status, 0) { // class check only: latency 0 always passes
					good = int64(math.Round(bkts[k]))
					if good > n {
						good = n
						rep.Clamped++
					}
				}
				events = append(events, event.Event{
					Timestamp: sc.Time, Service: service, SLI: event.SLILatency,
					Good: good, Valid: n, Source: sourceProm,
					Label: event.MakeLabel(k.handler, slo.StatusClass(k.status)),
				})
			}
		}
	}
	if noCode {
		warn("samples without a code label were ignored: their status class is unknown, so they cannot be judged (no guessing)")
	}
	if rep.Clamped > 0 {
		warn("%d latency events had more good than valid requests; good was limited to valid", rep.Clamped)
	}
	event.Sort(events)
	return events, rep
}

func collectProm(r io.Reader, service string, slos []slo.SLO) (Result, error) {
	if service == "" {
		return Result{}, errors.New("prometheus metrics have no service field: pass a service name")
	}
	scrapes, st, err := ParseScrapes(r)
	if err != nil {
		return Result{Stats: st}, err
	}
	evs, rep := PromEvents(scrapes, service, slos)
	st.Events = len(evs)
	st.Resets, st.NewSeries = rep.Resets, rep.NewSeries
	st.Duplicates, st.OutOfOrder, st.Clamped = rep.Duplicates, rep.OutOfOrder, rep.Clamped
	st.Warnings = rep.Warnings
	return Result{Events: evs, Stats: st}, nil
}
