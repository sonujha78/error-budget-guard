package event

import "testing"

func TestLabelRoundTrip(t *testing.T) {
	cases := []struct{ ep, class string }{
		{"/pay", "5xx"},
		{"/orders/:id", "2xx"},
		{"/a|b", "other"},
		{"/", "2xx"},
	}
	for _, c := range cases {
		l := MakeLabel(c.ep, c.class)
		ep, cl := SplitLabel(l)
		if ep != c.ep || cl != c.class {
			t.Errorf("round trip of (%q,%q) via %q gave (%q,%q)", c.ep, c.class, l, ep, cl)
		}
	}
}

func TestSplitLabelWithoutSeparator(t *testing.T) {
	ep, cl := SplitLabel("legacy")
	if ep != "legacy" || cl != "" {
		t.Errorf("got (%q,%q), want (legacy,\"\")", ep, cl)
	}
}
