package helm_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
)

func captureHelmFailureDiagnostics(harness *e2e.Harness) {
	if !CurrentSpecReport().Failed() || harness == nil || harness.VM == nil {
		return
	}
	testID := harness.GetTestIDFromContext()
	if testID == "" {
		GinkgoWriter.Println("Helm failure diagnostics: no test ID; skipping capture")
		return
	}

	artifactDir := filepath.Join(util.GetTopLevelDir(), "artifacts", "deployment-logs", testID)
	if err := os.MkdirAll(artifactDir, 0o700); err != nil {
		GinkgoWriter.Printf("Helm failure diagnostics: create %s: %v\n", artifactDir, err)
		return
	}

	capture := func(name string, run func() (string, error)) {
		output, err := run()
		if err != nil {
			output += fmt.Sprintf("\ncapture error: %v\n", err)
		}
		path := filepath.Join(artifactDir, name)
		if writeErr := os.WriteFile(path, []byte(output), 0o600); writeErr != nil {
			GinkgoWriter.Printf("Helm failure diagnostics: write %s: %v\n", path, writeErr)
			return
		}
		GinkgoWriter.Printf("Helm failure diagnostics: wrote %s\n", path)
	}

	capture("device-status.json", func() (string, error) {
		devices, err := harness.CLI("get", "device", "-l", "test-id="+testID, "-o", "name")
		if err != nil {
			return devices, err
		}
		snapshots := make([]map[string]any, 0)
		for _, resource := range strings.Fields(devices) {
			deviceID := strings.TrimPrefix(resource, "device/")
			device, err := harness.GetDevice(deviceID)
			if err != nil {
				return "", err
			}
			snapshots = append(snapshots, map[string]any{
				"name":   deviceID,
				"status": device.Status,
			})
		}
		data, err := json.MarshalIndent(snapshots, "", "  ")
		return string(data) + "\n", err
	})

	kubeconfig := "--kubeconfig=" + e2e.MicroshiftKubeconfigPath
	var greenbootHealthcheckJournal string
	capture("microshift-status.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "systemctl", "status", "microshift", "--no-pager")
	})
	capture("microshift-journal.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "journalctl", "-u", "microshift", "-n", "3000", "--no-pager", "-o", "short-precise")
	})
	capture("greenboot-available-boots.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "journalctl", "--list-boots", "--no-pager")
	})
	capture("greenboot-healthcheck-journal.txt", func() (string, error) {
		var err error
		greenbootHealthcheckJournal, err = runHelmVMCommand(harness, "sudo", "journalctl", "-u", "greenboot-healthcheck.service", "-n", "5000", "--no-pager", "-o", "short-precise")
		return greenbootHealthcheckJournal, err
	})
	capture("greenboot-redboot-journal.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "journalctl", "-u", "redboot-task-runner.service", "-n", "5000", "--no-pager", "-o", "short-precise")
	})
	capture("greenboot-status-journal.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "journalctl", "-u", "greenboot-status.service", "-n", "5000", "--no-pager", "-o", "short-precise")
	})
	capture("greenboot-units.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "systemctl", "list-units", "--all", "--no-pager", "greenboot*")
	})
	capture("greenboot-script-files.txt", func() (string, error) {
		return captureMicroshiftGreenbootScriptFiles(harness)
	})
	capture("greenboot-config.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "cat", "/etc/greenboot/greenboot.conf")
	})
	capture("microshift-greenboot-script-journal.txt", func() (string, error) {
		return extractMicroshiftGreenbootScriptLogs(greenbootHealthcheckJournal), nil
	})
	capture("microshift-pods.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "oc", "get", "pods", "-A", "-o", "wide", kubeconfig)
	})
	capture("microshift-events.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "oc", "get", "events", "-A", "--sort-by=.lastTimestamp", kubeconfig)
	})
	capture("helm-resources.yaml", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "oc", "get", "all", "-n", helmAppNamespace, "-o", "yaml", kubeconfig)
	})
	capture("helm-pod-descriptions.txt", func() (string, error) {
		return runHelmVMCommand(harness, "sudo", "oc", "describe", "pods", "-n", helmAppNamespace, kubeconfig)
	})
	capture("helm-pod-logs.txt", func() (string, error) {
		return captureHelmPodLogs(harness, kubeconfig)
	})
}

