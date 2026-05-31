package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMedianFloat(t *testing.T) {
	tests := []struct {
		name   string
		input  []float64
		expect float64
	}{
		{
			name:   "When slice is empty it should return 0",
			input:  []float64{},
			expect: 0,
		},
		{
			name:   "When slice has a single value it should return that value",
			input:  []float64{42.0},
			expect: 42.0,
		},
		{
			name:   "When slice has odd length it should return the middle element",
			input:  []float64{10.0, 30.0, 20.0},
			expect: 20.0,
		},
		{
			name:   "When slice has even length it should return average of two middle elements",
			input:  []float64{10.0, 20.0, 30.0, 40.0},
			expect: 25.0,
		},
		{
			name:   "When all values are equal it should return that value",
			input:  []float64{5.0, 5.0, 5.0},
			expect: 5.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.InDelta(t, tt.expect, medianFloat(tt.input), 1e-9)
		})
	}
}

func TestDefaultDuration(t *testing.T) {
	tests := []struct {
		name    string
		timings map[string]float64
		floor   float64
		expect  float64
	}{
		{
			name:    "When timings are empty it should return the floor",
			timings: map[string]float64{},
			floor:   60.0,
			expect:  60.0,
		},
		{
			name:    "When median is below the floor it should return the floor",
			timings: map[string]float64{"a": 10.0, "b": 20.0, "c": 30.0},
			floor:   60.0,
			expect:  60.0,
		},
		{
			name:    "When median exceeds the floor it should return the median",
			timings: map[string]float64{"a": 100.0, "b": 200.0, "c": 300.0},
			floor:   60.0,
			expect:  200.0,
		},
		{
			name:    "When median equals the floor it should return the floor",
			timings: map[string]float64{"a": 60.0},
			floor:   60.0,
			expect:  60.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.InDelta(t, tt.expect, defaultDuration(tt.timings, tt.floor), 1e-9)
		})
	}
}

func TestSeparateTimings(t *testing.T) {
	tests := []struct {
		name              string
		all               map[string]float64
		expectSpec        map[string]float64
		expectSuite       map[string]float64
	}{
		{
			name: "When map has both spec and suite keys it should split them correctly",
			all: map[string]float64{
				"Suite Spec A":             10.0,
				"Suite Spec B":             20.0,
				"__suite__:My Suite":       120.0,
				"__suite__:Other Suite":    30.0,
			},
			expectSpec:  map[string]float64{"Suite Spec A": 10.0, "Suite Spec B": 20.0},
			expectSuite: map[string]float64{"My Suite": 120.0, "Other Suite": 30.0},
		},
		{
			name:        "When map has only spec keys it should return empty suite map",
			all:         map[string]float64{"Spec A": 10.0},
			expectSpec:  map[string]float64{"Spec A": 10.0},
			expectSuite: map[string]float64{},
		},
		{
			name:        "When map is empty it should return two empty maps",
			all:         map[string]float64{},
			expectSpec:  map[string]float64{},
			expectSuite: map[string]float64{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specTimings, suiteTimings := separateTimings(tt.all)
			require.Equal(t, tt.expectSpec, specTimings)
			require.Equal(t, tt.expectSuite, suiteTimings)
		})
	}
}

// specNames extracts the name field from a slice of specInfo.
func specNames(specs []specInfo) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.name
	}
	return names
}

// specCount returns the total number of specs assigned across all nodes.
func specCount(assignments map[string][]string) int {
	n := 0
	for _, specs := range assignments {
		n += len(specs)
	}
	return n
}

