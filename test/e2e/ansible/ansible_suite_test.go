package ansible

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/ansible"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = BeforeSuite(func() {
	var h *e2e.Harness

	logrus.Infof("Before all tests!")
	h = e2e.NewTestHarness()
	login.LoginToAPIWithToken(h)
	// Clone the flightctl-ansible repository.
	ansibleCollectionsPath, err := ansible.CloneAnsibleCollectionRepo(util.FlightctlAnsibleRepoURL, util.DefaultMainBranch)
	if err != nil {
		Fail(fmt.Sprintf("Failed to clone Ansible collection from local path: %v", err))
	}

	err = ansible.InstallAnsibleCollectionFromLocalPath(ansibleCollectionsPath)
	if err != nil {
		Fail(fmt.Sprintf("Failed to install Ansible collection from local path: %v", err))
	}
	ansible_collection_path := filepath.Join(os.Getenv("HOME"), util.AnsibleCollectionFLightCTLPath)
	err = ansible.InstallAnsiblePythonDeps(ansible_collection_path)
	if err != nil {
		Fail(fmt.Sprintf("Failed to install Ansible collection Python dependencies: %v", err))
	}

	_, err = ansible.SetupAnsibleTestEnvironment()
	if err != nil {
		Fail(fmt.Sprintf("Failed to set up Ansible Configuration: %v", err))
	}
})

// TestAnsible initializes and runs the suite of end-to-end tests for the Command Line Interface (CLI).
func TestAnsible(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ansible E2E Suite")
}