func captureMicroshiftGreenbootScriptFiles(harness *e2e.Harness) (string, error) {
	var output strings.Builder
	var firstErr error
	for _, dir := range []string{
		"/usr/lib/greenboot/check/required.d",
		"/etc/greenboot/check/required.d",
	} {
		if _, err := runHelmVMCommand(harness, "sudo", "test", "-d", dir); err != nil {
			continue
		}
		paths, err := runHelmVMCommand(harness, "sudo", "find", dir, "-maxdepth", "1", "-type", "f", "-iname", "'*microshift*'", "-print")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, path := range strings.Fields(paths) {
			contents, err := runHelmVMCommand(harness, "sudo", "cat", path)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			fmt.Fprintf(&output, "===== %s =====\n%s\n", path, contents)
		}
	}
	return output.String(), firstErr
}

func extractMicroshiftGreenbootScriptLogs(journal string) string {
	var output strings.Builder
	capturing := false
	for _, line := range strings.Split(journal, "\n") {
		lowerLine := strings.ToLower(line)
		if strings.Contains(lowerLine, "running required check") && strings.Contains(lowerLine, "microshift") {
			capturing = true
		}
		if capturing && strings.Contains(lowerLine, "running wanted check") {
			break
		}
		if capturing {
			output.WriteString(line)
			output.WriteByte('\n')
		}
		if capturing && (strings.Contains(lowerLine, "greenboot health-check passed") || strings.Contains(lowerLine, "greenboot health-check failed")) {
			capturing = false
		}
	}
	return output.String()
}

func configurePersistentJournaldForHelmDiagnostics(harness *e2e.Harness) error {
	const script = `set -euo pipefail
sudo mkdir -p /etc/systemd/journald.conf.d /var/log/journal
cat <<'EOF' | sudo tee /etc/systemd/journald.conf.d/99-e2e-helm-journal.conf >/dev/null
[Journal]
Storage=persistent
EOF
sudo systemd-tmpfiles --create --prefix=/var/log/journal 2>/dev/null || true
sudo systemctl restart systemd-journald
`
	_, err := harness.RunScriptOnVM(script)
	return err
}

func runHelmVMCommand(harness *e2e.Harness, args ...string) (string, error) {
	output, err := harness.VM.RunSSH(args, nil)
	if output == nil {
		return "", err
	}
	return output.String(), err
}

func captureHelmPodLogs(harness *e2e.Harness, kubeconfig string) (string, error) {
	pods, err := runHelmVMCommand(harness, "sudo", "oc", "get", "pods", "-n", helmAppNamespace, "-o", "name", kubeconfig)
	if err != nil {
		return pods, err
	}

	var logs strings.Builder
	for _, pod := range strings.Fields(pods) {
		fmt.Fprintf(&logs, "\n===== %s current logs =====\n", pod)
		output, err := runHelmVMCommand(harness, "sudo", "oc", "logs", "-n", helmAppNamespace, pod, "--all-containers=true", "--prefix=true", "--tail=300", kubeconfig)
		logs.WriteString(output)
		if err != nil {
			fmt.Fprintf(&logs, "capture error: %v\n", err)
		}

		fmt.Fprintf(&logs, "\n===== %s previous logs =====\n", pod)
		output, err = runHelmVMCommand(harness, "sudo", "oc", "logs", "-n", helmAppNamespace, pod, "--all-containers=true", "--prefix=true", "--previous=true", "--tail=300", kubeconfig)
		logs.WriteString(output)
		if err != nil {
			fmt.Fprintf(&logs, "capture error: %v\n", err)
		}
	}
	return logs.String(), nil
}
