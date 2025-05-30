package ansible

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/flightctl/flightctl/test/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

var errorPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)fatal:`),
	regexp.MustCompile(`(?i)failed=`),
	regexp.MustCompile(`(?i)ERROR!`),
	regexp.MustCompile(`"msg":\s*"([^"]+)"`),
}

type AnsibleRunResult struct {
	RawOutput string
	Errors    []string
}

func RunAnsiblePlaybook(playbookPath string, extraArgs ...string) AnsibleRunResult {

	args := append([]string{playbookPath}, extraArgs...)

	ansible_default_path := filepath.Join(os.Getenv("HOME"), util.AnsibleCollectionFLightCTLPath)
	configFile := filepath.Join(util.GetTopLevelDir(), util.AnsibleConfigFilePath)
	args = append(args, []string{"--extra-vars", "@" + configFile}...)
	//args = append(args, []string{"--extra-vars", "flightctl_validate_certs=False"}...)

	cmd := exec.Command("ansible-playbook", args...)
	cmd.Env = os.Environ()
	cmd.Env = append(cmd.Env, fmt.Sprintf("PYTHONPATH=%s/python:$PYTHONPATH", ansible_default_path))
	cmd.Env = append(cmd.Env, "ANSIBLE_STDOUT_CALLBACK=json")                      // Crucial for JSON output
	cmd.Dir = filepath.Join(util.GetTopLevelDir(), util.AnsiblePlaybookFolderPath) // Set the working directory to the Ansible collection path
	logrus.Infof("Running CMD: %s\n", cmd)                                         // from  dir:%s", cmd, cmd.Dir)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	result := AnsibleRunResult{
		RawOutput: outputStr,
		Errors:    []string{},
	}

	for _, pattern := range errorPatterns {
		matches := pattern.FindAllStringSubmatch(outputStr, -1)
		for _, match := range matches {
			if len(match) > 1 {
				result.Errors = append(result.Errors, match[1])
			} else {
				result.Errors = append(result.Errors, match[0])
			}
		}
	}

	if err != nil || len(result.Errors) > 0 {
		logrus.Infof("Ansible Playbook Failed:\n%s", outputStr)
	}

	return result
}

func ParseAnsibleJSONOutput(rawOutput string) (AnsibleOutput, error) {
	var parsed AnsibleOutput
	jsonRegex := regexp.MustCompile(`(?s)(\{.*\"devices\".*?\})`)
	match := jsonRegex.FindStringSubmatch(rawOutput)
	if len(match) >= 2 {
		err := json.Unmarshal([]byte(match[1]), &parsed)
		return parsed, err
	}
	return parsed, fmt.Errorf("no JSON found in output")
}

func CloneAnsibleCollectionRepo(repoURL string, branch string) (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}

	ansibleCollectionsPath := filepath.Join(homeDir, "flightctl-ansible")

	// Check if the directory already exists
	if _, err := os.Stat(ansibleCollectionsPath); err == nil {
		// Directory exists, proceed with cleanup
		logrus.Infof("Cleaning up existing Ansible collection repository at %s\n", ansibleCollectionsPath)
		if err := os.RemoveAll(ansibleCollectionsPath); err != nil {
			return "", fmt.Errorf("failed to clean up existing ansible collection path %s: %w", ansibleCollectionsPath, err)
		}
	} else if !os.IsNotExist(err) {
		// An error occurred other than "does not exist"
		return "", fmt.Errorf("failed to check existence of ansible collection path %s: %w", ansibleCollectionsPath, err)
	}

	// Clone the repository
	logrus.Infof("Cloning flightctl-ansible repository to %s\n", ansibleCollectionsPath)
	_, err = git.PlainClone(ansibleCollectionsPath, false, &git.CloneOptions{
		URL:           repoURL,
		ReferenceName: plumbing.ReferenceName(fmt.Sprintf("refs/heads/%s", branch)),
		Progress:      os.Stdout,
	})
	if err != nil {
		return "", fmt.Errorf("failed to clone flightctl-ansible: %v", err)
	}

	return ansibleCollectionsPath, nil
}

