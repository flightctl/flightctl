package shared

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
)

// DistributedReporter handles test reporting across multiple GitHub Actions runners
// Test distribution is handled by focus patterns, but we use the test list for better categorization
type DistributedReporter struct {
	currentNode   int
	totalNodes    int
	assignedTests map[string]bool // Tests assigned to this node (for categorization)
	testsLoaded   bool            // Whether we've loaded the test list
}

// NewDistributedReporter creates a new distributed reporter if environment variables are set
func NewDistributedReporter() *DistributedReporter {
	currentNodeStr := os.Getenv("GINKGO_NODE")
	totalNodesStr := os.Getenv("GINKGO_TOTAL_NODES")

	if currentNodeStr == "" || totalNodesStr == "" {
		return nil // Not in distributed mode
	}

	currentNode, err := strconv.Atoi(currentNodeStr)
	if err != nil {
		return nil
	}

	totalNodes, err := strconv.Atoi(totalNodesStr)
	if err != nil {
		return nil
	}

	reporter := &DistributedReporter{
		currentNode:   currentNode,
		totalNodes:    totalNodes,
		assignedTests: make(map[string]bool),
	}

	// Load assigned tests if available
	reporter.loadAssignedTests()

	return reporter
}

// loadAssignedTests loads the list of tests assigned to this node from the environment variable
func (r *DistributedReporter) loadAssignedTests() {
	if r.testsLoaded {
		return
	}

	nodeTestsPattern := os.Getenv("FLIGHTCTL_NODE_TESTS")
	if nodeTestsPattern == "" {
		return // Not in distributed mode or pattern not available
	}

	// Split the pattern by | delimiter (similar to focus pattern)
	testNames := strings.Split(nodeTestsPattern, "|")
	for _, testName := range testNames {
		testName = strings.TrimSpace(testName)
		if testName != "" {
			r.assignedTests[testName] = true
		}
	}

	r.testsLoaded = true
}

// isAssignedToThisNode checks if a test is assigned to this node
func (r *DistributedReporter) isAssignedToThisNode(testName string) bool {
	r.loadAssignedTests() // Ensure tests are loaded
	return r.assignedTests[testName]
}

var (
	globalReporter *DistributedReporter
	setupOnce      sync.Once
)

// SetupDistributedReporting sets up the distributed reporting for a test suite
// This should be called in each suite's TestXXX function
func SetupDistributedReporting() {
	reporter := NewDistributedReporter()
	if reporter == nil {
		return // Not in distributed mode, run normally
	}

	// Store the reporter globally for the ReportAfterSuite
	globalReporter = reporter

	// Add ReportBeforeEach to skip tests not assigned to this node
	ReportBeforeEach(func(specReport types.SpecReport) {
		testName := specReport.FullText()

		// If this test is not assigned to this node, skip it
		if !reporter.isAssignedToThisNode(testName) {
			Skip(fmt.Sprintf("🔄 Test distributed to another node (assigned to different node, not node %d)", reporter.currentNode))
		}
	})

	// Add ReportAfterSuite for aggregated summary (only add once using sync.Once)
	setupOnce.Do(func() {
		ReportAfterSuite("Distributed Node Summary", func(report types.Report) {
			if globalReporter != nil {
				globalReporter.generateSummary(report)
			}
		})
	})
}

// generateSummary creates a comprehensive summary of test execution on this node
func (r *DistributedReporter) generateSummary(report types.Report) {
	var passed, failed, intentionallySkipped, distributedToOtherNodes int
	suiteBreakdown := make(map[string]map[string]int)

	for _, specReport := range report.SpecReports {
		// Extract suite name from the container hierarchy
		suiteName := "Unknown Suite"
		if len(specReport.ContainerHierarchyTexts) > 0 {
			suiteName = specReport.ContainerHierarchyTexts[0]
		}

		if suiteBreakdown[suiteName] == nil {
			suiteBreakdown[suiteName] = make(map[string]int)
		}

		switch specReport.State {
		case types.SpecStatePassed:
			passed++
			suiteBreakdown[suiteName]["passed"]++
		case types.SpecStateFailed:
			failed++
			suiteBreakdown[suiteName]["failed"]++
		case types.SpecStateSkipped:
			// Categorize based on the skip reason
			if specReport.Failure.Message != "" && strings.Contains(specReport.Failure.Message, "Test distributed to another node") {
				// This test was skipped by our ReportBeforeEach (distributed to another node)
				distributedToOtherNodes++
				suiteBreakdown[suiteName]["distributed"]++
			} else {
				// This test was skipped for other reasons (Skip() calls, pending, user filters, etc.)
				intentionallySkipped++
				suiteBreakdown[suiteName]["skipped"]++
			}
		case types.SpecStatePending:
			intentionallySkipped++
			suiteBreakdown[suiteName]["pending"]++
		}
	}

	// Generate the summary report
	fmt.Printf("\n" + strings.Repeat("=", 80) + "\n")
	fmt.Printf("📊 NODE %d/%d DISTRIBUTED E2E TEST SUMMARY\n", r.currentNode, r.totalNodes)
	fmt.Printf(strings.Repeat("=", 80) + "\n")
	fmt.Printf("✅ PASSED:                     %d tests\n", passed)
	fmt.Printf("❌ FAILED:                     %d tests\n", failed)
	fmt.Printf("⏭️ SKIPPED:                    %d tests (Skip() calls, pending tests)\n", intentionallySkipped)
	fmt.Printf("🔄 DISTRIBUTED TO OTHER NODES: %d tests (assigned to other runners)\n", distributedToOtherNodes)
	fmt.Printf("📈 TOTAL TESTS IN SUITE:       %d tests\n", len(report.SpecReports))

	actuallyRun := passed + failed + intentionallySkipped
	fmt.Printf("⚡ TESTS RUN ON THIS NODE:     %d tests\n", actuallyRun)
	fmt.Printf("⏱️  EXECUTION TIME:             %.2f seconds\n", report.RunTime.Seconds())

	// Calculate coverage percentage
	if len(report.SpecReports) > 0 {
		coverage := float64(actuallyRun) / float64(len(report.SpecReports)) * 100
		fmt.Printf("🎯 NODE COVERAGE:              %.1f%% of total tests\n", coverage)
	}

	// Per-suite breakdown
	if len(suiteBreakdown) > 1 {
		fmt.Printf("\n📋 PER-SUITE BREAKDOWN:\n")
		for suiteName, counts := range suiteBreakdown {
			total := counts["passed"] + counts["failed"] + counts["skipped"] + counts["pending"]
			if total > 0 {
				fmt.Printf("  %s:\n", suiteName)
				fmt.Printf("    ✅ %d passed, ❌ %d failed, ⏭️ %d skipped, 🔄 %d distributed\n",
					counts["passed"], counts["failed"],
					counts["skipped"]+counts["pending"], counts["distributed"])
			}
		}
	}

	// Overall result
	fmt.Printf("\n")
	if failed > 0 {
		fmt.Printf("❌ OVERALL RESULT: FAILED (%d failures)\n", failed)
	} else {
		fmt.Printf("✅ OVERALL RESULT: SUCCESS\n")
	}

	fmt.Printf(strings.Repeat("=", 80) + "\n")
	fmt.Printf("💡 This summary shows only tests that were assigned to this node.\n")
	fmt.Printf("   Other nodes are running the remaining %d tests in parallel.\n", distributedToOtherNodes)
	fmt.Printf("💡 Clear distinction: 'SKIPPED' = didn't match filters, 'DISTRIBUTED' = assigned to other nodes.\n")
	fmt.Printf(strings.Repeat("=", 80) + "\n")
}