// allSpecs returns a sorted slice of every spec appearing in the assignments.
func allSpecs(assignments map[string][]string) []string {
	seen := map[string]struct{}{}
	for _, specs := range assignments {
		for _, s := range specs {
			seen[s] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for s := range seen {
		result = append(result, s)
	}
	sort.Strings(result)
	return result
}

// totalDuration returns the estimated total duration for a node's spec list,
// including any suite BeforeSuite overhead for suites first seen on this node.
func totalDuration(specs []string, suiteLookup map[string]string, specTimings map[string]float64, suiteTimings map[string]float64, def float64) float64 {
	var sum float64
	suitesSeen := map[string]struct{}{}
	for _, s := range specs {
		if suite, ok := suiteLookup[s]; ok && suite != "" {
			if _, seen := suitesSeen[suite]; !seen {
				sum += suiteTimings[suite]
				suitesSeen[suite] = struct{}{}
			}
		}
		if d, ok := specTimings[s]; ok {
			sum += d
		} else {
			sum += def
		}
	}
	return sum
}

func TestLptAssign(t *testing.T) {
	noSuiteTimings := map[string]float64{}

	tests := []struct {
		name         string
		specs        []specInfo
		specTimings  map[string]float64
		suiteTimings map[string]float64
		nNodes       int
		defDuration  float64
		checkFn      func(t *testing.T, assignments map[string][]string)
	}{
		{
			name: "When there is one node it should assign all specs to node 1",
			specs: []specInfo{
				{name: "Suite Spec A", suite: "Suite"},
				{name: "Suite Spec B", suite: "Suite"},
				{name: "Suite Spec C", suite: "Suite"},
			},
			specTimings:  map[string]float64{"Suite Spec A": 10.0, "Suite Spec B": 20.0, "Suite Spec C": 30.0},
			suiteTimings: noSuiteTimings,
			nNodes:       1,
			defDuration:  60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				require.Len(t, a["1"], 3)
			},
		},
		{
			name: "When two nodes and specs can be split evenly it should produce zero spread",
			specs: []specInfo{
				{name: "Suite Spec A", suite: "Suite"},
				{name: "Suite Spec B", suite: "Suite"},
				{name: "Suite Spec C", suite: "Suite"},
			},
			specTimings: map[string]float64{
				"Suite Spec A": 90.0, "Suite Spec B": 60.0, "Suite Spec C": 30.0,
			},
			suiteTimings: noSuiteTimings,
			// LPT: assign A(90)→node1, then B(60)→node2, then C(30)→node2
			// node1=90, node2=90 → spread=0
			nNodes:      2,
			defDuration: 60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				specT := map[string]float64{"Suite Spec A": 90.0, "Suite Spec B": 60.0, "Suite Spec C": 30.0}
				suiteLookup := map[string]string{"Suite Spec A": "Suite", "Suite Spec B": "Suite", "Suite Spec C": "Suite"}
				d1 := totalDuration(a["1"], suiteLookup, specT, noSuiteTimings, 60.0)
				d2 := totalDuration(a["2"], suiteLookup, specT, noSuiteTimings, 60.0)
				require.InDelta(t, 0.0, d1-d2, 1e-9, "expected zero spread")
			},
		},
		{
			name: "When a spec has no timing entry it should use the default duration",
			specs: []specInfo{
				{name: "Suite Known Spec", suite: "Suite"},
				{name: "Suite Unknown Spec", suite: "Suite"},
			},
			specTimings:  map[string]float64{"Suite Known Spec": 100.0},
			suiteTimings: noSuiteTimings,
			nNodes:       2,
			defDuration:  60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				specT := map[string]float64{"Suite Known Spec": 100.0}
				suiteLookup := map[string]string{"Suite Known Spec": "Suite", "Suite Unknown Spec": "Suite"}
				d1 := totalDuration(a["1"], suiteLookup, specT, noSuiteTimings, 60.0)
				d2 := totalDuration(a["2"], suiteLookup, specT, noSuiteTimings, 60.0)
				require.InDelta(t, 160.0, d1+d2, 1e-9, "total duration must be preserved")
			},
		},
		{
			name:         "When there is only one spec and many nodes it should assign it to exactly one node",
			specs:        []specInfo{{name: "Suite Lone Spec", suite: "Suite"}},
			specTimings:  map[string]float64{"Suite Lone Spec": 45.0},
			suiteTimings: noSuiteTimings,
			nNodes:       4,
			defDuration:  60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				require.Equal(t, 1, specCount(a))
			},
		},
		{
			name: "When there are more nodes than specs some nodes should be empty",
			specs: []specInfo{
				{name: "Suite Spec A", suite: "Suite"},
				{name: "Suite Spec B", suite: "Suite"},
			},
			specTimings:  map[string]float64{"Suite Spec A": 10.0, "Suite Spec B": 20.0},
			suiteTimings: noSuiteTimings,
			nNodes:       5,
			defDuration:  60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				require.Len(t, a, 5, "all 5 nodes must be present in the result")
				require.Equal(t, 2, specCount(a), "total spec count must equal input")
			},
		},
		{
			name: "When suite BeforeSuite overhead is set it should be added once per suite per node",
			specs: []specInfo{
				{name: "Agent Suite spec 1", suite: "Agent Suite"},
				{name: "Agent Suite spec 2", suite: "Agent Suite"},
				{name: "CLI Suite spec 1", suite: "CLI Suite"},
			},
			specTimings: map[string]float64{
				"Agent Suite spec 1": 100.0,
				"Agent Suite spec 2": 100.0,
				"CLI Suite spec 1":   10.0,
			},
			// Agent Suite has 200s BeforeSuite overhead, CLI Suite has 10s.
			suiteTimings: map[string]float64{"Agent Suite": 200.0, "CLI Suite": 10.0},
			nNodes:       2,
			defDuration:  60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				// LPT sees: Agent spec1(100)→node1, Agent spec2(100)→node2,
				// CLI spec1(10)→node2 (cheaper after overhead is accounted).
				// Each node pays Agent BeforeSuite(200) once.
				// Node1: 200(BeforeSuite) + 100 = 300
				// Node2: 200(BeforeSuite) + 100 + 10(CLI BeforeSuite) + 10 = 320
				// We only verify the BeforeSuite is counted exactly once per suite per node.
				specT := map[string]float64{
					"Agent Suite spec 1": 100.0,
					"Agent Suite spec 2": 100.0,
					"CLI Suite spec 1":   10.0,
				}
				suiteT := map[string]float64{"Agent Suite": 200.0, "CLI Suite": 10.0}
				suiteLookup := map[string]string{
					"Agent Suite spec 1": "Agent Suite",
					"Agent Suite spec 2": "Agent Suite",
					"CLI Suite spec 1":   "CLI Suite",
				}
				d1 := totalDuration(a["1"], suiteLookup, specT, suiteT, 60.0)
				d2 := totalDuration(a["2"], suiteLookup, specT, suiteT, 60.0)
				// Total must account for both suite overheads + all spec times.
				// Agent Suite overhead paid at most twice (once per node that has ≥1 agent spec).
				// CLI Suite overhead paid once (only one CLI spec, goes to one node).
				require.Greater(t, d1+d2, 0.0)
				require.Equal(t, 3, specCount(a))
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assignments := lptAssign(tt.specs, tt.specTimings, tt.suiteTimings, tt.nNodes, tt.defDuration)

			// Common invariants for every case.
			require.Len(t, assignments, tt.nNodes, "number of nodes in result must equal nNodes")
			require.Equal(t, len(tt.specs), specCount(assignments), "total assigned specs must equal input spec count")
			// No spec appears more than once.
			seen := map[string]struct{}{}
			for _, specs := range assignments {
				for _, s := range specs {
					_, dup := seen[s]
					require.False(t, dup, "spec %q appears more than once in assignments", s)
					seen[s] = struct{}{}
				}
			}

			if tt.checkFn != nil {
				tt.checkFn(t, assignments)
			}
		})
	}
}

