package decommission_test

import (
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// verifySpecFilesDoNotContainDeviceID checks that spec JSON files do not contain the old device ID
// after decommission. The agent may recreate these files on startup, but they should not contain
// the old device's data since the identity was wiped.
func verifySpecFilesDoNotContainDeviceID(harness *e2e.Harness, deviceId string) {
	specFiles := []string{DesiredSpecPath, CurrentSpecPath, RollbackSpecPath}
	for _, specFile := range specFiles {
		output, err := harness.VM.RunSSH([]string{"sudo", "cat", specFile}, nil)
		if err == nil && output.Len() > 0 {
			// File exists, verify it doesn't contain the old device ID
			Expect(output.String()).NotTo(ContainSubstring(deviceId),
				"Expected %s to NOT contain old device ID %s after decommission", specFile, deviceId)
			GinkgoWriter.Printf("Verified: %s does not contain old device ID\n", specFile)
		} else {
			// File doesn't exist or is empty - this is also acceptable
			GinkgoWriter.Printf("Verified: %s does not exist or is empty\n", specFile)
		}
	}
}

// verifyCertificateDoesNotExist checks that the agent management certificate does not exist
func verifyCertificateDoesNotExist(harness *e2e.Harness) {
	_, err := harness.VM.RunSSH([]string{"test", "-f", AgentCertPath}, nil)
	Expect(err).To(HaveOccurred(), "Expected certificate file %s to NOT exist after decommission - agent needs to re-enroll", AgentCertPath)
}
