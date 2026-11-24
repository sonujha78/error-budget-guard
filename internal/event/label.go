package event

import "strings"

const labelSep = "|"

// MakeLabel builds the short label "endpoint|status_class", e.g. "/pay|5xx".
func MakeLabel(endpoint, class string) string { return endpoint + labelSep + class }

// SplitLabel is the reverse of MakeLabel. The class never contains the separator,
// so the last separator is the right one even when the endpoint contains "|".
func SplitLabel(label string) (endpoint, class string) {
	i := strings.LastIndex(label, labelSep)
	if i < 0 {
		return label, ""
	}
	return label[:i], label[i+1:]
}