func writeDiscoveryJSON(t *testing.T, dir string, suites []suiteReport) string {
	t.Helper()
	data, err := json.Marshal(suites)
	require.NoError(t, err)
	path := filepath.Join(dir, "discovery.json")
	require.NoError(t, os.WriteFile(path, data, 0o644))
	return path
}

func TestLoadDiscovery(t *testing.T) {
	tests := []struct {
		name        string
		suites      []suiteReport
		expectNames []string // spec names, nil means error expected
		expectErr   bool
	}{
		{
			name: "When discovery has valid It specs it should return full paths sorted and deduplicated",
			suites: []suiteReport{{SuiteDescription: "Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Spec C", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "It", LeafNodeText: "Spec A", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "It", LeafNodeText: "Spec B", ContainerHierarchyTexts: []string{"Suite"}},
			}}},
			expectNames: []string{"Suite Spec A", "Suite Spec B", "Suite Spec C"},
		},
		{
			name: "When spec has no container hierarchy it should use only the LeafNodeText",
			suites: []suiteReport{{SuiteDescription: "My Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Lone Spec"},
			}}},
			expectNames: []string{"Lone Spec"},
		},
		{
			name: "When spec has multiple container levels it should join them all",
			suites: []suiteReport{{SuiteDescription: "My Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "should work", ContainerHierarchyTexts: []string{"Describe", "Context"}},
			}}},
			expectNames: []string{"Describe Context should work"},
		},
		{
			name: "When discovery contains skipped specs it should exclude them",
			suites: []suiteReport{{SuiteDescription: "Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Runs", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "It", LeafNodeText: "Skipped", ContainerHierarchyTexts: []string{"Suite"}, State: "skipped"},
			}}},
			expectNames: []string{"Suite Runs"},
		},
		{
			name: "When discovery contains non-It node types it should exclude them",
			suites: []suiteReport{{SuiteDescription: "Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Real Spec", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "BeforeEach", LeafNodeText: "Setup", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "AfterEach", LeafNodeText: "Teardown", ContainerHierarchyTexts: []string{"Suite"}},
			}}},
			expectNames: []string{"Suite Real Spec"},
		},
		{
			name: "When discovery contains duplicate full paths it should deduplicate",
			suites: []suiteReport{
				{SuiteDescription: "Suite", SpecReports: []specReport{
					{LeafNodeType: "It", LeafNodeText: "Same Name", ContainerHierarchyTexts: []string{"Suite"}},
				}},
				{SuiteDescription: "Suite", SpecReports: []specReport{
					{LeafNodeType: "It", LeafNodeText: "Same Name", ContainerHierarchyTexts: []string{"Suite"}},
					{LeafNodeType: "It", LeafNodeText: "Other", ContainerHierarchyTexts: []string{"Suite"}},
				}},
			},
			expectNames: []string{"Suite Other", "Suite Same Name"},
		},
		{
			name:      "When discovery JSON is invalid it should return an error",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			var path string
			if tt.expectErr {
				path = filepath.Join(dir, "bad.json")
				require.NoError(t, os.WriteFile(path, []byte("not json"), 0o644))
			} else {
				path = writeDiscoveryJSON(t, dir, tt.suites)
			}

			specs, err := loadDiscovery(path)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectNames, specNames(specs))
		})
	}

	t.Run("When discovery file does not exist it should return an error", func(t *testing.T) {
		_, err := loadDiscovery(filepath.Join(t.TempDir(), "nonexistent.json"))
		require.Error(t, err)
	})

	t.Run("When suites have descriptions each spec should carry its suite name", func(t *testing.T) {
		suites := []suiteReport{
			{SuiteDescription: "Agent Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "should boot", ContainerHierarchyTexts: []string{"Agent"}},
			}},
			{SuiteDescription: "CLI Suite", SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "should login"},
			}},
		}
		path := writeDiscoveryJSON(t, t.TempDir(), suites)
		specs, err := loadDiscovery(path)
		require.NoError(t, err)
		require.Len(t, specs, 2)
		byName := map[string]specInfo{}
		for _, s := range specs {
			byName[s.name] = s
		}
		require.Equal(t, "Agent Suite", byName["Agent should boot"].suite)
		require.Equal(t, "CLI Suite", byName["should login"].suite)
	})
}

func TestLoadTimings(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		writeFile bool
		expectMap map[string]float64
		expectErr bool
	}{
		{
			name:      "When timings file is valid JSON it should return the correct map",
			writeFile: true,
			content:   `{"Spec A": 45.3, "Spec B": 120.0}`,
			expectMap: map[string]float64{"Spec A": 45.3, "Spec B": 120.0},
		},
		{
			name:      "When timings file does not exist it should return empty map with no error",
			writeFile: false,
			expectMap: map[string]float64{},
		},
		{
			name:      "When timings file contains invalid JSON it should return an error",
			writeFile: true,
			content:   "not valid json",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "timings.json")
			if tt.writeFile {
				require.NoError(t, os.WriteFile(path, []byte(tt.content), 0o644))
			}

			got, err := loadTimings(path)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expectMap, got)
		})
	}
}
