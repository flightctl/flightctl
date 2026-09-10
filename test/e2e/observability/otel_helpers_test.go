package observability_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	"github.com/sirupsen/logrus"
	"k8s.io/apimachinery/pkg/util/wait"
)

// ensureOTelDevice enrolls and updates a device to the OTEL image, then waits for
// otelcol to be active. existingDeviceID must identify a device already prepared
// by this helper; its collector readiness is checked again before reuse.
func ensureOTelDevice(ctx context.Context, harness *e2e.Harness, existingDeviceID string) (string, error) {
	if harness == nil || harness.VM == nil {
		return "", fmt.Errorf("OTEL device requires a harness with a VM")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var enrollmentID string
	defer func() {
		if CurrentSpecReport().Failed() {
			logOTelEnrollmentDiagnostics(harness, enrollmentID)
		}
	}()
	deviceID := existingDeviceID
	if deviceID == "" {
		enrollmentID = harness.GetEnrollmentIDFromServiceLogs("flightctl-agent")
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
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

// logOTelEnrollmentDiagnostics records API-side enrollment and device state after
// an enrollment-related failure. It deliberately excludes certificate and spec data.
func logOTelEnrollmentDiagnostics(harness *e2e.Harness, enrollmentID string) {
	if harness == nil || harness.Client == nil {
		logrus.Warn("OTEL enrollment diagnostics unavailable: harness or client is nil")
		return
	}
	if enrollmentID == "" {
		logrus.Warn("OTEL enrollment diagnostics unavailable: enrollment ID is empty")
		return
	}

	enrollment, err := harness.Client.GetEnrollmentRequestWithResponse(harness.Context, enrollmentID)
	if err != nil {
		logrus.Warnf("OTEL enrollment diagnostics: failed to get enrollment request %s: %v", enrollmentID, err)
	} else if enrollment == nil || enrollment.JSON200 == nil {
		statusCode := 0
		if enrollment != nil {
			statusCode = enrollment.StatusCode()
		}
		logrus.Warnf("OTEL enrollment diagnostics: enrollment request %s unavailable (status=%d)", enrollmentID, statusCode)
	} else {
		approval := "unset"
		conditions := enrollment.JSON200.Status
		if conditions != nil {
			if conditions.Approval != nil {
				approval = fmt.Sprintf("%t", conditions.Approval.Approved)
			}
			logrus.Infof("OTEL enrollment diagnostics: id=%s approval=%s conditions=%s", enrollmentID, approval, formatOTelConditions(conditions.Conditions))
		} else {
			logrus.Infof("OTEL enrollment diagnostics: id=%s approval=%s status=absent", enrollmentID, approval)
		}
	}

	device, err := harness.Client.GetDeviceWithResponse(harness.Context, enrollmentID)
	if err != nil {
		logrus.Warnf("OTEL device diagnostics: failed to get device %s: %v", enrollmentID, err)
		return
	}
	if device == nil || device.JSON200 == nil || device.JSON200.Status == nil {
		statusCode := 0
		if device != nil {
			statusCode = device.StatusCode()
		}
		logrus.Warnf("OTEL device diagnostics: device %s status unavailable (status=%d)", enrollmentID, statusCode)
		return
	}

	status := device.JSON200.Status
	info := "unset"
	if status.Summary.Info != nil {
		info = *status.Summary.Info
	}
	logrus.Infof("OTEL device diagnostics: id=%s summaryStatus=%s summaryInfo=%s conditions=%s", enrollmentID, status.Summary.Status, info, formatOTelConditions(status.Conditions))
}

func formatOTelConditions(conditions []v1beta1.Condition) string {
	if len(conditions) == 0 {
		return "none"
	}
	formatted := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		formatted = append(formatted, fmt.Sprintf("%s=%s reason=%q message=%q", condition.Type, condition.Status, condition.Reason, condition.Message))
	}
	return strings.Join(formatted, "; ")
}
