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
	SpecReports      []SpecReport `json:"SpecReports"`
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
			specs = append(specs, SpecInfo{Name: fullName, Suite: suite.SuiteDescription})
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}
