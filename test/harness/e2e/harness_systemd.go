package e2e

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

const systemdListUnitsProbeTimeout = 30 * time.Second

// SystemdUnitState is one row from `systemctl list-units` on the test VM.
type SystemdUnitState struct {
	Unit        string
	LoadState   string
	ActiveState string
	SubState    string
}

func CreateFailingServiceOnDevice(h *Harness, serviceName string) error {
	GinkgoWriter.Printf("creating failing systemd service %s on device VM\n", serviceName)

	unitPath := fmt.Sprintf("/etc/systemd/system/%s", serviceName)
	unitContents := `[Unit]
Description=Edge test failing service

[Service]
Type=simple
ExecStart=/bin/false
Restart=no

[Install]
WantedBy=multi-user.target`

	cmd := fmt.Sprintf(`set -euo pipefail
sudo -n tee %s >/dev/null <<'EOF'
%s
EOF
sudo -n systemctl daemon-reload
sudo -n systemctl enable %s
sudo -n systemctl start %s
`, unitPath, unitContents, serviceName, serviceName)

	const maxRetries = 5
	var stdoutBuf *bytes.Buffer
	var err error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		stdoutBuf, err = h.VM.RunSSH([]string{"bash", "-lc", cmd}, nil)
		if err == nil {
			break
		}
		outStr := ""
		if stdoutBuf != nil {
			outStr = stdoutBuf.String()
		}
		GinkgoWriter.Printf("attempt %d/%d: CreateFailingServiceOnDevice failed: %v\nstdout: %s\n", attempt, maxRetries, err, outStr)
		time.Sleep(time.Duration(200*attempt) * time.Millisecond)
	}
	if err != nil {
		outStr := ""
		if stdoutBuf != nil {
			outStr = stdoutBuf.String()
		}
		GinkgoWriter.Printf("CreateFailingServiceOnDevice final failure, stdout: %s\n", outStr)
	}
	return err
}

func StopServiceOnDevice(h *Harness, serviceName string) error {
	GinkgoWriter.Printf("stopping service %s on device VM\n", serviceName)
	stdoutBuf, err := h.VM.RunSSH([]string{"sudo", "systemctl", "stop", serviceName}, nil)
	if err != nil {
		outStr := ""
		if stdoutBuf != nil {
			outStr = stdoutBuf.String()
		}
		// Skip failure if the unit is not loaded/present on the VM
		if strings.Contains(outStr, fmt.Sprintf("Unit %s not loaded", serviceName)) ||
			strings.Contains(err.Error(), fmt.Sprintf("Unit %s not loaded", serviceName)) {
			GinkgoWriter.Printf("systemctl stop %s skipped: unit not loaded\n", serviceName)
			return nil
		}
		GinkgoWriter.Printf("systemctl stop stdout: %s\n", outStr)
	}
	return err
}

func RestoreServiceOnDevice(h *Harness, serviceName string) error {
	GinkgoWriter.Printf("restoring service %s on device VM\n", serviceName)
	stdoutBuf, err := h.VM.RunSSH([]string{"sudo", "systemctl", "restart", serviceName}, nil)
	if err != nil {
		outStr := ""
		if stdoutBuf != nil {
			outStr = stdoutBuf.String()
		}
		if strings.Contains(outStr, fmt.Sprintf("Unit %s not found", serviceName)) ||
			strings.Contains(outStr, fmt.Sprintf("Unit %s not loaded", serviceName)) ||
			strings.Contains(err.Error(), fmt.Sprintf("Unit %s not found", serviceName)) ||
			strings.Contains(err.Error(), fmt.Sprintf("Unit %s not loaded", serviceName)) {
			GinkgoWriter.Printf("systemctl restart %s skipped: unit not present\n", serviceName)
			return nil
		}
		GinkgoWriter.Printf("systemctl restart %s failed: %v, stdout: %s\n", serviceName, err, outStr)
	}
	return err
}

// ListSystemdUnitsOnVM lists live systemd units on the harness VM that match the provided patterns.
func (h *Harness) ListSystemdUnitsOnVM(patterns ...string) ([]SystemdUnitState, string, error) {
	return h.ListSystemdUnitsOnVMWithTimeout(systemdListUnitsProbeTimeout, patterns...)
}

// ListSystemdUnitsOnVMContext lists live systemd units on the harness VM using the provided context.
func (h *Harness) ListSystemdUnitsOnVMContext(ctx context.Context, patterns ...string) ([]SystemdUnitState, string, error) {
	if err := h.validateSystemdUnitListing(); err != nil {
		return nil, "", err
	}
	if len(patterns) == 0 {
		return nil, "", fmt.Errorf("at least one systemd unit pattern is required")
	}

	args := append([]string{
		"sudo",
		"systemctl",
		"list-units",
		"--all",
		"--no-legend",
		"--plain",
	}, patterns...)

	output, err := h.VM.RunSSHContext(ctx, args, nil)
	if err != nil {
		return nil, "", fmt.Errorf("listing systemd units on VM: %w", err)
	}
	if output == nil {
		return nil, "", fmt.Errorf("listing systemd units on VM returned nil output")
	}

	rawOutput := output.String()
	if strings.TrimSpace(rawOutput) == "" {
		GinkgoWriter.Printf("systemctl list-units returned no matches for patterns: %v\n", patterns)
	}
	return ParseSystemdListUnits(rawOutput), rawOutput, nil
}

