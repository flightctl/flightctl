package decommission_test

import (
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

// checkSpecFilesDoNotContainDeviceID checks that spec JSON files do not contain the old device ID
// after decommission. Returns a list of files that still contain the device ID (empty means success).
func checkSpecFilesDoNotContainDeviceID(harness *e2e.Harness, deviceId string) ([]string, error) {
	if harness == nil || harness.VM == nil {
		return nil, fmt.Errorf("harness or VM is nil")
	}
	if deviceId == "" {
		return nil, fmt.Errorf("device ID is empty")
	}

	specFiles := []string{DesiredSpecPath, CurrentSpecPath, RollbackSpecPath}
	var staleFiles []string

	for _, specFile := range specFiles {
		output, err := harness.VM.RunSSH([]string{"sudo", "cat", specFile}, nil)
		if err != nil || output.Len() == 0 {
			GinkgoWriter.Printf("Verified: %s does not exist or is empty\n", specFile)
			continue
		}
		if strings.Contains(output.String(), deviceId) {
			GinkgoWriter.Printf("WARNING: %s still contains old device ID %s\n", specFile, deviceId)
			staleFiles = append(staleFiles, specFile)
		} else {
			GinkgoWriter.Printf("Verified: %s does not contain old device ID\n", specFile)
		}
	}
	return staleFiles, nil
}

// checkCertificateDoesNotExist checks that the agent management certificate does not exist.
// Returns true if the certificate is absent (expected after decommission), false if it still exists.
func checkCertificateDoesNotExist(harness *e2e.Harness) (bool, error) {
	if harness == nil || harness.VM == nil {
		return false, fmt.Errorf("harness or VM is nil")
	}
	_, err := harness.VM.RunSSH([]string{"test", "-f", AgentCertPath}, nil)
	if err != nil {
		GinkgoWriter.Printf("Verified: certificate file %s does not exist\n", AgentCertPath)
		return true, nil
	}
	GinkgoWriter.Printf("WARNING: certificate file %s still exists after decommission\n", AgentCertPath)
	return false, nil
}

// cleanupDeviceAndER deletes a device and its enrollment request, logging warnings on failure.
func cleanupDeviceAndER(harness *e2e.Harness, deviceId string) {
	if deviceId == "" {
		GinkgoWriter.Println("Warning: skipping cleanup, empty device ID")
		return
	}
	if _, err := harness.CleanUpResource(testutil.Device, deviceId); err != nil {
		GinkgoWriter.Printf("Warning: failed to delete device %s: %v\n", deviceId, err)
	}
	if _, err := harness.CleanUpResource(testutil.EnrollmentRequest, deviceId); err != nil {
		GinkgoWriter.Printf("Warning: failed to delete enrollment request %s: %v\n", deviceId, err)
	}
}
