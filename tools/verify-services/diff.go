package main

import (
	"fmt"
	"slices"
	"strings"
)

// DiffReports missing and unexpected items between want and got sets.
type Diff struct {
	Missing    []string
	Unexpected []string
}

func DiffSets(want, got map[string]struct{}) Diff {
	var d Diff
	for k := range want {
		if _, ok := got[k]; !ok {
			d.Missing = append(d.Missing, k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			d.Unexpected = append(d.Unexpected, k)
		}
	}
	return d
}

func (d Diff) Empty() bool {
	return len(d.Missing) == 0 && len(d.Unexpected) == 0
}

func (d Diff) Format(label string) string {
	if d.Empty() {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", label)
	if len(d.Missing) > 0 {
		missing := slices.Clone(d.Missing)
		slices.Sort(missing)
		fmt.Fprintf(&b, "  missing: %s\n", strings.Join(missing, ", "))
	}
	if len(d.Unexpected) > 0 {
		unexpected := slices.Clone(d.Unexpected)
		slices.Sort(unexpected)
		fmt.Fprintf(&b, "  unexpected: %s\n", strings.Join(unexpected, ", "))
	}
	return b.String()
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return out
}

func toSet(items []string) map[string]struct{} {
	s := make(map[string]struct{}, len(items))
	for _, i := range items {
		s[i] = struct{}{}
	}
	return s
}