// ListSystemdUnitsOnVMWithTimeout lists live systemd units on the harness VM with a per-call timeout.
func (h *Harness) ListSystemdUnitsOnVMWithTimeout(timeout time.Duration, patterns ...string) ([]SystemdUnitState, string, error) {
	if err := h.validateSystemdUnitListing(); err != nil {
		return nil, "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return h.ListSystemdUnitsOnVMContext(ctx, patterns...)
}

// validateSystemdUnitListing checks that the harness can run systemd unit probes on a VM.
func (h *Harness) validateSystemdUnitListing() error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if h.VM == nil {
		return fmt.Errorf("harness VM is nil")
	}
	return nil
}

// ParseSystemdListUnits parses the stable columns emitted by `systemctl list-units --plain`.
func ParseSystemdListUnits(output string) []SystemdUnitState {
	units := []SystemdUnitState{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		units = append(units, SystemdUnitState{
			Unit:        fields[0],
			LoadState:   fields[1],
			ActiveState: fields[2],
			SubState:    fields[3],
		})
	}
	return units
}

// SystemdUnitsContainState reports whether any parsed unit name matches exactly or with a dash-delimited suffix and has the expected state.
func SystemdUnitsContainState(units []SystemdUnitState, unitName string, loadState string, activeState string, subState string) bool {
	return slices.ContainsFunc(units, func(unit SystemdUnitState) bool {
		return systemdUnitNameMatches(unit.Unit, unitName) &&
			unit.LoadState == loadState &&
			unit.ActiveState == activeState &&
			unit.SubState == subState
	})
}

// systemdUnitNameMatches reports whether unitName is exact or namespaced with a dash delimiter.
func systemdUnitNameMatches(unitName string, expectedName string) bool {
	return unitName == expectedName || strings.HasSuffix(unitName, "-"+expectedName)
}

// RemoveSystemdService disables and removes a systemd unit file on the device and reloads systemd.
func RemoveSystemdService(h *Harness, serviceName string) error {
	GinkgoWriter.Printf("removing systemd service %s on device VM\n", serviceName)
	cmd := fmt.Sprintf(`set -euo pipefail
sudo -n systemctl disable %s
sudo -n rm -f /etc/systemd/system/%s
sudo -n systemctl daemon-reload
`, serviceName, serviceName)

	_, err := h.VM.RunSSH([]string{"bash", "-lc", cmd}, nil)
	return err
}

// UpdateSystemdMatchPatterns applies the given match patterns to device.spec.systemd.
func (h *Harness) UpdateSystemdMatchPatterns(deviceID string, patterns []string) error {
	device, err := h.GetDevice(deviceID)
	if err != nil {
		return err
	}

	var specCopy v1beta1.DeviceSpec
	if device.Spec != nil {
		specCopy = *device.Spec
	}

	mpCopy := append([]string(nil), patterns...)
	specCopy.Systemd = &struct {
		MatchPatterns *[]string `json:"matchPatterns,omitempty"`
	}{
		MatchPatterns: &mpCopy,
	}

	metadata := device.Metadata
	if metadata.Name == nil {
		metadata.Name = &deviceID
	}
	// Drop server-managed fields that would be disallowed on apply
	metadata.ResourceVersion = nil
	metadata.Generation = nil
	metadata.CreationTimestamp = nil

	deviceToApply := v1beta1.Device{
		ApiVersion: v1beta1.DeviceAPIVersion,
		Kind:       v1beta1.DeviceKind,
		Metadata:   metadata,
		Spec:       &specCopy,
	}

	deviceYAML, err := yaml.Marshal(deviceToApply)
	if err != nil {
		return err
	}

	_, err = h.CLIWithStdin(string(deviceYAML), "apply", "-f", "-")
	return err
}

// WaitForSystemdUnitsLen waits until status.systemd has the expected number of units.
// Returns the final length for convenience.
func (h *Harness) WaitForSystemdUnitsLen(deviceID string, expectedLen int, timeout, polling any) int {
	length := -1

	Eventually(func() int {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil {
			length = -1
			return length
		}
		if resp.JSON200.Status.Systemd == nil {
			length = 0
			return length
		}
		length = len(*resp.JSON200.Status.Systemd)
		return length
	}, timeout, polling).Should(Equal(expectedLen))

	return length
}

// WaitForSystemdUnitStatus waits until the requested unit appears in status.systemd and returns it.
// timeout and polling mirror Gomega Eventually args (string or time.Duration).
func (h *Harness) WaitForSystemdUnitStatus(deviceID, unitName string, timeout, polling any) v1beta1.SystemdUnitStatus {
	var unitStatus v1beta1.SystemdUnitStatus

	Eventually(func() error {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil {
			return err
		}
		if resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
			return fmt.Errorf("systemd status not populated yet")
		}

		for _, u := range *resp.JSON200.Status.Systemd {
			if u.Unit == unitName {
				unitStatus = u
				return nil
			}
		}
		return fmt.Errorf("%s not found in status.systemd", unitName)
	}, timeout, polling).Should(Succeed())

	return unitStatus
}
