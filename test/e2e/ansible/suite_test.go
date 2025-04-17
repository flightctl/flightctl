// suite_test.go
package ansible_test

import (
	"context"
	"os/exec"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestAnsibleE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ansible E2E Suite")
}

var _ = BeforeSuite(func() {
	var _ = BeforeSuite(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "ansible-galaxy", "collection", "install", "flightctl.core")
		output, err := cmd.CombinedOutput()
		Expect(err).NotTo(HaveOccurred(), "Failed to install flightctl.core collection: %s", output)
	})
})
