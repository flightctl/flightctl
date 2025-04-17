// suite_test.go
package ansible_test

import (
	"os/exec"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAnsibleE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ansible E2E Suite")
}

var _ = BeforeSuite(func() {
	cmd := exec.Command("ansible-galaxy", "collection", "install", "flightctl.core")
	err := cmd.Run()
	Expect(err).NotTo(HaveOccurred(), "Failed to install flightctl.core collection")
})
