package ansible

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

var _ = BeforeSuite(func() {
	var h *e2e.Harness

	fmt.Println("Before all tests!")
	h = e2e.NewTestHarness()
	err := h.CleanUpAllResources()
	Expect(err).ToNot(HaveOccurred())
	login.LoginToAPIWithToken(h)
	// Clone the flightctl-ansible repository.
	// This is a local path to clone into.
	ansibleCollectionsPath := filepath.Join(os.Getenv("HOME"), "flightctl-ansible") // Local path to clone into.
	_, err = os.Stat(ansibleCollectionsPath)
	if os.IsNotExist(err) {
		// Clone the repository if it does not exist
		_, err := git.PlainClone(ansibleCollectionsPath, false, &git.CloneOptions{
			URL:           "https://github.com/flightctl/flightctl-ansible",
			ReferenceName: plumbing.ReferenceName("refs/heads/main"), // Clone the main branch
			Progress:      os.Stdout,
		})
		if err != nil {
			Fail(fmt.Sprintf("Failed to clone flightctl-ansible: %v", err))
		}
		logrus.Infof("flightctl-ansible repository cloned successfully to %s", ansibleCollectionsPath)

	} else {
		logrus.Infof("flightctl-ansible repository already exists in %s", ansibleCollectionsPath)
	}

	// Install the Ansible collection from the local checkout.
	installCollectionCmd := exec.Command("ansible-galaxy", "collection", "install", ".", "--force")
	installCollectionCmd.Dir = ansibleCollectionsPath
	installCollectionOutput, err := installCollectionCmd.CombinedOutput()
	if err != nil {
		logrus.Infof("ansible-galaxy collection install output: %s", installCollectionOutput)
		Fail(fmt.Sprintf("Failed to install Ansible collection from local path: %v", err))
	}
	logrus.Infof("Ansible collection installed from local path successfully. Output: %s", installCollectionOutput)

	// write-integration-config (assuming this is a shell script).
	//    You might need to adjust the path to the script.
	ansible_default_path := filepath.Join(os.Getenv("HOME"), ".ansible/collections/ansible_collections/flightctl/core")

	clientConfigPath := filepath.Join(os.Getenv("HOME"), ".config/flightctl/client.yaml")
	type clientCfg struct {
		Service struct {
			InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
			Server             string `yaml:"server"`
		} `yaml:"service"`
		Auth struct {
			Token string `yaml:"token"`
		} `yaml:"auth"`
	}
	cfgBytes, err := os.ReadFile(clientConfigPath)
	if err != nil {
		Fail(fmt.Sprintf("read client.yaml: %v", err))
	}
	var cfg clientCfg
	if err := yaml.Unmarshal(cfgBytes, &cfg); err != nil {
		Fail(fmt.Sprintf("parse client.yaml: %v", err))
	}
	token, serviceAddr := cfg.Auth.Token, cfg.Service.Server

	configFile := filepath.Join(ansible_default_path, "tests/integration/integration_config.yml")
	err = os.WriteFile(configFile, []byte(fmt.Sprintf("flightctl_token: %s\nflightctl_host: %s\n", token, serviceAddr)), 0600)
	if err != nil {
		Fail(fmt.Sprintf("Failed to write integration_config.yml: %v", err))
	}
	logrus.Infof("%s", fmt.Sprintf("%s written successfully", configFile))
})

// TestAnsible initializes and runs the suite of end-to-end tests for the Command Line Interface (CLI).
func TestAnsible(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Ansible E2E Suite")
}
