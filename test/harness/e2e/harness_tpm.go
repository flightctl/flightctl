package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"
)

// TPMType represents the type of TPM hardware detected.
type TPMType int

const (
	// TPMTypeSwtpm indicates a software TPM (swtpm) emulator.
	TPMTypeSwtpm TPMType = iota
	// TPMTypeReal indicates real TPM hardware.
	TPMTypeReal
)

func (t TPMType) String() string {
	if t == TPMTypeSwtpm {
		return "swtpm"
	}
	return "real"
}

// DetectTPMType determines whether the VM has a software TPM (swtpm) or real hardware TPM
// by querying the TPM manufacturer properties.
func (h *Harness) DetectTPMType() (TPMType, error) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "tpm2_getcap", "properties-fixed"}, nil)
	if err != nil {
		return TPMTypeSwtpm, fmt.Errorf("failed to query TPM properties: %w", err)
	}

	// swtpm reports manufacturer value starting with "SW" (e.g. value: "SW  ")
	if strings.Contains(stdout.String(), `value: "SW`) {
		logrus.Info("Detected software TPM (swtpm)")
		return TPMTypeSwtpm, nil
	}

	logrus.Info("Detected real TPM hardware")
	return TPMTypeReal, nil
}

// dumpTPMVMFailureDiagnostics collects diagnostic information when the TPM passthrough
// VM fails to start. It checks both the L2 VM (via SSH, if still alive) and the L1 host
// (via local commands for libvirt/QEMU logs) since the VM may have crashed entirely.
func (h *Harness) dumpTPMVMFailureDiagnostics(vmName string) {
	logrus.Info("[TPM-fail-diag] Collecting failure diagnostics...")

	// Try to get agent journal from L2 VM (may fail if VM crashed)
	if h.VM != nil {
		journalOut, journalErr := h.VM.RunSSH([]string{"sudo", "journalctl", "-u", "flightctl-agent", "--no-pager", "-n", "200"}, nil)
		if journalErr != nil {
			logrus.Warnf("[TPM-fail-diag] L2 VM unreachable (likely crashed): %v", journalErr)
		} else {
			logrus.Errorf("[TPM-fail-diag] flightctl-agent journal:\n%s", journalOut)
		}
	}

	// Check L1 host: domain state (runs locally since we're inside L1)
	if out, err := exec.Command("virsh", "-c", "qemu:///session", "domstate", vmName, "--reason").CombinedOutput(); err != nil {
		logrus.Warnf("[TPM-fail-diag] Failed to get domain state: %v", err)
	} else {
		logrus.Infof("[TPM-fail-diag] Domain state for %s: %s", vmName, strings.TrimSpace(string(out)))
	}

	// Check QEMU log for the crashed VM
	homeDir, _ := os.UserHomeDir()
	qemuLogPath := filepath.Join(homeDir, ".cache", "libvirt", "qemu", "log", vmName+".log")
	if out, err := exec.Command("tail", "-n", "100", qemuLogPath).CombinedOutput(); err != nil {
		logrus.Warnf("[TPM-fail-diag] Failed to read QEMU log at %s: %v", qemuLogPath, err)
	} else {
		logrus.Infof("[TPM-fail-diag] QEMU log (%s):\n%s", qemuLogPath, string(out))
	}

	// Check dmesg for TPM-related kernel errors
	if out, err := exec.Command("sudo", "dmesg", "--ctime").CombinedOutput(); err != nil {
		logrus.Warnf("[TPM-fail-diag] Failed to read dmesg: %v", err)
	} else {
		lines := strings.Split(string(out), "\n")
		var tpmLines []string
		for _, line := range lines {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "tpm") || strings.Contains(lower, "qemu") {
				tpmLines = append(tpmLines, line)
			}
		}
		if len(tpmLines) > 0 {
			logrus.Infof("[TPM-fail-diag] TPM/QEMU dmesg entries:\n%s", strings.Join(tpmLines, "\n"))
		} else {
			logrus.Info("[TPM-fail-diag] No TPM/QEMU entries in dmesg")
		}
	}

	// Check SELinux denials
	if out, err := exec.Command("sudo", "ausearch", "-m", "avc", "-ts", "recent").CombinedOutput(); err != nil {
		logrus.Infof("[TPM-fail-diag] No recent SELinux denials (or ausearch unavailable)")
	} else {
		outStr := string(out)
		if strings.Contains(outStr, "tpm") || strings.Contains(outStr, "qemu") {
			logrus.Warnf("[TPM-fail-diag] SELinux denials (TPM/QEMU related):\n%s", outStr)
		}
	}
}

// SetupVMFromPoolWithTPM sets up a VM from the pool, configures TPM, and starts the agent.
// AuthEnabled is set based on TPM type: true for swtpm, false for real hardware
// to avoid taking ownership of real TPM devices.
func (h *Harness) SetupVMFromPoolWithTPM(workerID int, detected TPMType) error {
	if err := h.SetupVMFromPool(workerID); err != nil {
		return err
	}

	if err := h.configureTPMAgent(detected); err != nil {
		return fmt.Errorf("failed to configure TPM agent: %w", err)
	}

	if err := h.StartFlightCtlAgent(); err != nil {
		return fmt.Errorf("failed to start agent with TPM: %w", err)
	}

	return nil
}

