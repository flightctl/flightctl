package e2e

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

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
