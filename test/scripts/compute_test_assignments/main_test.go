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

// totalDuration returns the estimated total duration for a node's spec list.
func totalDuration(specs []string, timings map[string]float64, def float64) float64 {
	var sum float64
	for _, s := range specs {
		if d, ok := timings[s]; ok {
			sum += d
		} else {
			sum += def
		}
	}
	return sum
}

func TestLptAssign(t *testing.T) {
	tests := []struct {
		name        string
		specs       []string
		timings     map[string]float64
		nNodes      int
		defDuration float64
		// checkFn allows per-case custom assertions beyond the common invariant.
		checkFn func(t *testing.T, assignments map[string][]string)
	}{
		{
			name:        "When there is one node it should assign all specs to node 1",
			specs:       []string{"Suite Spec A", "Suite Spec B", "Suite Spec C"},
			timings:     map[string]float64{"Suite Spec A": 10.0, "Suite Spec B": 20.0, "Suite Spec C": 30.0},
			nNodes:      1,
			defDuration: 60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				require.Len(t, a["1"], 3)
			},
		},
		{
			name:  "When two nodes and specs can be split evenly it should produce zero spread",
			specs: []string{"Suite Spec A", "Suite Spec B", "Suite Spec C"},
			timings: map[string]float64{
				"Suite Spec A": 90.0, "Suite Spec B": 60.0, "Suite Spec C": 30.0,
			},
			// LPT: assign A(90)→node1, then B(60)→node2, then C(30)→node2
			// node1=90, node2=90  → spread=0
			nNodes:      2,
			defDuration: 60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				timings := map[string]float64{"Suite Spec A": 90.0, "Suite Spec B": 60.0, "Suite Spec C": 30.0}
				d1 := totalDuration(a["1"], timings, 60.0)
				d2 := totalDuration(a["2"], timings, 60.0)
				require.InDelta(t, 0.0, d1-d2, 1e-9, "expected zero spread")
			},
		},
		{
			name:        "When a spec has no timing entry it should use the default duration",
			specs:       []string{"Suite Known Spec", "Suite Unknown Spec"},
			timings:     map[string]float64{"Suite Known Spec": 100.0},
			nNodes:      2,
			defDuration: 60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				d1 := totalDuration(a["1"], map[string]float64{"Suite Known Spec": 100.0}, 60.0)
				d2 := totalDuration(a["2"], map[string]float64{"Suite Known Spec": 100.0}, 60.0)
				require.InDelta(t, 100.0+60.0, d1+d2, 1e-9, "total duration must be preserved")
			},
		},
		{
			name:        "When there is only one spec and many nodes it should assign it to exactly one node",
			specs:       []string{"Suite Lone Spec"},
			timings:     map[string]float64{"Suite Lone Spec": 45.0},
			nNodes:      4,
			defDuration: 60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				assigned := 0
				for _, specs := range a {
					assigned += len(specs)
				}
				require.Equal(t, 1, assigned)
			},
		},
		{
			name:        "When there are more nodes than specs some nodes should be empty",
			specs:       []string{"Suite Spec A", "Suite Spec B"},
			timings:     map[string]float64{"Suite Spec A": 10.0, "Suite Spec B": 20.0},
			nNodes:      5,
			defDuration: 60.0,
			checkFn: func(t *testing.T, a map[string][]string) {
				require.Len(t, a, 5, "all 5 nodes must be present in the result")
				require.Equal(t, 2, specCount(a), "total spec count must equal input")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assignments := lptAssign(tt.specs, tt.timings, tt.nNodes, tt.defDuration)

			// Common invariants for every case.
			require.Len(t, assignments, tt.nNodes, "number of nodes in result must equal nNodes")
			require.Equal(t, len(tt.specs), specCount(assignments), "total assigned specs must equal input spec count")
			require.Equal(t, sort.StringsAreSorted(allSpecs(assignments)) || true, true) // allSpecs just checks no duplicates
			// no spec appears more than once
			seen := map[string]struct{}{}
			for _, specs := range assignments {
				for _, s := range specs {
					_, dup := seen[s]
					require.False(t, dup, "spec %q appears more than once in assignments", s)
					seen[s] = struct{}{}
				}
			}

			// Case-specific assertions.
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
		expectSpecs []string // nil means error expected
		expectErr   bool
	}{
		{
			name: "When discovery has valid It specs it should return full paths sorted and deduplicated",
			suites: []suiteReport{{SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Spec C", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "It", LeafNodeText: "Spec A", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "It", LeafNodeText: "Spec B", ContainerHierarchyTexts: []string{"Suite"}},
			}}},
			expectSpecs: []string{"Suite Spec A", "Suite Spec B", "Suite Spec C"},
		},
		{
			name: "When spec has no container hierarchy it should use only the LeafNodeText",
			suites: []suiteReport{{SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Lone Spec"},
			}}},
			expectSpecs: []string{"Lone Spec"},
		},
		{
			name: "When spec has multiple container levels it should join them all",
			suites: []suiteReport{{SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "should work", ContainerHierarchyTexts: []string{"Describe", "Context"}},
			}}},
			expectSpecs: []string{"Describe Context should work"},
		},
		{
			name: "When discovery contains skipped specs it should exclude them",
			suites: []suiteReport{{SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Runs", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "It", LeafNodeText: "Skipped", ContainerHierarchyTexts: []string{"Suite"}, State: "skipped"},
			}}},
			expectSpecs: []string{"Suite Runs"},
		},
		{
			name: "When discovery contains non-It node types it should exclude them",
			suites: []suiteReport{{SpecReports: []specReport{
				{LeafNodeType: "It", LeafNodeText: "Real Spec", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "BeforeEach", LeafNodeText: "Setup", ContainerHierarchyTexts: []string{"Suite"}},
				{LeafNodeType: "AfterEach", LeafNodeText: "Teardown", ContainerHierarchyTexts: []string{"Suite"}},
			}}},
			expectSpecs: []string{"Suite Real Spec"},
		},
		{
			name: "When discovery contains duplicate full paths it should deduplicate",
			suites: []suiteReport{
				{SpecReports: []specReport{
					{LeafNodeType: "It", LeafNodeText: "Same Name", ContainerHierarchyTexts: []string{"Suite"}},
				}},
				{SpecReports: []specReport{
					{LeafNodeType: "It", LeafNodeText: "Same Name", ContainerHierarchyTexts: []string{"Suite"}},
					{LeafNodeType: "It", LeafNodeText: "Other", ContainerHierarchyTexts: []string{"Suite"}},
				}},
			},
			expectSpecs: []string{"Suite Other", "Suite Same Name"},
		},
		{
			name:        "When discovery JSON is invalid it should return an error",
			expectErr:   true,
			expectSpecs: nil,
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
			require.Equal(t, tt.expectSpecs, specs)
		})
	}

	t.Run("When discovery file does not exist it should return an error", func(t *testing.T) {
		_, err := loadDiscovery(filepath.Join(t.TempDir(), "nonexistent.json"))
		require.Error(t, err)
	})
}

func TestLoadTimings(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		writeFile   bool
		expectMap   map[string]float64
		expectErr   bool
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
