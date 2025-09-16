package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type TestSpec struct {
	LeafNodeText   string   `json:"LeafNodeText"`
	LeafNodeLabels []string `json:"LeafNodeLabels"`
}

type TestDiscovery struct {
	SuitePath   string     `json:"SuitePath"`
	SpecReports []TestSpec `json:"SpecReports"`
}

type SuiteInfo struct {
	Name  string
	Tests []string
}

type RunningSuite struct {
	SuiteName string
	WorkerID  string
	Process   *exec.Cmd
	StartTime time.Time
}

type Config struct {
	ReportsDir            string
	E2EDirs               []string
	Focus                 string
	LabelFilter           string
	Procs                 int
	OutputInterceptorMode string
	TotalNodes            int
	Node                  int
	WorkerTotal           int
	WorkerID              string
}

func main() {
	config := parseConfig()

	// Always use the new parallel execution logic
	runParallelExecution(config)
}

func parseConfig() Config {
	config := Config{
		ReportsDir:            getEnv("REPORTS", "reports"),
		E2EDirs:               getEnvSlice("GO_E2E_DIRS", []string{"./test/e2e/..."}),
		Focus:                 getEnv("GINKGO_FOCUS", ""),
		LabelFilter:           getEnv("GINKGO_LABEL_FILTER", ""),
		Procs:                 getEnvInt("GINKGO_PROCS", 1),
		OutputInterceptorMode: getEnv("GINKGO_OUTPUT_INTERCEPTOR_MODE", "dup"),
		TotalNodes:            getEnvInt("GINKGO_TOTAL_NODES", 1),
		Node:                  getEnvInt("GINKGO_NODE", 1),
		WorkerTotal:           getEnvInt("GINKGO_WORKER_TOTAL", 4),
		WorkerID:              getEnv("GINKGO_WORKER_ID", fmt.Sprintf("%d", getEnvInt("GINKGO_NODE", 1))),
	}

	return config
}

func runParallelExecution(config Config) {
	fmt.Printf("Manual test splitting enabled: Node %d of %d\n", config.Node, config.TotalNodes)
	fmt.Printf("Worker total: %d\n", config.WorkerTotal)

	// Discover tests
	suites, err := discoverTests(config)
	if err != nil {
		log.Fatalf("Failed to discover tests: %v", err)
	}

	// Filter tests for this node
	nodeSuites := filterTestsForNode(suites, config.Node, config.TotalNodes)
	if len(nodeSuites) == 0 {
		fmt.Printf("No tests assigned to node %d. Skipping execution.\n", config.Node)
		os.Exit(0)
	}

	fmt.Printf("Node %d will run %d suites\n", config.Node, len(nodeSuites))

	// Run startup
	if err := runStartup(); err != nil {
		log.Fatalf("Startup failed: %v", err)
	}

	// Run suites with sliding window
	exitCode := runSuitesWithSlidingWindow(nodeSuites, config)

	// Run cleanup
	if err := runCleanup(); err != nil {
		fmt.Printf("⚠️  Cleanup failed: %v\n", err)
	}

	// Merge reports
	if err := mergeReports(nodeSuites, config.ReportsDir); err != nil {
		fmt.Printf("⚠️  Report merging failed: %v\n", err)
	}

	os.Exit(exitCode)
}