// HostHasTPMDevice returns true if the specified TPM device exists on the host.
func HostHasTPMDevice(device string) bool {
	_, err := os.Stat(device)
	return err == nil
}

// SetupVMWithTPMPassthrough creates a fresh VM with TPM passthrough and starts the agent.
// The VM passes through the specified host TPM device instead of using swtpm.
// It reuses the fresh VM pool infrastructure for disk overlay creation, SSH setup, and
// agent state cleanup, then applies TPM-specific agent configuration before starting.
func (h *Harness) SetupVMWithTPMPassthrough(workerID int, device string) error {
	// Offset workerID to avoid SSH port and VM name conflicts with
	// regular pool VMs (1+), backup_restore tests (100+), and rollout tests (1000+).
	tpmWorkerID := workerID + 10000
	testVM, err := CreateFreshVMWithTPM(tpmWorkerID, os.TempDir(), 2233, device)
	if err != nil {
		return fmt.Errorf("failed to create VM with TPM passthrough: %w", err)
	}

	h.VM = testVM

	if err := h.configureTPMAgent(TPMTypeReal); err != nil {
		return fmt.Errorf("failed to configure TPM agent: %w", err)
	}

	if err := h.StartFlightCtlAgent(); err != nil {
		vmName := fmt.Sprintf("flightctl-e2e-fresh-%d", tpmWorkerID)
		h.dumpTPMVMFailureDiagnostics(vmName)
		return fmt.Errorf("failed to start agent: %w", err)
	}

	return nil
}

// configureTPMAgent reads the existing agent config, enables TPM with the appropriate
// auth setting, and writes it back.
func (h *Harness) configureTPMAgent(detected TPMType) error {
	stdout, err := h.VM.RunSSH([]string{"cat", "/etc/flightctl/config.yaml"}, nil)
	var agentConfig *agentcfg.Config

	if err == nil && stdout.Len() > 0 {
		agentConfig = &agentcfg.Config{}
		if unmarshalErr := yaml.Unmarshal(stdout.Bytes(), agentConfig); unmarshalErr != nil {
			logrus.Warnf("Failed to parse existing config, using default: %v", unmarshalErr)
			agentConfig = &agentcfg.Config{}
		}
	} else {
		agentConfig = &agentcfg.Config{}
	}

	agentConfig.LogLevel = "debug"

	// avoid setting auth on a real TPM.
	agentConfig.TPM = agentcfg.TPM{
		Enabled:         true,
		DevicePath:      "/dev/tpm0",
		StorageFilePath: filepath.Join(agentcfg.DefaultDataDir, agentcfg.DefaultTPMKeyFile),
		AuthEnabled:     detected == TPMTypeSwtpm,
	}

	if err := h.SetAgentConfig(agentConfig); err != nil {
		return fmt.Errorf("failed to write TPM agent config: %w", err)
	}

	logrus.Infof("TPM configuration enabled (type=%s, authEnabled=%t)", detected, agentConfig.TPM.AuthEnabled)
	return nil
}

// VerifyEnrollmentTPMAttestationData checks for TPM attestation data in enrollment request SystemInfo.
func (h *Harness) VerifyEnrollmentTPMAttestationData(systemInfo v1beta1.DeviceSystemInfo) error {
	tpmVendorInfo, hasVendorInfo := systemInfo.Get("tpmVendorInfo")
	if hasVendorInfo {
		if tpmVendorInfo == "" {
			return fmt.Errorf("tpmVendorInfo is empty in enrollment request")
		}
		logrus.Infof("TPM vendor info found in enrollment request: %s", tpmVendorInfo)
		return nil
	}

	attestation, hasAttestation := systemInfo.Get("attestation")
	if hasAttestation {
		if attestation == "" {
			return fmt.Errorf("attestation data is empty in enrollment request")
		}
		logrus.Infof("TPM attestation data found in enrollment request: %.50s...", attestation)
		return nil
	}

	return fmt.Errorf("no TPM attestation data found in enrollment request")
}

// VerifyDeviceTPMAttestationData checks for TPM attestation data in device SystemInfo.
func (h *Harness) VerifyDeviceTPMAttestationData(device *v1beta1.Device) error {
	tpmVendorInfo, hasVendorInfo := device.Status.SystemInfo.Get("tpmVendorInfo")
	if hasVendorInfo {
		if tpmVendorInfo == "" {
			return fmt.Errorf("tpmVendorInfo is empty in device system info")
		}
		logrus.Infof("TPM vendor info found in device system info: %s", tpmVendorInfo)
		return nil
	}

	attestation, hasAttestation := device.Status.SystemInfo.Get("attestation")
	if hasAttestation {
		if attestation == "" {
			return fmt.Errorf("attestation data is empty in device system info")
		}
		logrus.Infof("TPM attestation data found in device system info: %.50s...", attestation)
		return nil
	}

	return fmt.Errorf("no TPM attestation data found in device system info")
}
