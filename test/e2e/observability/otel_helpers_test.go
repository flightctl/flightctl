package observability_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
)

const (
	otelCollectorService     = "otelcol.service"
	otelCollectorCertificate = "/etc/otelcol/certs/otel.crt"
	otelCollectorPrivateKey  = "/etc/otelcol/certs/otel.key"
)

// ensureOTelDevice enrolls and updates a device to the OTEL image, then waits for
// otelcol to be active. existingDeviceID must identify a device already prepared
// by this helper; its collector readiness is checked again before reuse.
func ensureOTelDevice(ctx context.Context, harness *e2e.Harness, organizationNamespace, existingDeviceID string) (string, error) {
	if harness == nil || harness.VM == nil {
		return "", fmt.Errorf("OTEL device requires a harness with a VM")
	}
	if ctx == nil {
		return "", fmt.Errorf("OTEL device context is nil")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	deviceID := existingDeviceID
	if deviceID == "" {
		if err := resetOTelCollectorIdentity(harness); err != nil {
			return "", fmt.Errorf("reset OTEL collector identity before enrollment: %w", err)
		}
		if err := selectTelemetryOrganization(harness, organizationNamespace); err != nil {
			return "", fmt.Errorf("select organization before OTEL enrollment: %w", err)
		}
		enrollmentConfig, err := harness.GenerateEnrollmentConfig("telemetry-otel-device")
		if err != nil {
			return "", fmt.Errorf("generate organization-scoped enrollment config: %w", err)
		}
		if err := harness.WriteAgentFile("/etc/flightctl/config.yaml", enrollmentConfig); err != nil {
			return "", fmt.Errorf("install organization-scoped enrollment config: %w", err)
		}
		if err := harness.ResetAgentEnrollmentState(); err != nil {
			return "", fmt.Errorf("reset agent enrollment state before OTEL enrollment: %w", err)
		}
		if err := harness.RestartFlightCtlAgent(); err != nil {
			return "", fmt.Errorf("restart flightctl-agent before OTEL enrollment: %w", err)
		}
		enrolledDeviceID, enrolledDevice := harness.EnrollAndWaitForOnlineStatus()
		deviceID = enrolledDeviceID
		if enrolledDevice == nil {
			return "", fmt.Errorf("enroll device and wait for online status returned no device")
		}
		if deviceID == "" {
			return "", fmt.Errorf("enrolled device ID is empty")
		}
		logrus.Infof("Preparing device %s with OTEL image %s", deviceID, util.DeviceTags.V10)
		nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceID)
		if err != nil {
			return "", fmt.Errorf("prepare next rendered version: %w", err)
		}
		if _, _, err := harness.WaitForBootstrapAndUpdateToVersion(deviceID, util.DeviceTags.V10); err != nil {
			return "", fmt.Errorf("update device %s to %s: %w", deviceID, util.DeviceTags.V10, err)
		}
		if err := harness.WaitForDeviceNewRenderedVersionWithReboot(deviceID, nextRenderedVersion); err != nil {
			return "", fmt.Errorf("wait for device %s rendered version %d: %w", deviceID, nextRenderedVersion, err)
		}
		if err := restartOTelCollector(ctx, harness); err != nil {
			return "", fmt.Errorf("start otelcol after updating device %s: %w", deviceID, err)
		}
	}
	logrus.Infof("Waiting for otelcol to be active on device %s", deviceID)
	status := harness.OTelcolActiveStatus()
	var lastStatus string
	err := wait.PollUntilContextTimeout(ctx, POLLING, TIMEOUT, true, func(context.Context) (bool, error) {
		lastStatus = status()
		return lastStatus == "active", nil
	})
	if err != nil {
		return "", fmt.Errorf("wait for otelcol on device %s (last status %q): %w", deviceID, lastStatus, err)
	}
	logrus.Infof("OTEL collector is active on device %s", deviceID)
	return deviceID, nil
}

// resetOTelCollectorIdentity removes the per-device OTEL client credentials so
// a reused VM provisions a certificate for the device enrolled by this spec.
func resetOTelCollectorIdentity(harness *e2e.Harness) error {
	if harness == nil || harness.VM == nil {
		return fmt.Errorf("OTEL collector identity reset requires a harness with a VM")
	}
	if err := e2e.StopServiceOnDevice(harness, otelCollectorService); err != nil {
		return fmt.Errorf("stop %s before removing credentials: %w", otelCollectorService, err)
	}
	for _, path := range []string{otelCollectorCertificate, otelCollectorPrivateKey} {
		if err := harness.RemoveAgentFile(path); err != nil {
			return fmt.Errorf("remove OTEL credential %s: %w", path, err)
		}
	}
	return nil
}

// selectTelemetryOrganization selects the deployment organization before
// enrollment so the issued device certificate contains the organization ID
// required by the telemetry gateway authenticator.
func selectTelemetryOrganization(harness *e2e.Harness, namespace string) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}

	var organizationID string
	if strings.TrimSpace(namespace) != "" {
		resolvedID, err := harness.GetOrganizationIDForNamespace(namespace)
		if err == nil {
			organizationID = resolvedID
		} else {
			logrus.Warnf("organization for namespace %q was not found; falling back to the first organization: %v", namespace, err)
		}
	}
	if organizationID == "" {
		var err error
		organizationID, err = harness.GetOrganizationID()
		if err != nil {
			return fmt.Errorf("resolve organization ID: %w", err)
		}
	}
	if organizationID == "" {
		return fmt.Errorf("resolved organization ID is empty")
	}
	if err := harness.SetCurrentOrganization(organizationID); err != nil {
		return fmt.Errorf("set current organization to %q: %w", organizationID, err)
	}
	return nil
}

// restartOTelCollector restarts the device collector so it establishes a fresh
// exporter connection after the telemetry gateway forwarding configuration changes.
func restartOTelCollector(ctx context.Context, harness *e2e.Harness) error {
	if harness == nil || harness.VM == nil {
		return fmt.Errorf("OTEL collector restart requires a harness with a VM")
	}
	if ctx == nil {
		return fmt.Errorf("OTEL collector restart context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e2e.RestoreServiceOnDevice(harness, otelCollectorService); err != nil {
		return fmt.Errorf("restart otelcol: %w", err)
	}
	status := harness.OTelcolActiveStatus()
	var lastStatus string
	if err := wait.PollUntilContextTimeout(ctx, POLLING, TIMEOUT, true, func(context.Context) (bool, error) {
		lastStatus = status()
		return lastStatus == "active", nil
	}); err != nil {
		return fmt.Errorf("wait for otelcol after restart (last status %q): %w", lastStatus, err)
	}
	return nil
}
