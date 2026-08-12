package e2etestutils

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// CodeLocation is the subset of a Ginkgo CodeLocation we need for discovery.
type CodeLocation struct {
	FileName   string `json:"FileName"`
	LineNumber int    `json:"LineNumber"`
}

// SpecReport is the subset of a Ginkgo SpecReport we care about.
// Exported so test fixtures in consuming packages can construct discovery data.
type SpecReport struct {
	LeafNodeType                string         `json:"LeafNodeType"`
	LeafNodeText                string         `json:"LeafNodeText"`
	ContainerHierarchyTexts     []string       `json:"ContainerHierarchyTexts"`
	ContainerHierarchyLocations []CodeLocation `json:"ContainerHierarchyLocations"`
	IsInOrderedContainer        bool           `json:"IsInOrderedContainer"`
	State                       string         `json:"State"`
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
	sourceCache := make(map[string][]string)
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
			spec := SpecInfo{
				Name:  fullName,
				Suite: suite.SuiteDescription,
			}
			if isOrderedSpec(sr, sourceCache) {
				spec.OrderedGroup = suite.SuiteDescription
			}
			specs = append(specs, spec)
		}
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })
	return specs, nil
}

func isOrderedSpec(sr SpecReport, sourceCache map[string][]string) bool {
	if sr.IsInOrderedContainer {
		return true
	}
	if len(sr.ContainerHierarchyLocations) != 1 {
		return false
	}
	location := sr.ContainerHierarchyLocations[0]
	return sourceLocationContainsOrdered(location, sourceCache)
}

func sourceLocationContainsOrdered(location CodeLocation, sourceCache map[string][]string) bool {
	if location.FileName == "" || location.LineNumber < 1 {
		return false
	}
	lines, ok := sourceCache[location.FileName]
	if !ok {
		data, err := os.ReadFile(location.FileName)
		if err != nil {
			sourceCache[location.FileName] = nil
			return false
		}
		lines = strings.Split(string(data), "\n")
		sourceCache[location.FileName] = lines
	}
	if len(lines) == 0 || location.LineNumber > len(lines) {
		return false
	}
	start := location.LineNumber - 1
	end := min(start+5, len(lines))
	for _, line := range lines[start:end] {
		if strings.Contains(line, "Ordered") {
			return true
		}
	}
	return false
}
