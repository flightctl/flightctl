# E2E Tests

This directory contains end-to-end tests for Flight Control.

## Prerequisites

### System Requirements

- **Libvirt**: Must be installed and running
- **QEMU/KVM**: Must be available for VM creation
- **User Permissions**: Your user must be in the `libvirt` group

### Setup

The e2e tests require a base disk image to be available in the correct location for libvirt access. The setup is handled automatically by the Makefile, but you can also run it manually:

```bash
# Automatic setup (recommended)
make run-e2e-test GO_E2E_DIRS=test/e2e/agent

# Manual setup
./test/scripts/setup_e2e_environment.sh
```

### Base Disk Location

The base disk is automatically copied from the project directory to `~/.local/share/libvirt/images/base-disk.qcow2` to ensure libvirt can access it properly. This is necessary because:

1. **Security**: Libvirt/QEMU has restrictions on accessing files in user home directories
2. **Permissions**: The standard libvirt images directory is accessible to the hypervisor
3. **Compatibility**: This location works across different libvirt configurations

## Running Tests

### Basic Usage

Run all e2e tests:
```bash
make run-e2e-test
```

Run specific test suites:
```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/agent
make run-e2e-test GO_E2E_DIRS=test/e2e/cli
```

### Parallel Execution

Run tests with multiple parallel processes:
```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/agent GINKGO_PROCS=4
```

### Filtering Tests

Filter tests by labels:
```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/agent GINKGO_LABEL_FILTER="81864"
```

## Test Reporting and Logging

### New Ginkgo Reporting Structure

The e2e tests now use an improved Ginkgo command structure that provides better logging and debugging capabilities:

**Key Flags Used:**
- `--output-dir "${REPORTS}"` - Consolidates all reports in one directory
- `--junit-report junit_e2e_test.xml` - Generates aggregated JUnit report
- `--keep-separate-reports` - Preserves individual worker reports as JSON files

- **Aggregated JUnit Report**: `reports/junit_e2e_test.xml` - Contains the overall test results
- **Individual Worker Reports**: `reports/report.e2e_suite_node*.json` - Detailed logs from each parallel worker (when using `--keep-separate-reports`)
- **Console Output**: Verbose output showing all worker activity

### Logging Best Practices

For reliable logging in parallel execution, use `GinkgoWriter` instead of `fmt.Printf` or `logrus`:

```go
import . "github.com/onsi/ginkgo/v2"

// ✅ Good - will appear in worker reports
GinkgoWriter.Printf("Worker %d: Processing test\n", workerID)

// ❌ Avoid - may not appear in parallel execution
fmt.Printf("Worker %d: Processing test\n", workerID)
logrus.Infof("Worker %d: Processing test\n", workerID)
```

### What You Get

After running tests, check the `reports/` directory for:

1. **junit_e2e_test.xml** - Aggregated test results
2. **report.e2e_suite_node1.json** - Detailed logs from worker 1
3. **report.e2e_suite_node2.json** - Detailed logs from worker 2
4. **...** - Additional worker reports for each parallel process

The individual worker reports contain:
- Complete stdout/stderr from each spec
- Non-truncated log output
- Worker-specific context and timing information

### Debugging Failed Tests

When tests fail in parallel execution:

1. Check the aggregated JUnit report for overall failure summary
2. Look at the specific worker report files for detailed logs
3. Use `GinkgoWriter.Printf()` statements in your tests for reliable logging
4. The `--keep-separate-reports` flag ensures individual worker logs are preserved

## Test Structure

Each test suite follows this pattern:

```go
var _ = BeforeSuite(func() {
    e2e.RegisterVMPoolCleanup()
    suiteCtx = testutil.InitSuiteTracerForGinkgo("Suite Name")
    workerID = GinkgoParallelProcess()
    
    GinkgoWriter.Printf("🔄 [BeforeSuite] Worker %d: Starting setup\n", workerID)
    // ... setup code ...
    GinkgoWriter.Printf("✅ [BeforeSuite] Worker %d: Setup completed\n", workerID)
})

var _ = BeforeEach(func() {
    GinkgoWriter.Printf("🔄 [BeforeEach] Worker %d: Test setup\n", workerID)
    // ... test setup ...
    GinkgoWriter.Printf("✅ [BeforeEach] Worker %d: Test setup completed\n", workerID)
})

var _ = AfterEach(func() {
    GinkgoWriter.Printf("🔄 [AfterEach] Worker %d: Test cleanup\n", workerID)
    // ... test cleanup ...
    GinkgoWriter.Printf("✅ [AfterEach] Worker %d: Test cleanup completed\n", workerID)
})
```

## Environment Variables

- `GINKGO_PROCS`: Number of parallel processes (default: 1)
- `GINKGO_OUTPUT_INTERCEPTOR_MODE`: Output mode for parallel execution
  - `dup`: Show output from all workers (default)
  - `swap`: Clean output, only show current worker
- `GINKGO_LABEL_FILTER`: Filter tests by labels
- `VERBOSE`: Enable verbose output 