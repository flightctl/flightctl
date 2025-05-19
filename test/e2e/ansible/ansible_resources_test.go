package ansible

import (
	"os"
	"path/filepath"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util/ansible"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("Flightctl Ansible Integration", func() {

	var (
		harness *e2e.Harness
	)

	BeforeEach(func() {
		harness = e2e.NewTestHarness()
		login.LoginToAPIWithToken(harness)
	})

	AfterEach(func() {
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
		harness.Cleanup(false)
	})

	Context("verify Resources lifecycle for:", func() {
		It("Device, Fleet, ResourceSync, Repository, EnrollmentRequest, CertificateSigningRequest", Label("75506", "sanity"), func() {
			cwd, err := os.Getwd()
			Expect(err).ToNot(HaveOccurred())
			playbook_path := filepath.Join(cwd, "playbooks/device_lifecycle.yml")
			result := ansible.RunAnsiblePlaybook(playbook_path)
			logrus.Infof("Ansible playbook output: %s", result.RawOutput)
			Expect(result.Errors).To(BeEmpty(), "Ansible playbook execution should not have errors: %v, \n Full output is: %s", result.Errors, result.RawOutput)
			parsed, err := ansible.ParseAnsibleJSONOutput(result.RawOutput)
			Expect(err).ToNot(HaveOccurred())
			Expect(parsed.Result.Devices).ToNot(BeEmpty())
			//Expect(parsed.Result.Devices[0].Name).To(Equal("device-001"))
			//Expect(parsed.Result.Devices[0].Annotations).To(HaveKeyWithValue("env", "prod"))
		})
	})
})