func discoverTests(config Config) (map[string]SuiteInfo, error) {
	fmt.Println("Generating list of all tests...")

	// First, try to install ginkgo if it's not available
	ginkgoPath := "ginkgo"
	if err := exec.Command("ginkgo", "version").Run(); err != nil {
		fmt.Println("Installing ginkgo...")
		installCmd := exec.Command("go", "install", "github.com/onsi/ginkgo/v2/ginkgo")
		installCmd.Stdout = os.Stdout
		installCmd.Stderr = os.Stderr
		if err := installCmd.Run(); err != nil {
			return nil, fmt.Errorf("failed to install ginkgo: %v", err)
		}

		// Try to find ginkgo in GOBIN or GOPATH
		if gobin := os.Getenv("GOBIN"); gobin != "" {
			ginkgoPath = filepath.Join(gobin, "ginkgo")
		} else if gopath := os.Getenv("GOPATH"); gopath != "" {
			ginkgoPath = filepath.Join(gopath, "bin", "ginkgo")
		} else {
			// Try common locations
			homeDir, _ := os.UserHomeDir()
			ginkgoPath = filepath.Join(homeDir, "go", "bin", "ginkgo")
		}

		// Check if ginkgo exists at the found path
		if _, err := os.Stat(ginkgoPath); os.IsNotExist(err) {
			return nil, fmt.Errorf("ginkgo not found after installation at %s", ginkgoPath)
		}
	}

	// Build discovery command
	cmd := exec.Command(ginkgoPath, "run", "--dry-run", "--json-report", "discovery.json")
	if config.Focus != "" {
		cmd.Args = append(cmd.Args, "--focus", config.Focus)
	}
	if config.LabelFilter != "" {
		cmd.Args = append(cmd.Args, "--label-filter", config.LabelFilter)
	}
	cmd.Args = append(cmd.Args, config.E2EDirs...)

	fmt.Printf("Running discovery command: %s\n", strings.Join(cmd.Args, " "))

	// Run discovery (suppress output to avoid cluttering logs)
	cmd.Stdout = os.Stderr // Redirect to stderr to see any errors
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ginkgo discovery failed: %v", err)
	}

	// Parse JSON report
	file, err := os.Open("discovery.json")
	if err != nil {
		return nil, fmt.Errorf("failed to open discovery.json: %v", err)
	}
	defer file.Close()
	defer os.Remove("discovery.json")

	var discoveries []TestDiscovery
	if err := json.NewDecoder(file).Decode(&discoveries); err != nil {
		return nil, fmt.Errorf("failed to parse discovery.json: %v", err)
	}

	// Group tests by suite
	suites := make(map[string]SuiteInfo)
	for _, discovery := range discoveries {
		// Get suite name from SuitePath
		suiteName := filepath.Base(discovery.SuitePath)
		if suiteName == "." {
			// Handle case where SuitePath might be empty or just "."
			suiteName = "unknown"
		}

		var suiteTests []string
		for _, spec := range discovery.SpecReports {
			// Filter by sanity label
			hasSanity := false
			for _, label := range spec.LeafNodeLabels {
				if label == "sanity" {
					hasSanity = true
					break
				}
			}
			if !hasSanity {
				continue
			}

			suiteTests = append(suiteTests, spec.LeafNodeText)
		}

		if len(suiteTests) > 0 {
			// Sort tests within each suite for stability
			sort.Strings(suiteTests)
			suites[suiteName] = SuiteInfo{
				Name:  suiteName,
				Tests: suiteTests,
			}
		}
	}

	fmt.Printf("Total suites found: %d\n", len(suites))

	// Sort suite names for stable output
	var suiteNames []string
	for suiteName := range suites {
		suiteNames = append(suiteNames, suiteName)
	}
	sort.Strings(suiteNames)

	for _, suiteName := range suiteNames {
		suite := suites[suiteName]
		fmt.Printf("  Suite %s: %d tests\n", suiteName, len(suite.Tests))
	}
	return suites, nil
}

func filterTestsForNode(suites map[string]SuiteInfo, node, totalNodes int) []SuiteInfo {
	var allTests []string
	suiteMap := make(map[string]string) // test -> suite

	for suiteName, suite := range suites {
		for _, test := range suite.Tests {
			allTests = append(allTests, test)
			suiteMap[test] = suiteName
		}
	}

	// Filter tests for this node using modulo
	var nodeTests []string
	for i, test := range allTests {
		if i%totalNodes == node-1 {
			nodeTests = append(nodeTests, test)
		}
	}

	fmt.Printf("All tests: %d, Node %d tests: %d\n", len(allTests), node, len(nodeTests))

	// Group filtered tests by suite
	nodeSuites := make(map[string]SuiteInfo)
	for _, test := range nodeTests {
		suiteName := suiteMap[test]
		if suite, exists := nodeSuites[suiteName]; exists {
			suite.Tests = append(suite.Tests, test)
			nodeSuites[suiteName] = suite
		} else {
			nodeSuites[suiteName] = SuiteInfo{
				Name:  suiteName,
				Tests: []string{test},
			}
		}
	}

	// Convert to slice and sort for stability
	var result []SuiteInfo
	for _, suite := range nodeSuites {
		result = append(result, suite)
	}

	// Sort suites by name for stable ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})

	fmt.Printf("Tests for node %d: %d\n", node, len(nodeTests))
	return result
}

func runSuitesWithSlidingWindow(suites []SuiteInfo, config Config) int {
	fmt.Printf("Running test suites in parallel (max %d concurrent, sliding window)...\n", config.WorkerTotal)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var overallExitCode int

	// VM pool: available worker IDs (1 to WorkerTotal)
	vmPool := make(chan int, config.WorkerTotal)
	for i := 1; i <= config.WorkerTotal; i++ {
		vmPool <- i
	}

	// Channel to control concurrency
	semaphore := make(chan struct{}, config.WorkerTotal)

	for _, suite := range suites {
		wg.Add(1)
		go func(suite SuiteInfo) {
			defer wg.Done()

			// Acquire semaphore
			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			// Get VM from pool (reuse worker IDs)
			vmID := <-vmPool

			// Build focus pattern
			focusPattern := strings.Join(suite.Tests, "|")
			// Escape regex metacharacters
			focusPattern = escapeRegex(focusPattern)

			fmt.Printf("Starting suite %s with worker: %d (VM %d)\n", suite.Name, vmID, vmID)

			// Run suite
			exitCode := runSuite(suite.Name, focusPattern, vmID, config)

			// Return VM to pool
			vmPool <- vmID

			mu.Lock()
			if exitCode != 0 {
				overallExitCode = 1
			}
			mu.Unlock()

			fmt.Printf("Suite %s completed with exit code: %d (VM %d returned to pool)\n", suite.Name, exitCode, vmID)
		}(suite)
	}

	wg.Wait()

	if overallExitCode != 0 {
		fmt.Println("❌ Some test suites failed")
	} else {
		fmt.Println("✅ All test suites completed successfully")
	}

	return overallExitCode
}

