// suite_test.go
package ansible_test

import (
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestAnsibleE2E(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ansible E2E Suite")
}

var _ = BeforeSuite(func() {
	h := e2e.NewTestHarness()
	Eventually(func() error {
		output, err := h.SH("ansible-galaxy", "collection", "install", util.AnsibleGalaxyCollection)
		if err != nil {
			logrus.Infof("Retrying install failed: %s\n", output)
		}
		return err
	}, util.TIMEOUT, "5s").Should(Succeed(), "Failed to install %s collection", util.AnsibleGalaxyCollection)
})
