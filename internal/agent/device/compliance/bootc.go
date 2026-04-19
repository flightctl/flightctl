package compliance

import (
	"context"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
)

const (
	bootcTimerUnitFile = "/usr/lib/systemd/system/bootc-fetch-apply-updates.timer"
	bootcTimerUnit     = "bootc-fetch-apply-updates.timer"
)

// BootcChecker checks bootc timer compliance and reports it as a device condition.
type BootcChecker struct {
	exec   executer.Executer
	reader fileio.Reader
	log    *log.PrefixLogger
}

func NewBootcChecker(exec executer.Executer, reader fileio.Reader, log *log.PrefixLogger) *BootcChecker {
	return &BootcChecker{
		exec:   exec,
		reader: reader,
		log:    log,
	}
}

// Status implements status.Exporter interface.
// It checks if bootc-fetch-apply-updates.timer is properly masked and updates the
// BootcTimerCompliant condition accordingly.
func (b *BootcChecker) Status(ctx context.Context, deviceStatus *v1beta1.DeviceStatus, _ ...status.CollectorOpt) error {
	condition := b.checkBootcTimer(ctx)

	// Update or add the condition
	deviceStatus.Conditions = updateCondition(deviceStatus.Conditions, condition)

	return nil
}

// checkBootcTimer checks the bootc timer state and returns the appropriate condition.
func (b *BootcChecker) checkBootcTimer(ctx context.Context) v1beta1.Condition {
	// Check if timer unit file exists
	exists, err := b.reader.PathExists(bootcTimerUnitFile)
	if err != nil {
		b.log.Warnf("Failed to check if bootc timer exists: %v", err)
		// Treat as non-present if we can't check
		return v1beta1.Condition{
			Type:    "BootcTimerCompliant",
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  "NotPresent",
			Message: "bootc-fetch-apply-updates.timer is not present on this system",
		}
	}

	if !exists {
		// Non-bootc system or timer not installed - compliant
		return v1beta1.Condition{
			Type:    "BootcTimerCompliant",
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  "NotPresent",
			Message: "bootc-fetch-apply-updates.timer is not present on this system",
		}
	}

	// Check timer state using systemctl
	stdout, stderr, exitCode := b.exec.ExecuteWithContext(ctx, "systemctl", "is-enabled", bootcTimerUnit)
	state := strings.TrimSpace(stdout)

	b.log.Tracef("bootc timer state check: state=%s, exitCode=%d, stderr=%s", state, exitCode, stderr)

	// Masked state means compliant
	if state == "masked" || state == "masked-runtime" {
		return v1beta1.Condition{
			Type:    "BootcTimerCompliant",
			Status:  v1beta1.ConditionStatusTrue,
			Reason:  "Masked",
			Message: "bootc-fetch-apply-updates.timer is properly masked",
		}
	}

	// Any other state (enabled, disabled, static, etc.) is non-compliant
	// because the timer could potentially be started
	message := "bootc-fetch-apply-updates.timer is not masked - device may auto-update"
	if state != "" {
		message = "bootc-fetch-apply-updates.timer is " + state + " - should be masked to prevent unmanaged OS updates"
	}

	return v1beta1.Condition{
		Type:    "BootcTimerCompliant",
		Status:  v1beta1.ConditionStatusFalse,
		Reason:  "NotMasked",
		Message: message,
	}
}

// updateCondition updates or adds a condition to the conditions list.
// If a condition with the same type exists, it's replaced. Otherwise, it's appended.
func updateCondition(conditions []v1beta1.Condition, newCondition v1beta1.Condition) []v1beta1.Condition {
	for i, c := range conditions {
		if c.Type == newCondition.Type {
			conditions[i] = newCondition
			return conditions
		}
	}
	return append(conditions, newCondition)
}
