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
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"

	"github.com/flightctl/flightctl/test/scripts/pkg/e2etestutils"
	"github.com/spf13/cobra"
)

// fmtDuration formats seconds as "Xm Ys" (e.g. "22m 15s").
func fmtDuration(secs float64) string {
	total := int(math.Round(secs))
	m, s := total/60, total%60
	if m == 0 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm %02ds", m, s)
}

// Type aliases so the rest of this file keeps using the short names.
type specTiming = e2etestutils.SpecTiming
type specInfo = e2etestutils.SpecInfo

const suiteOverheadPrefix = e2etestutils.SuiteOverheadPrefix

func loadDiscovery(path string) ([]specInfo, error) {
	return e2etestutils.LoadDiscovery(path)
}

func loadTimings(path string) (map[string]specTiming, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]specTiming{}, nil
		}
		return nil, fmt.Errorf("read timings file: %w", err)
	}
	var timings map[string]specTiming
	if err := json.Unmarshal(data, &timings); err != nil {
		return nil, fmt.Errorf("parse timings file: %w", err)
	}
	return timings, nil
}

func printSummary(assignments map[string][]string, specTimings map[string]specTiming, suiteTimings map[string]specTiming, defDuration float64, unknowns []string, specs []specInfo, sigma float64) {
	// Build a lookup from spec name to suite name.
	suiteLookup := make(map[string]string, len(specs))
	for _, s := range specs {
		suiteLookup[s.Name] = s.Suite
	}

	label := "Est. Duration"
	if sigma > 0 {
		label = fmt.Sprintf("Est. (σ×%.1f)", sigma)
	}
	fmt.Println()
	fmt.Printf("%-6s %15s %8s\n", "Node", label, "Specs")
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
						total += e2etestutils.EffectiveWeight(overhead, sigma)
					}
					suitesSeen[suite] = struct{}{}
				}
			}
			if t, ok := specTimings[s]; ok {
				total += e2etestutils.EffectiveWeight(t, sigma)
			} else {
				total += defDuration
			}
		}
		totals = append(totals, total)
		fmt.Printf("  %-4d %15s %8d\n", nodeID, fmtDuration(total), len(nodeSpecs))
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
		fmt.Printf("  Min node:         %s\n", fmtDuration(minT))
		fmt.Printf("  Max node:         %s\n", fmtDuration(maxT))
		fmt.Printf("  Spread (max-min): %s\n", fmtDuration(maxT-minT))
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
		sigma       float64
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
			if sigma < 0 {
				return fmt.Errorf("--jitter-sigma must be >= 0")
			}

			specs, err := loadDiscovery(discovery)
			if err != nil {
				return err
			}

			allTimings, err := loadTimings(timingsPath)
			if err != nil {
				return err
			}
			specTimings, suiteTimings := e2etestutils.SeparateTimings(allTimings)

			def := defSecs
			if def < 0 {
				def = e2etestutils.DefaultDuration(specTimings, 60.0)
			}
			fmt.Printf("Default duration for unknown specs: %.1fs\n", def)

			if sigma > 0 {
				fmt.Printf("Jitter inflation: avg + %.1f×stddev\n", sigma)
			}
			if len(suiteTimings) > 0 {
				fmt.Printf("Suite BeforeSuite overhead: %d suite(s) tracked\n", len(suiteTimings))
			}

			var unknowns []string
			for _, s := range specs {
				if _, ok := specTimings[s.Name]; !ok {
					unknowns = append(unknowns, s.Name)
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

			assignments, _ := e2etestutils.LPTAssign(specs, specTimings, suiteTimings, nodes, def, sigma)
			if err := writeJSON(output, assignments); err != nil {
				return err
			}
			fmt.Printf("Assignments written to: %s\n", output)
			printSummary(assignments, specTimings, suiteTimings, def, unknowns, specs, sigma)
			return nil
		},
	}

	cmd.Flags().StringVar(&discovery, "discovery", "discovery.json", "Ginkgo dry-run JSON report")
	cmd.Flags().StringVar(&timingsPath, "timings", "test/scripts/test-timings.json", "Timing cache path")
	cmd.Flags().IntVar(&nodes, "nodes", 0, "Number of shards to assign to (required)")
	cmd.Flags().StringVar(&output, "output", "assignments.json", "Output file")
	cmd.Flags().Float64Var(&defSecs, "default-secs", -1, "Fallback duration for unknown specs in seconds (default: max(60, median))")
	cmd.Flags().Float64Var(&sigma, "jitter-sigma", 1.0, "Std deviations added to each spec's avg when estimating weight (0 = avg only, 1 = avg+1σ pessimistic buffer)")
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
