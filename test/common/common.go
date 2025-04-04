package common

import (
	"fmt"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/gomega"
)

// RunGetDevices executes "get devices" CLI command with optional arguments.
// Verifies if the expected error condition is met and checks if the output contains a given substring.
func RunGetDevices(harness *e2e.Harness, expectError bool, expectedSubstring string, args ...string) (string, error) {
	baseArgs := []string{"get", "devices"}
	args = append(baseArgs, args...)
	out, err := harness.CLI(args...)

	if expectError {
		Expect(err).To(HaveOccurred())
	} else {
		Expect(err).ToNot(HaveOccurred())
	}

	if expectedSubstring != "" {
		Expect(out).To(ContainSubstring(expectedSubstring))
	}

	return out, err
}

// ManageResource performs an operation ("apply" or "delete") on a specified resource.
func ManageResource(harness *e2e.Harness, operation, resource string, args ...string) (string, error) {
	switch operation {
	case "apply":
		return harness.CLI("apply", "-f", GetTestExamplesYamlPath(resource))
	case "delete":
		return harness.CLI("delete", resource)
	default:
		return "", fmt.Errorf("unsupported operation: %s", operation)
	}
}

// GenerateTimestamps returns start of current and next year in RFC3339 format.
func GenerateTimestamps() (string, string) {
	now := time.Now()
	startOfYear := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, time.UTC)
	endOfYear := time.Date(now.Year()+1, 1, 1, 0, 0, 0, 0, time.UTC)

	return startOfYear.Format(time.RFC3339), endOfYear.Format(time.RFC3339)
}

// CheckRunningContainers verifies the expected number of running containers on the VM.
func CheckRunningContainers(h *e2e.Harness, expectedCount string) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "|", "grep", "Up", "|", "wc", "-l"}, nil)
	Expect(err).ToNot(HaveOccurred())
	Expect(stdout.String()).To(ContainSubstring(expectedCount))
}

// ConditionExists checks if a specific condition exists for the device with the given type, status, and reason.
func ConditionExists(device *v1alpha1.Device, conditionType, conditionStatus, conditionReason string) bool {
	for _, condition := range device.Status.Conditions {
		if string(condition.Type) == conditionType &&
			condition.Reason == conditionReason &&
			string(condition.Status) == conditionStatus {
			return true
		}
	}
	return false
}
