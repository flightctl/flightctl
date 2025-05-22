package ansible

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
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
		It("Device, Fleet, ResourceSync, Repository, EnrollmentRequest, CertificateSigningRequest", Label("75506"), func() {
			ansible_default_path := filepath.Join(os.Getenv("HOME"), ".ansible/collections/ansible_collections/flightctl/core")
			configFile := filepath.Join(ansible_default_path, "tests/integration/integration_config.yml")
			logrus.Infof("HOME output: %s", os.Getenv("HOME"))
			testCmd := exec.Command("ansible-test", "integration", "--docker", "--diff", "--color", "--include", "targets/flightctl_resource_info/tasks/devices.yml") //, "-vvv")
			testCmd.Dir = filepath.Join(ansible_default_path, "tests")
			testCmd.Env = os.Environ()
			testCmd.Env = append(testCmd.Env, fmt.Sprintf("ANSIBLE_CONFIG=%s", configFile))
			testCmd.Env = append(testCmd.Env, fmt.Sprintf("PYTHONPATH=%s/python:$PYTHONPATH", ansible_default_path))
			logrus.Infof("ansible-test CMD: %s\nDir:%s", testCmd, testCmd.Dir)
			testOutput, err := testCmd.CombinedOutput()
			testOutputString := string(testOutput)
			if err != nil {
				logrus.Infof("ansible-test output: %s", testOutputString)
				Fail(fmt.Sprintf("Ansible test failed: %v\nOutput:\n%s", err, testOutputString))
			}
			logrus.Infof("ansible-test output: %s", testOutputString)
			Expect(testOutputString).To(ContainSubstring("failed=0"), "Expected zero failed tests")
		})
	})
})