func InstallAnsibleCollectionFromLocalPath(ansibleCollectionsPath string) error {
	// Check if the path is empty
	if ansibleCollectionsPath == "" {
		return fmt.Errorf("ansible collections path is empty")
	}
	if _, err := os.Stat(ansibleCollectionsPath); os.IsNotExist(err) {
		return fmt.Errorf("ansible collections path does not exist: %s", ansibleCollectionsPath)
	}
	// Install the Ansible collection from the local checkout.
	installCollectionCmd := exec.Command("ansible-galaxy", "collection", "install", ".", "--force")
	installCollectionCmd.Dir = ansibleCollectionsPath
	installCollectionOutput, err := installCollectionCmd.CombinedOutput()
	if err != nil {
		logrus.Infof("ansible-galaxy collection install output: %s", installCollectionOutput)
		return fmt.Errorf("failed to install Ansible collection from local path: %v", err)
	}
	logrus.Infof("Ansible collection installed from local path successfully. Output: %s", installCollectionOutput)
	return nil
}

func InstallAnsiblePythonDeps(ansibleCollectionsPath string) error {
	//Install python dependencies
	// This assumes you have a requirements.txt file in the ansible collection directory.
	installDepsCmd := exec.Command("pip", "install", "-r", filepath.Join(ansibleCollectionsPath, "tests", "integration", "requirements.txt"))
	installDepsOutput, err := installDepsCmd.CombinedOutput()
	if err != nil {
		logrus.Infof("pip install output: %s", installDepsOutput)
		return fmt.Errorf("failed to install Python dependencies: %v", err)
	}
	logrus.Infof("Python dependencies installed successfully. Output: %s", installDepsOutput)
	return nil
}

// clientCfg struct matches the structure of your client.yaml
type clientCfg struct {
	Service struct {
		InsecureSkipVerify bool   `yaml:"insecureSkipVerify"`
		Server             string `yaml:"server"`
	} `yaml:"service"`
	Auth struct {
		Token string `yaml:"token"`
	} `yaml:"auth"`
}

// SetupAnsibleTestEnvironment orchestrates the setup for Ansible integration tests.
// It ensures the Ansible collection repo is clean, reads client configuration,
// and creates the Ansible-specific config file.
func SetupAnsibleTestEnvironment() (string, error) {

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user home directory: %w", err)
	}
	// --- 2. Read client.yaml Configuration ---
	clientConfigPath := filepath.Join(homeDir, util.ClientConfigPath) // Assuming util.ClientConfigPath is relative to HOME
	logrus.Infof("Reading client configuration from %s", clientConfigPath)

	cfgBytes, err := os.ReadFile(clientConfigPath)
	if err != nil {
		return "", fmt.Errorf("failed to read client.yaml from %s: %w", clientConfigPath, err)
	}

	var cfg clientCfg
	if err := yaml.Unmarshal(cfgBytes, &cfg); err != nil {
		return "", fmt.Errorf("failed to parse client.yaml from %s: %w", clientConfigPath, err)
	}

	token, serviceAddr := cfg.Auth.Token, cfg.Service.Server
	// if token == "" || serviceAddr == "" {
	// 	return "", fmt.Errorf("missing token or service address in client.yaml: token='%s', serviceAddr='%s'", token, serviceAddr)
	// }
	// logrus.Infof("Successfully read token and service address from client.yaml")

	// --- 3. Create or Update Ansible-specific Config File ---

	// Ensure the directory path exists. If it doesn't, os.MkdirAll will create it
	// along with any necessary parent directories.
	if err := os.MkdirAll(filepath.Join(util.GetTopLevelDir(), util.AnsiblePlaybookFolderPath), 0755); err != nil {
		return "", fmt.Errorf("failed to create directory for Ansible config file at %s: %w", util.AnsiblePlaybookFolderPath, err)
	}

	// Join the directory path with the config file name to get the full path
	// This assumes util.AnsibleConfigFilePath returns just the filename, e.g., "integration_config.yml"
	ansibleConfigFileFullPath := filepath.Join(util.GetTopLevelDir(), util.AnsibleConfigFilePath)

	// Construct the content for the Ansible config file
	fileContent := fmt.Sprintf("flightctl_token: %s\nflightctl_host: %s\n", token, serviceAddr)

	// os.WriteFile will create the file if it doesn't exist, or overwrite it if it does.
	err = os.WriteFile(ansibleConfigFileFullPath, []byte(fileContent), 0600) // 0600 = owner read/write
	if err != nil {
		return "", fmt.Errorf("failed to write to Ansible config file %s: %w", ansibleConfigFileFullPath, err)
	}
	logrus.Infof("%s written successfully", ansibleConfigFileFullPath)

	return ansibleConfigFileFullPath, nil // Return the path to the created config file
}
