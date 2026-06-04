// Package e2etestutils provides shared logic used by update_test_timings,
// compute_test_assignments, and analyze_e2e_health.
//
// All three tools must stay in sync on:
//   - the test-timings.json file format (SpecTiming)
//   - the per-spec/BeforeSuite observation pipeline (ParseTimingsFromFile)
//   - the LPT bin-packing algorithm (LPTAssign)
//
// Keeping the implementations here ensures a single source of truth.
package e2etestutils

import (
	"encoding/xml"
	"fmt"
	"math"
	"os"
	"strings"
)

// SuiteOverheadPrefix is the key prefix used in the timings cache for
// per-suite BeforeSuite durations, e.g. "__suite__:Agent E2E Suite".
const SuiteOverheadPrefix = "__suite__:"

// SpecTiming holds the aggregate timing data for a single spec (or suite
// BeforeSuite overhead) across all observed CI runs.
// This is the canonical entry type written to test-timings.json.
type SpecTiming struct {
	Avg    float64 `json:"avg"`
	StdDev float64 `json:"stddev"`
}

// JUnitTestCase holds the fields we need from each <testcase> element.
// Exported so that tests in consuming packages can construct fixture data.
type JUnitTestCase struct {
	Name      string     `xml:"name,attr"`
	ClassName string     `xml:"classname,attr"`
	Time      float64    `xml:"time,attr"`
	Skipped   []struct{} `xml:"skipped"`
}

// junitTestSuites is the root element of Ginkgo's JUnit XML output.
type junitTestSuites struct {
	XMLName    xml.Name         `xml:"testsuites"`
	TestSuites []junitTestSuite `xml:"testsuite"`
}

type junitTestSuite struct {
	TestCases []JUnitTestCase `xml:"testcase"`
}

// JUnitSpecName extracts the plain spec name from a Ginkgo JUnit testcase name.
//
// Ginkgo formats names as:
//
//	[It] Container1 Container2 LeafText [label1, label2, ...]
//
// We strip the "[It] " prefix and the trailing " [labels]" group so that the
// resulting key matches what compute_test_assignments produces from the Ginkgo
// discovery JSON (ContainerHierarchyTexts joined with LeafNodeText).
// Non-It entries (e.g. [BeforeSuite]) return "".
func JUnitSpecName(name string) string {
	const itPrefix = "[It] "
	if !strings.HasPrefix(name, itPrefix) {
		return ""
	}
	name = strings.TrimPrefix(name, itPrefix)
	if idx := strings.LastIndex(name, " ["); idx > 0 && strings.HasSuffix(name, "]") {
		name = name[:idx]
	}
	return name
}

// ParseTimingsFromFile reads a Ginkgo JUnit XML file and returns a map of
// spec name (or "__suite__:ClassName" for BeforeSuite) → observed durations.
// Skipped specs and entries with non-positive durations are excluded.
//
// This is the canonical parsing function; all tools must call this rather
// than re-implementing the extraction logic.
func ParseTimingsFromFile(path string) (map[string][]float64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var suites junitTestSuites
	if err := xml.Unmarshal(data, &suites); err != nil {
		return nil, fmt.Errorf("parse xml %s: %w", path, err)
	}

	result := make(map[string][]float64)
	for i := range suites.TestSuites {
		for _, tc := range suites.TestSuites[i].TestCases {
			if len(tc.Skipped) > 0 || tc.Time <= 0 {
				continue
			}
			if tc.Name == "[BeforeSuite]" && tc.ClassName != "" {
				key := SuiteOverheadPrefix + tc.ClassName
				result[key] = append(result[key], tc.Time)
				continue
			}
			key := JUnitSpecName(tc.Name)
			if key == "" {
				continue
			}
			result[key] = append(result[key], tc.Time)
		}
	}
	return result, nil
}

// PopulationStdDev computes the population standard deviation of values around
// the given mean. Returns 0 when fewer than 2 values are present.
func PopulationStdDev(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var variance float64
	for _, v := range values {
		d := v - mean
		variance += d * d
	}
	return math.Sqrt(variance / float64(len(values)))
}
