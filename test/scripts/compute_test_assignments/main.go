// Command compute_test_assignments produces duration-weighted shard assignments
// for e2e tests using the Longest Processing Time (LPT) greedy bin-packing
// algorithm.
//
// It reads a Ginkgo dry-run discovery JSON and the committed test-timings.json
// cache, then assigns each spec to the shard with the current minimum estimated
// total so that shard durations are as even as possible.
//
// Per-suite BeforeSuite overhead (stored in the timings cache under the
// "__suite__:SuiteName" key) is added to a node's estimated total the first
// time a spec from that suite is assigned to it.  This prevents the algorithm
// from under-estimating nodes that are the only runner for a suite whose setup
// (e.g. VM boot) is expensive.
//
// Specs without a historical timing entry are assigned a default weight of
// max(60s, median_of_known_durations), preventing new potentially-slow tests
// from over-loading an otherwise-fast shard.
//
// Usage:
//
//	go run ./test/scripts/compute_test_assignments \
//	    --discovery discovery.json \
//	    --timings test/scripts/test-timings.json \
//	    --nodes N \
//	    --output assignments.json
package main

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

// suiteOverheadPrefix is the key prefix used for per-suite BeforeSuite timings
// in the cache, matching the prefix used by update_test_timings.
const suiteOverheadPrefix = "__suite__:"

// specReport is the subset of a Ginkgo SpecReport we care about.
type specReport struct {
	LeafNodeType            string   `json:"LeafNodeType"`
	LeafNodeText            string   `json:"LeafNodeText"`
	ContainerHierarchyTexts []string `json:"ContainerHierarchyTexts"`
	State                   string   `json:"State"`
}

// suiteReport is the top-level array element in a Ginkgo JSON report.
type suiteReport struct {
	SuiteDescription string       `json:"SuiteDescription"`
	SpecReports      []specReport `json:"SpecReports"`
}

// specInfo pairs a spec's full name with its parent suite name.
type specInfo struct {
	name  string
	suite string
}

// nodeState tracks the accumulated estimated duration and assigned specs for
// one shard in the LPT heap.
type nodeState struct {
	id     int
	total  float64
	specs  []string
	suites map[string]struct{} // suites already started on this node
}

// nodeHeap is a min-heap of nodeState ordered by total estimated duration.
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

func loadDiscovery(path string) ([]specInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read discovery file: %w", err)
	}
	var suites []suiteReport
	if err := json.Unmarshal(data, &suites); err != nil {
		return nil, fmt.Errorf("parse discovery file: %w", err)
	}

	seen := make(map[string]struct{})
	var specs []specInfo
	for _, suite := range suites {
		for _, sr := range suite.SpecReports {
			if sr.LeafNodeType != "It" || sr.State == "skipped" {
				continue
			}
			if sr.LeafNodeText == "" {
				continue
			}
			parts := append(sr.ContainerHierarchyTexts, sr.LeafNodeText)
			fullName := strings.Join(parts, " ")
			if _, exists := seen[fullName]; exists {
				continue
			}
			seen[fullName] = struct{}{}
			specs = append(specs, specInfo{name: fullName, suite: suite.SuiteDescription})
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].name < specs[j].name })
	return specs, nil
}

func loadTimings(path string) (map[string]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]float64{}, nil
		}
		return nil, fmt.Errorf("read timings file: %w", err)
	}
	var timings map[string]float64
	if err := json.Unmarshal(data, &timings); err != nil {
		return nil, fmt.Errorf("parse timings file: %w", err)
	}
	return timings, nil
}

// separateTimings splits the combined timings map into spec timings and
// per-suite BeforeSuite overhead timings (keys prefixed with suiteOverheadPrefix).
func separateTimings(all map[string]float64) (specTimings, suiteTimings map[string]float64) {
	specTimings = make(map[string]float64, len(all))
	suiteTimings = make(map[string]float64)
	for k, v := range all {
		if strings.HasPrefix(k, suiteOverheadPrefix) {
			suiteTimings[strings.TrimPrefix(k, suiteOverheadPrefix)] = v
		} else {
			specTimings[k] = v
		}
	}
	return
}