func runSuite(suiteName, focusPattern string, workerNum int, config Config) int {
	// Find ginkgo path (same logic as in discoverTests)
	var ginkgoPath string
	if gobin := os.Getenv("GOBIN"); gobin != "" {
		ginkgoPath = filepath.Join(gobin, "ginkgo")
	} else if gopath := os.Getenv("GOPATH"); gopath != "" {
		ginkgoPath = filepath.Join(gopath, "bin", "ginkgo")
	} else {
		homeDir, _ := os.UserHomeDir()
		ginkgoPath = filepath.Join(homeDir, "go", "bin", "ginkgo")
	}

	// Fallback to "ginkgo" if not found in expected locations
	if ginkgoPath == "" {
		ginkgoPath = "ginkgo"
	}

	// Build ginkgo command
	cmd := exec.Command(ginkgoPath, "run",
		"--focus", focusPattern,
		"--label-filter", config.LabelFilter,
		"--timeout", "120m",
		"--race",
		"-vv",
		"-nodes", strconv.Itoa(config.Procs),
		"--show-node-events",
		"--trace",
		"--force-newlines",
		"--output-interceptor-mode", config.OutputInterceptorMode,
		"--github-output",
		"--output-dir", config.ReportsDir,
		"--junit-report", fmt.Sprintf("junit_%s.xml", suiteName),
		fmt.Sprintf("test/e2e/%s", suiteName))

	// Set environment variables
	cmd.Env = append(os.Environ(),
		"GINKGO_WORKER_NUM="+strconv.Itoa(workerNum),
	)

	// Redirect stdout and stderr to our own
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run command
	if err := cmd.Run(); err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			return exitError.ExitCode()
		}
		return 1
	}

	return 0
}

func runStartup() error {
	fmt.Println("🔄 [Test Execution] Step 1: Running startup...")
	cmd := exec.Command("test/scripts/e2e_startup.sh")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("startup failed: %v", err)
	}
	fmt.Println("✅ [Test Execution] Startup completed successfully")
	return nil
}

func runCleanup() error {
	fmt.Println("🔄 [Test Execution] Step 3: Running cleanup...")
	cmd := exec.Command("test/scripts/e2e_cleanup.sh")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cleanup failed: %v", err)
	}
	fmt.Println("✅ [Test Execution] Cleanup completed")
	return nil
}

func mergeReports(suites []SuiteInfo, reportsDir string) error {
	fmt.Println("Merging JUnit reports...")

	mergedReport := filepath.Join(reportsDir, "junit_e2e_test.xml")
	file, err := os.Create(mergedReport)
	if err != nil {
		return fmt.Errorf("failed to create merged report: %v", err)
	}
	defer file.Close()

	// Write header
	fmt.Fprintln(file, `<?xml version="1.0" encoding="UTF-8"?>`)
	fmt.Fprintln(file, `<testsuites>`)

	// Append each suite's report
	for _, suite := range suites {
		suiteReport := filepath.Join(reportsDir, fmt.Sprintf("junit_%s.xml", suite.Name))
		if _, err := os.Stat(suiteReport); err == nil {
			suiteFile, err := os.Open(suiteReport)
			if err != nil {
				continue
			}

			scanner := bufio.NewScanner(suiteFile)
			inTestsuite := false
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(line, "<testsuite") {
					inTestsuite = true
				}
				if inTestsuite {
					fmt.Fprintln(file, line)
				}
				if strings.Contains(line, "</testsuite>") {
					inTestsuite = false
				}
			}
			suiteFile.Close()
		}
	}

	// Write footer
	fmt.Fprintln(file, `</testsuites>`)

	fmt.Printf("✅ Merged report saved to: %s\n", mergedReport)
	return nil
}

func escapeRegex(pattern string) string {
	// Escape regex metacharacters
	replacer := strings.NewReplacer(
		"[", "\\[",
		".", "\\.",
		"*", "\\*",
		"^", "\\^",
		"$", "\\$",
		"(", "\\(",
		")", "\\)",
		"+", "\\+",
		"?", "\\?",
		"{", "\\{",
		"|", "\\|",
		"\\", "\\\\",
	)
	return replacer.Replace(pattern)
}

// Helper functions
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getEnvInt(key string, defaultValue int) int {
	if value := os.Getenv(key); value != "" {
		if intValue, err := strconv.Atoi(value); err == nil {
			return intValue
		}
	}
	return defaultValue
}

func getEnvSlice(key string, defaultValue []string) []string {
	if value := os.Getenv(key); value != "" {
		return strings.Split(value, " ")
	}
	return defaultValue
}
