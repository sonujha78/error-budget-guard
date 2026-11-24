package event

import (
	"math/rand"
	"reflect"
	"testing"
	"time"
)

func base() Event {
	return Event{
		Timestamp: time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC),
		Service:   "checkout",
		SLI:       SLIAvailability,
		Good:      9,
		Valid:     10,
		Source:    "accesslog",
		Label:     "5xx",
	}
}

func TestValidate(t *testing.T) {
	cases := []struct {
		name    string
		mut     func(*Event)
		wantErr bool
	}{
		{"ok", func(e *Event) {}, false},
		{"zero counts ok", func(e *Event) { e.Good = 0; e.Valid = 0 }, false},
		{"zero timestamp", func(e *Event) { e.Timestamp = time.Time{} }, true},
		{"empty service", func(e *Event) { e.Service = "" }, true},
		{"unknown sli", func(e *Event) { e.SLI = "throughput" }, true},
		{"negative good", func(e *Event) { e.Good = -1 }, true},
		{"negative valid", func(e *Event) { e.Valid = -1 }, true},
		{"good greater than valid", func(e *Event) { e.Good = 11 }, true},
		{"empty source", func(e *Event) { e.Source = "" }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := base()
			c.mut(&e)
			err := e.Validate()
			if (err != nil) != c.wantErr {
				t.Fatalf("Validate() err=%v, wantErr=%v", err, c.wantErr)
			}
		})
	}
}

func makeEvents() []Event {
	var evs []Event
	labels := []string{"2xx", "5xx", "/pay", "/cart"}
	svcs := []string{"checkout", "search", "payments"}
	for i := 0; i < 50; i++ {
		e := base()
		e.Timestamp = e.Timestamp.Add(time.Duration(i%7) * time.Second)
		e.Service = svcs[i%3]
		e.Label = labels[i%4]
		e.Good = int64(i)
		e.Valid = int64(i + 1)
		evs = append(evs, e)
	}
	return evs
}

func TestSortIsDeterministic(t *testing.T) {
	a := makeEvents()
	b := makeEvents()
	rand.New(rand.NewSource(1)).Shuffle(len(a), func(i, j int) { a[i], a[j] = a[j], a[i] })
	rand.New(rand.NewSource(2)).Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	Sort(a)
	Sort(b)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Sort gave different order for differently shuffled input")
	}
	for i := 1; i < len(a); i++ {
		if a[i].Timestamp.Before(a[i-1].Timestamp) {
			t.Fatalf("events not in time order at index %d", i)
		}
	}
}