func medianFloat(values []float64) float64 {
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

func defaultDuration(specTimings map[string]float64, floor float64) float64 {
	if len(specTimings) == 0 {
		return floor
	}
	vals := make([]float64, 0, len(specTimings))
	for _, v := range specTimings {
		vals = append(vals, v)
	}
	return math.Max(floor, medianFloat(vals))
}

// weighted pairs a spec with its estimated duration.
type weighted struct {
	spec     specInfo
	duration float64
}

func lptAssign(specs []specInfo, specTimings map[string]float64, suiteTimings map[string]float64, nNodes int, defDuration float64) map[string][]string {
	// Pair each spec with its duration; sort descending (LPT property).
	ws := make([]weighted, len(specs))
	for i, s := range specs {
		dur, ok := specTimings[s.name]
		if !ok {
			dur = defDuration
		}
		ws[i] = weighted{s, dur}
	}
	sort.Slice(ws, func(i, j int) bool { return ws[i].duration > ws[j].duration })

	h := make(nodeHeap, nNodes)
	for i := range h {
		h[i] = nodeState{id: i + 1, specs: []string{}, suites: make(map[string]struct{})}
	}
	heap.Init(&h)

	for _, w := range ws {
		n := heap.Pop(&h).(nodeState)

		// Add BeforeSuite overhead the first time a spec from this suite lands
		// on this node.
		if w.spec.suite != "" {
			if _, started := n.suites[w.spec.suite]; !started {
				if overhead, ok := suiteTimings[w.spec.suite]; ok {
					n.total += overhead
				}
				n.suites[w.spec.suite] = struct{}{}
			}
		}

		n.specs = append(n.specs, w.spec.name)
		n.total += w.duration
		heap.Push(&h, n)
	}

	result := make(map[string][]string, nNodes)
	for _, n := range h {
		result[strconv.Itoa(n.id)] = n.specs
	}
	return result
}

func printSummary(assignments map[string][]string, specTimings map[string]float64, suiteTimings map[string]float64, defDuration float64, unknowns []string, specs []specInfo) {
	// Build a lookup from spec name to suite name.
	suiteLookup := make(map[string]string, len(specs))
	for _, s := range specs {
		suiteLookup[s.name] = s.suite
	}

	fmt.Println()
	fmt.Printf("%-6s %15s %8s\n", "Node", "Est. Duration", "Specs")
	fmt.Println("-----  ---------------  ------")

	totals := make([]float64, 0, len(assignments))
	for nodeID := 1; nodeID <= len(assignments); nodeID++ {
		nodeSpecs := assignments[strconv.Itoa(nodeID)]
		var total float64
		suitesSeen := make(map[string]struct{})
		for _, s := range nodeSpecs {
			suite := suiteLookup[s]
			if suite != "" {
				if _, seen := suitesSeen[suite]; !seen {
					if overhead, ok := suiteTimings[suite]; ok {
						total += overhead
					}
					suitesSeen[suite] = struct{}{}
				}
			}
			if d, ok := specTimings[s]; ok {
				total += d
			} else {
				total += defDuration
			}
		}
		totals = append(totals, total)
		fmt.Printf("  %-4d %13.1fs %8d\n", nodeID, total, len(nodeSpecs))
	}

	if len(totals) > 0 {
		minT, maxT := totals[0], totals[0]
		for _, t := range totals[1:] {
			if t < minT {
				minT = t
			}
			if t > maxT {
				maxT = t
			}
		}
		fmt.Printf("  Min node:         %.1fs\n", minT)
		fmt.Printf("  Max node:         %.1fs\n", maxT)
		fmt.Printf("  Spread (max-min): %.1fs\n", maxT-minT)
	}

	if len(unknowns) > 0 {
		fmt.Printf("\n  %d spec(s) used default duration (%.0fs):\n", len(unknowns), defDuration)
		limit := 5
		if len(unknowns) < limit {
			limit = len(unknowns)
		}
		for _, s := range unknowns[:limit] {
			fmt.Printf("    - %q\n", s)
		}
		if len(unknowns) > 5 {
			fmt.Printf("    ... and %d more\n", len(unknowns)-5)
		}
	}
	fmt.Println()
}

func newRootCmd() *cobra.Command {
	var (
		discovery   string
		timingsPath string
		nodes       int
		output      string
		defSecs     float64
	)

	cmd := &cobra.Command{
		Use:   "compute_test_assignments",
		Short: "Compute duration-weighted e2e test shard assignments via LPT bin-packing",
		Long: `Reads a Ginkgo dry-run discovery JSON and the committed test-timings.json
cache, then assigns specs to shards using the Longest Processing Time (LPT)
greedy algorithm so that estimated shard durations are as even as possible.

Per-suite BeforeSuite overhead is accounted for: the first time a spec from a
given suite is assigned to a node, the suite's average BeforeSuite duration is
added to that node's estimated total.

Specs without historical timing data use max(60s, median_of_known) as a
default weight so new tests do not overload an otherwise-fast shard.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if nodes < 1 {
				return fmt.Errorf("--nodes must be >= 1")
			}

			specs, err := loadDiscovery(discovery)
			if err != nil {
				return err
			}

			allTimings, err := loadTimings(timingsPath)
			if err != nil {
				return err
			}
			specTimings, suiteTimings := separateTimings(allTimings)

			def := defSecs
			if def < 0 {
				def = defaultDuration(specTimings, 60.0)
			}
			fmt.Printf("Default duration for unknown specs: %.1fs\n", def)

			if len(suiteTimings) > 0 {
				fmt.Printf("Suite BeforeSuite overhead: %d suite(s) tracked\n", len(suiteTimings))
			}

			var unknowns []string
			for _, s := range specs {
				if _, ok := specTimings[s.name]; !ok {
					unknowns = append(unknowns, s.name)
				}
			}
			fmt.Printf("Specs: %d total, %d with timing data, %d using default\n",
				len(specs), len(specs)-len(unknowns), len(unknowns))

			if len(specs) == 0 {
				fmt.Fprintln(os.Stderr, "Warning: no specs found in discovery; writing empty assignments.")
				empty := make(map[string][]string, nodes)
				for i := 1; i <= nodes; i++ {
					empty[strconv.Itoa(i)] = []string{}
				}
				return writeJSON(output, empty)
			}

			assignments := lptAssign(specs, specTimings, suiteTimings, nodes, def)
			if err := writeJSON(output, assignments); err != nil {
				return err
			}
			fmt.Printf("Assignments written to: %s\n", output)
			printSummary(assignments, specTimings, suiteTimings, def, unknowns, specs)
			return nil
		},
	}

	cmd.Flags().StringVar(&discovery, "discovery", "discovery.json", "Ginkgo dry-run JSON report")
	cmd.Flags().StringVar(&timingsPath, "timings", "test/scripts/test-timings.json", "Timing cache path")
	cmd.Flags().IntVar(&nodes, "nodes", 0, "Number of shards to assign to (required)")
	cmd.Flags().StringVar(&output, "output", "assignments.json", "Output file")
	cmd.Flags().Float64Var(&defSecs, "default-secs", -1, "Fallback duration for unknown specs in seconds (default: max(60, median))")
	_ = cmd.MarkFlagRequired("nodes")

	return cmd
}

func writeJSON(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal JSON: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}
