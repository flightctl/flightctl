package ansiblewrapper

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"

	adHoc "github.com/apenella/go-ansible/v2/pkg/adhoc"
	execute "github.com/apenella/go-ansible/v2/pkg/execute"
	playbook "github.com/apenella/go-ansible/v2/pkg/playbook"
	"github.com/flightctl/flightctl/test/util"
)

// ModuleName constructs the full module name for a given resource.
func ModuleName(resource string) string {
	return fmt.Sprintf("flightctl.core.%s", resource)
}

// buildArgs converts a map of arguments to a string suitable for Ansible.
func buildArgs(args map[string]interface{}) string {
	var parts []string
	for k, v := range args {
		parts = append(parts, fmt.Sprintf("%s=%v", k, v))
	}
	return strings.Join(parts, " ")
}

// runAdHoc executes an Ansible ad-hoc command with a module and returns raw JSON.
func runAdHoc(module string, args map[string]interface{}) (string, error) {
	if module == "" {
		return "", fmt.Errorf("module name cannot be empty")
	}

	var stdout, stderr bytes.Buffer

	executor := &adHoc.AnsibleAdhocCmd{
		Pattern:    "localhost",
		ModuleName: module,
		ModuleArgs: buildArgs(args),
		Connection: "local",
		Inventory:  "localhost,", // inline inventory
		Executor: &execute.Execute{
			StdoutCallback: "json",
			StdoutWriter:   &stdout,
			StderrWriter:   &stderr,
		},
	}

	ctx := context.Background()
	err := executor.Run(ctx)
	if err != nil {
		return "", fmt.Errorf("ad-hoc execution failed: module=%s, error=%v, stderr=%s",
			module, err, stderr.String())
	}

	output := stdout.String()
	if output == "" {
		return "", fmt.Errorf("no output received from module=%s", module)
	}

	return output, nil
}

// runPlaybook executes a dynamically created playbook with a module and returns raw JSON.
func runPlaybook(module string, args map[string]interface{}) (string, error) {
	tmpFile, err := generateTempPlaybook(module, args)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpFile)

	var stdout, stderr bytes.Buffer

	executor := &playbook.AnsiblePlaybookCmd{
		Playbooks: []string{tmpFile},
		Executor: &execute.Execute{
			StdoutCallback: "json",
			StdoutWriter:   &stdout,
			StderrWriter:   &stderr,
		},
	}

	ctx := context.Background()
	err = executor.Run(ctx)
	if err != nil {
		return "", fmt.Errorf("playbook failed: %v\nstderr: %s", err, stderr.String())
	}
	return stdout.String(), nil
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
      ` + module + `:`

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
