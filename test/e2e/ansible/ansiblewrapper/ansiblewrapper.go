package ansiblewrapper

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	adHoc "github.com/apenella/go-ansible/v2/pkg/adhoc"
	playbook "github.com/apenella/go-ansible/v2/pkg/playbook"
	"github.com/flightctl/flightctl/test/util"
)

// ModuleName constructs the full module name for a given resource.
func ModuleName(resource string) string {
	return fmt.Sprintf("flightctl.core.%s", resource)
}

// runAdHoc executes an Ansible ad-hoc command with a module and returns raw JSON.
func runAdHoc(module string, args map[string]interface{}) (string, error) {
	if module == "" {
		return "", fmt.Errorf("module name cannot be empty")
	}

	var stderr bytes.Buffer

	executor := &adHoc.AnsibleAdhocCmd{
		Pattern: "localhost",
		AdhocOptions: &adHoc.AnsibleAdhocOptions{
			ModuleName: ModuleName(module),
			Args:       buildArgs(args),
			Connection: "local",
			Inventory:  "localhost,"},
		//StdoutCallback: "json",
		//Stderr:         &stderr,
	}

	output, err := executor.Command()
	if err != nil {
		return "", fmt.Errorf("ad-hoc execution failed: module=%s, error=%v, stderr=%s",
			module, err, stderr.String())
	}

	if len(output) == 0 {
		return "", fmt.Errorf("no output received from module=%s", module)
	}

	result := strings.Join(output, "\n")

	return result, nil
}

// runPlaybook executes a dynamically created playbook with a module and returns raw JSON.
func runPlaybook(module string, args map[string]interface{}) (string, error) {
	tmpFile, err := generateTempPlaybook(module, args)
	if err != nil {
		return "", err
	}
	// Ensure the temporary file is removed after execution
	defer os.Remove(tmpFile)
	var stderr bytes.Buffer
	executor := &playbook.AnsiblePlaybookCmd{
		Playbooks: []string{tmpFile},
		//StdoutCallback: "json",
		//Stderr:         &stderr,
	}
	output, err := executor.Command()
	if err != nil {
		return "", fmt.Errorf("playbook failed: %v\nstderr: %s", err, stderr.String())
	}
	if len(output) == 0 {
		return "", fmt.Errorf("no output received from module=%s", module)
	}
	result := strings.Join(output, "\n")
	return result, nil
}

// RunPlaybookModule executes a dynamically created playbook with a module and returns parsed JSON.
func RunPlaybookModule(module string, args map[string]interface{}) (map[string]interface{}, error) {
	raw, err := runPlaybook(module, args)
	if err != nil {
		return nil, err
	}
	return parseJSON(raw)
}

// RunModule dynamically determines whether to use ad-hoc or playbook execution.
func RunModule(module string, args map[string]interface{}) (string, error) {
	if isSimpleModule(module) {
		return runAdHoc(module, args)
	}
	return runPlaybook(module, args)
}

// RunInfoModule executes an Ansible info module via ad-hoc command and returns parsed JSON.
func RunInfoModule(module string, args map[string]interface{}) (map[string]interface{}, error) {
	raw, err := runAdHoc(module, args)
	if err != nil {
		return nil, err
	}
	return parseJSON(raw)
}

// isSimpleModule determines if this resource is known and supported for ad-hoc.
func isSimpleModule(module string) bool {
	for _, t := range util.ResourceTypes {
		if module == ModuleName(t) {
			return true
		}
	}
	return false
}

func generateTempPlaybook(module string, args map[string]interface{}) (string, error) {
	content := `- name: Dynamic playbook
  hosts: localhost
  gather_facts: false
  tasks:
    - name: Run ` + module + `
      ` + ModuleName(module) + `:`

	for k, v := range args {
		content += fmt.Sprintf("\n        %s: %v", k, v)
	}

	tmpfile, err := os.CreateTemp("", "ansible-playbook-*.yml")
	if err != nil {
		return "", err
	}
	defer tmpfile.Close()

	_, err = tmpfile.WriteString(content)
	return tmpfile.Name(), err
}
