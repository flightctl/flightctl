package e2etestutils

import (
	"container/heap"
	"math"
	"sort"
	"strconv"
	"strings"
)

// SpecInfo pairs a spec's full name with the Ginkgo suite it belongs to.
// The suite name is used to add per-suite BeforeSuite overhead the first
// time a spec from that suite is assigned to a shard.
type SpecInfo struct {
	Name         string
	Suite        string
	OrderedGroup string
}

// nodeState tracks accumulated estimated duration and assigned specs for one
// shard in the LPT min-heap.
type nodeState struct {
	id     int
	total  float64
	specs  []string
	suites map[string]struct{}
}

type nodeHeap []nodeState

func (h nodeHeap) Len() int            { return len(h) }
func (h nodeHeap) Less(i, j int) bool  { return h[i].total < h[j].total }
func (h nodeHeap) Swap(i, j int)       { h[i], h[j] = h[j], h[i] }
func (h *nodeHeap) Push(x interface{}) { *h = append(*h, x.(nodeState)) }
func (h *nodeHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}

type weighted struct {
	spec     SpecInfo
	duration float64
}

type weightedGroup struct {
	specs    []SpecInfo
	duration float64
}

// EffectiveWeight returns avg + sigma*stddev.
// sigma=0 uses plain averages; sigma=1 adds one standard deviation as a
// pessimistic buffer for high-jitter specs (the default used by CI).
func EffectiveWeight(t SpecTiming, sigma float64) float64 {
	return t.Avg + sigma*t.StdDev
}

// MedianFloat returns the median of a float64 slice (sorts a copy).
func MedianFloat(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// DefaultDuration returns max(floor, median(avg)) across all known spec timings.
// Used as the fallback weight for specs with no historical data.
func DefaultDuration(specTimings map[string]SpecTiming, floor float64) float64 {
	if len(specTimings) == 0 {
		return floor
	}
	vals := make([]float64, 0, len(specTimings))
	for _, v := range specTimings {
		vals = append(vals, v.Avg)
	}
	return math.Max(floor, MedianFloat(vals))
}

// SeparateTimings splits a combined timings map (as loaded from test-timings.json)
// into per-spec timings and per-suite BeforeSuite overhead timings.
// Keys prefixed with SuiteOverheadPrefix are treated as suite overhead.
func SeparateTimings(all map[string]SpecTiming) (specTimings, suiteTimings map[string]SpecTiming) {
	specTimings = make(map[string]SpecTiming, len(all))
	suiteTimings = make(map[string]SpecTiming)
	for k, v := range all {
		if strings.HasPrefix(k, SuiteOverheadPrefix) {
			suiteTimings[strings.TrimPrefix(k, SuiteOverheadPrefix)] = v
		} else {
			specTimings[k] = v
		}
	}
	return
}

// LPTAssign assigns specs to nNodes shards using the Longest Processing Time
// greedy bin-packing algorithm.
//
//   - specTimings: per-spec average durations (non-suite entries).
//   - suiteTimings: per-suite BeforeSuite overhead (suite entries, keys without prefix).
//   - defDuration: fallback weight for specs absent from specTimings.
//   - sigma: std-deviation multiplier for pessimistic inflation (0 = avg only).
//
// Returns the shard assignments map (nodeID → spec names) and the maximum
// shard total (critical-path estimate).
func LPTAssign(specs []SpecInfo, specTimings, suiteTimings map[string]SpecTiming, nNodes int, defDuration, sigma float64) (assignments map[string][]string, maxTotal float64) {
	if nNodes <= 0 {
		return map[string][]string{}, 0
	}
	ws := make([]weighted, len(specs))
	for i, s := range specs {
		var dur float64
		if t, ok := specTimings[s.Name]; ok {
			dur = EffectiveWeight(t, sigma)
		} else {
			dur = defDuration
		}
		ws[i] = weighted{spec: s, duration: dur}
	}
	groups := buildWeightedGroups(ws)
	sort.Slice(groups, func(i, j int) bool { return groups[i].duration > groups[j].duration })

	h := make(nodeHeap, nNodes)
	for i := range h {
		h[i] = nodeState{id: i + 1, specs: []string{}, suites: make(map[string]struct{})}
	}
	heap.Init(&h)

	for _, group := range groups {
		// Pick the node whose projected total (current + BeforeSuite if new + spec) is
		// lowest. This avoids the classic greedy mistake of choosing the currently
		// lightest node when it would trigger an expensive BeforeSuite that a slightly
		// heavier node (which already ran that suite) would not incur.
		bestIdx := 0
		bestProjected := math.Inf(1)
		groupSuites := groupSuites(group)
		for i := range h {
			projected := h[i].total + group.duration
			for _, suite := range groupSuites {
				if _, started := h[i].suites[suite]; !started {
					if overhead, ok := suiteTimings[suite]; ok {
						projected += EffectiveWeight(overhead, sigma)
					}
				}
			}
			if projected < bestProjected {
				bestProjected = projected
				bestIdx = i
			}
		}
		n := heap.Remove(&h, bestIdx).(nodeState)
		for _, suite := range groupSuites {
			if _, started := n.suites[suite]; !started {
				if overhead, ok := suiteTimings[suite]; ok {
					n.total += EffectiveWeight(overhead, sigma)
				}
				n.suites[suite] = struct{}{}
			}
		}
		for _, spec := range group.specs {
			n.specs = append(n.specs, spec.Name)
		}
		n.total += group.duration
		heap.Push(&h, n)
	}

	assignments = make(map[string][]string, nNodes)
	for _, n := range h {
		assignments[strconv.Itoa(n.id)] = n.specs
		if n.total > maxTotal {
			maxTotal = n.total
		}
	}
	return
}

func buildWeightedGroups(specs []weighted) []weightedGroup {
	groups := make([]weightedGroup, 0, len(specs))
	orderedGroups := make(map[string]int)
	for _, spec := range specs {
		if spec.spec.OrderedGroup == "" {
			groups = append(groups, weightedGroup{
				specs:    []SpecInfo{spec.spec},
				duration: spec.duration,
			})
			continue
		}

		groupIdx, exists := orderedGroups[spec.spec.OrderedGroup]
		if !exists {
			groupIdx = len(groups)
			orderedGroups[spec.spec.OrderedGroup] = groupIdx
			groups = append(groups, weightedGroup{})
		}
		groups[groupIdx].specs = append(groups[groupIdx].specs, spec.spec)
		groups[groupIdx].duration += spec.duration
	}
	return groups
}

func groupSuites(group weightedGroup) []string {
	seen := make(map[string]struct{}, len(group.specs))
	suites := make([]string, 0, len(group.specs))
	for _, spec := range group.specs {
		if spec.Suite == "" {
			continue
		}
		if _, exists := seen[spec.Suite]; exists {
			continue
		}
		seen[spec.Suite] = struct{}{}
		suites = append(suites, spec.Suite)
	}
	return suites
}
