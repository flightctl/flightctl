package e2etestutils

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SpecReport is the subset of a Ginkgo SpecReport we care about.
// Exported so test fixtures in consuming packages can construct discovery data.
type SpecReport struct {
	LeafNodeType            string   `json:"LeafNodeType"`
	LeafNodeText            string   `json:"LeafNodeText"`
	ContainerHierarchyTexts []string `json:"ContainerHierarchyTexts"`
	State                   string   `json:"State"`
}

// SuiteReport is the top-level array element in a Ginkgo JSON report.
// Exported so test fixtures in consuming packages can construct discovery data.
type SuiteReport struct {
	SuiteDescription string       `json:"SuiteDescription"`
	SuitePath        string       `json:"SuitePath"`
	SpecReports      []SpecReport `json:"SpecReports"`
}

// suiteDirMarker is the path segment every e2e suite directory lives under.
// Used to turn Ginkgo's absolute SuitePath into a repo-relative directory that
// is valid regardless of the checkout root on a given CI runner.
const suiteDirMarker = "test/e2e/"

// relSuiteDir converts an absolute Ginkgo SuitePath into a repo-relative
// directory (e.g. "test/e2e/agent"). Returns "" if the marker is not found.
func relSuiteDir(absPath string) string {
	idx := strings.Index(absPath, suiteDirMarker)
	if idx < 0 {
		return ""
	}
	return absPath[idx:]
}

// LoadDiscovery parses a Ginkgo dry-run JSON report and returns the list of
// non-skipped It specs, each annotated with its suite name.
// Duplicate spec names are deduplicated; the result is sorted by name.
func LoadDiscovery(path string) ([]SpecInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read discovery file: %w", err)
	}
	var suites []SuiteReport
	if err := json.Unmarshal(data, &suites); err != nil {
		return nil, fmt.Errorf("parse discovery file: %w", err)
	}

	seen := make(map[string]struct{})
	var specs []SpecInfo
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
			specs = append(specs, SpecInfo{Name: fullName, Suite: suite.SuiteDescription, Path: relSuiteDir(suite.SuitePath)})
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}
