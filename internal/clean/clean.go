// Package clean repairs the messy parts of collected data: clock skew, timestamps from the
// future, and gaps. It never invents data. It only shifts, bounds or measures.
package clean

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sonujha78/error-budget-guard/internal/event"
)

// MaxSkew is the largest accepted clock offset. More than this is not clock skew, it is a
// broken source, and hiding it behind an offset would be wrong.
const MaxSkew = 5 * time.Minute

// Skew maps a source name to how far that source's clock is AHEAD of the reference clock.
// Positive means ahead, so its timestamps are moved back.
type Skew map[string]time.Duration

// ParseSkew reads "nginx-log=+3s,probe=-2s".
func ParseSkew(s string) (Skew, error) {
	sk := Skew{}
	s = strings.TrimSpace(s)
	if s == "" {
		return sk, nil
	}
	for _, part := range strings.Split(s, ",") {
		name, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		name, val = strings.TrimSpace(name), strings.TrimSpace(val)
		if !ok || name == "" || val == "" {
			return nil, fmt.Errorf("bad skew entry %q: want source=offset, like nginx-log=+3s", part)
		}
		d, err := time.ParseDuration(val)
		if err != nil {
			return nil, fmt.Errorf("bad skew offset for %q: %v", name, err)
		}
		if d > MaxSkew || d < -MaxSkew {
			return nil, fmt.Errorf("skew for %q is %v: more than %v is not clock skew, fix the source", name, d, MaxSkew)
		}
		if _, dup := sk[name]; dup {
			return nil, fmt.Errorf("skew for %q given twice", name)
		}
		sk[name] = d
	}
	return sk, nil
}

// ApplySkew returns a copy of evs with every timestamp corrected. Names in the skew that
// matched no event are returned, so a typo in a source name does not pass silently.
func ApplySkew(evs []event.Event, sk Skew) ([]event.Event, []string) {
	out := make([]event.Event, len(evs))
	used := map[string]bool{}
	for i, e := range evs {
		if off, ok := sk[e.Source]; ok {
			e.Timestamp = e.Timestamp.Add(-off)
			used[e.Source] = true
		}
		out[i] = e
	}
	var unused []string
	for name := range sk {
		if !used[name] {
			unused = append(unused, name)
		}
	}
	sort.Strings(unused)
	return out, unused
}

// BoundReport says what Bound had to change.
type BoundReport struct {
	Clamped int // timestamps slightly in the future, moved to now
	Dropped int // timestamps too far in the future, removed
}

// Bound handles timestamps from the future. Up to `tolerance` ahead of now is treated as
// leftover clock skew and clamped to now. Further ahead is dropped: it cannot be trusted.
func Bound(evs []event.Event, now time.Time, tolerance time.Duration) ([]event.Event, BoundReport) {
	var rep BoundReport
	out := make([]event.Event, 0, len(evs))
	limit := now.Add(tolerance)
	for _, e := range evs {
		switch {
		case e.Timestamp.After(limit):
			rep.Dropped++
			continue
		case e.Timestamp.After(now):
			e.Timestamp = now
			rep.Clamped++
		}
		out = append(out, e)
	}
	return out, rep
}

// Gap is a stretch of time with no samples.
type Gap struct {
	Start, End time.Time
}

// Coverage says how much of a window is covered by samples.
type Coverage struct {
	Span    time.Duration
	Samples int // distinct sample times inside the window
	Gaps    []Gap
	Missing time.Duration
	Ratio   float64 // 1 - Missing/Span, always within 0..1
}

// MeasureCoverage compares sample times with the expected interval over (from, to].
// A step longer than 1.5 x interval is a gap, and the gap's missing time is the step minus
// one normal interval. Jitter is therefore not a gap. No samples at all means everything is missing.
func MeasureCoverage(times []time.Time, from, to time.Time, interval time.Duration) (Coverage, error) {
	if interval <= 0 {
		return Coverage{}, errors.New("interval must be positive")
	}
	if !to.After(from) {
		return Coverage{}, errors.New("to must be after from")
	}
	span := to.Sub(from)
	in := make([]time.Time, 0, len(times))
	for _, t := range times {
		if t.After(from) && !t.After(to) {
			in = append(in, t)
		}
	}
	sort.Slice(in, func(i, j int) bool { return in[i].Before(in[j]) })
	uniq := in[:0]
	for _, t := range in {
		if len(uniq) == 0 || !t.Equal(uniq[len(uniq)-1]) {
			uniq = append(uniq, t)
		}
	}

	c := Coverage{Span: span, Samples: len(uniq)}
	if len(uniq) == 0 {
		c.Gaps = []Gap{{from, to}}
		c.Missing = span
		return c, nil
	}
	limit := interval + interval/2
	prev := from
	step := func(next time.Time) {
		if d := next.Sub(prev); d > limit {
			c.Gaps = append(c.Gaps, Gap{prev, next})
			c.Missing += d - interval
		}
		prev = next
	}
	for _, t := range uniq {
		step(t)
	}
	step(to)

	c.Ratio = 1 - float64(c.Missing)/float64(span)
	if c.Ratio < 0 {
		c.Ratio = 0
	}
	if c.Ratio > 1 {
		c.Ratio = 1
	}
	return c, nil
}
