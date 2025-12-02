package e2e

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

type jsonPatchOp struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

func PatchDeviceJSON(h *Harness, deviceID string, patch string) {
	By(fmt.Sprintf("patching device %s with: %s", deviceID, patch))

	var ops []jsonPatchOp
	Expect(json.Unmarshal([]byte(patch), &ops)).To(Succeed())

	const maxRetries = 8

	for retry := 1; retry <= maxRetries; retry++ {
		err := h.UpdateDeviceWithRetries(deviceID, func(dev *v1beta1.Device) {
			var specMap map[string]any

			raw, err := json.Marshal(dev.Spec)
			Expect(err).ToNot(HaveOccurred(), "failed to marshal device spec")
			err = json.Unmarshal(raw, &specMap)
			Expect(err).ToNot(HaveOccurred(), "failed to unmarshal device spec")
			if specMap == nil {
				specMap = map[string]any{}
			}

			for _, op := range ops {
				if !strings.HasPrefix(op.Path, "/spec/") {
					Fail(fmt.Sprintf("unsupported path: %s", op.Path))
				}
				sub := strings.TrimPrefix(op.Path, "/spec/")

				switch op.Op {
				case "add":
					switch sub {
					case "systemd":
						var v map[string]any
						Expect(json.Unmarshal(op.Value, &v)).To(Succeed())
						specMap["systemd"] = v

					case "applications":
						var v any
						Expect(json.Unmarshal(op.Value, &v)).To(Succeed())
						specMap["applications"] = v

					case "systemd/matchPatterns/-":
						var svc string
						Expect(json.Unmarshal(op.Value, &svc)).To(Succeed())

						sys, _ := specMap["systemd"].(map[string]any)
						if sys == nil {
							sys = map[string]any{}
						}
						arr, _ := sys["matchPatterns"].([]any)
						arr = append(arr, svc)

						sys["matchPatterns"] = arr
						specMap["systemd"] = sys

					default:
						Fail(fmt.Sprintf("unsupported add path: %s", op.Path))
					}

				case "remove":
					switch sub {
					case "systemd":
						delete(specMap, "systemd")
					case "applications":
						delete(specMap, "applications")
					default:
						Fail(fmt.Sprintf("unsupported remove path: %s", op.Path))
					}

				default:
					Fail(fmt.Sprintf("unsupported op: %s", op.Op))
				}
			}

			updatedRaw, err := json.Marshal(specMap)
			Expect(err).ToNot(HaveOccurred(), "failed to marshal updated spec map")
			Expect(json.Unmarshal(updatedRaw, &dev.Spec)).To(Succeed())
		})

		// SUCCESS → exit retry loop
		if err == nil {
			return
		}

		// ONLY retry on conflict (HTTP 409) – match the message you see in logs
		msg := err.Error()
		if strings.Contains(msg, "Conflict") ||
			strings.Contains(msg, "the object has been modified; please apply your changes to the latest version and try again") {
			time.Sleep(time.Duration(150*retry) * time.Millisecond)
			continue
		}

		Fail(fmt.Sprintf("patchDeviceJSON failed with non-retryable error: %v", err))
	}

	Fail("patchDeviceJSON failed after max retries")
}

func CreateFailingServiceOnDevice(h *Harness, serviceName string) {
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

	cmd := fmt.Sprintf(`sudo -n tee %s >/dev/null <<'EOF'
%s
EOF
sudo -n systemctl daemon-reload
sudo -n systemctl enable %s
sudo -n systemctl start %s || true
`, unitPath, unitContents, serviceName, serviceName)

	stdout, err := h.VM.RunSSH([]string{"bash", "-lc", cmd}, nil)
	if err != nil {
		GinkgoWriter.Printf("command %q failed, stdout: %s\n", cmd, stdout)
	}
	Expect(err).ToNot(HaveOccurred())
}

func StopServiceOnDevice(h *Harness, serviceName string) {
	GinkgoWriter.Printf("stopping service %s on device VM\n", serviceName)
	stdout, err := h.VM.RunSSH([]string{"sudo", "systemctl", "stop", serviceName}, nil)
	if err != nil {
		GinkgoWriter.Printf("systemctl stop stdout: %s\n", stdout)
	}
	Expect(err).ToNot(HaveOccurred())
}

func RestoreServiceOnDevice(h *Harness, serviceName string) {
	GinkgoWriter.Printf("restoring service %s on device VM\n", serviceName)
	stdout, err := h.VM.RunSSH([]string{"sudo", "systemctl", "restart", serviceName}, nil)
	if err != nil {
		GinkgoWriter.Printf("systemctl restart %s failed: %v, stdout: %s\n", serviceName, err, stdout)
	}
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
		MatchPatterns *[]string "json:\"matchPatterns,omitempty\""
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
	var length int

	Eventually(func() int {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil || resp == nil || resp.JSON200 == nil || resp.JSON200.Status == nil || resp.JSON200.Status.Systemd == nil {
			return 0
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

// returns true if the device is updating or has already updated to the expected version
func IsDeviceUpdateObserved(device *v1beta1.Device, expectedVersion int) bool {
	version, err := GetRenderedVersion(device)
	if err != nil {
		var rendered string = "<nil>"
		if device != nil && device.Status != nil {
			rendered = device.Status.Config.RenderedVersion
		}
		GinkgoWriter.Printf("Failed to parse rendered version '%s': %v\n", rendered, err)
		return false
	}
	// The update has already applied
	if version == expectedVersion {
		return true
	}
	cond := v1beta1.FindStatusCondition(device.Status.Conditions, v1beta1.ConditionTypeDeviceUpdating)
	if cond == nil {
		return false
	}
	// send another update if we're in this state
	validReasons := []v1beta1.UpdateState{
		v1beta1.UpdateStatePreparing,
		v1beta1.UpdateStateReadyToUpdate,
		v1beta1.UpdateStateApplyingUpdate,
	}
	return slices.Contains(validReasons, v1beta1.UpdateState(cond.Reason))
}
