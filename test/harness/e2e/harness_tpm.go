package e2e

import (
	"fmt"
	"os"
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
	// regular pool VMs that may be running for the same workerID.
	tpmWorkerID := workerID + 100
	testVM, err := CreateFreshVMWithTPM(tpmWorkerID, os.TempDir(), 2233, device)
	if err != nil {
		return fmt.Errorf("failed to create VM with TPM passthrough: %w", err)
	}

	h.VM = testVM

	if err := h.configureTPMAgent(TPMTypeReal); err != nil {
		return fmt.Errorf("failed to configure TPM agent: %w", err)
	}

	if err := h.StartFlightCtlAgent(); err != nil {
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
