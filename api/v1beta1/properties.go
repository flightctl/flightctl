package v1alpha1

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

// IsDisconnected() is true if the device never updated status or its last status update is older than disconnectTimeout.
func (d *Device) IsDisconnected(disconnectTimeout time.Duration) bool {
	return d == nil || d.Status == nil || d.Status.LastSeen == nil || (d.Status.LastSeen.IsZero() || d.Status.LastSeen.Add(disconnectTimeout).Before(time.Now()))
}

// IsManaged() if the device has its owner field set.
func (d *Device) IsManaged() bool {
	return d != nil && d.Metadata.Owner != nil && len(util.DefaultIfNil(d.Metadata.Owner, "")) > 0
}

// IsManagedBy() is true if the device is managed by the given fleet.
func (d *Device) IsManagedBy(f *Fleet) bool {
	if f == nil || !d.IsManaged() {
		return false
	}
	return lo.FromPtr(f.Metadata.Name) == strings.TrimPrefix(lo.FromPtr(d.Metadata.Owner), "Fleet/")
}

// IsUpdating() is true if the device's agent reports that it is updating.
func (d *Device) IsUpdating() bool {
	return d != nil && d.Status != nil && IsStatusConditionTrue(d.Status.Conditions, ConditionTypeDeviceUpdating)
}

// IsRebooting() is true if the device's agent has the updating condition set with state Rebooting.
func (d *Device) IsRebooting() bool {
	if d == nil || d.Status == nil {
		return false
	}
	updatingCondition := FindStatusCondition(d.Status.Conditions, ConditionTypeDeviceUpdating)
	if updatingCondition == nil {
		return false
	}
	return updatingCondition.Status == ConditionStatusTrue && updatingCondition.Reason == string(UpdateStateRebooting)
}

// IsUpdatedToDeviceSpec() is true if the device's current rendered version matches its spec's rendered version.
func (d *Device) IsUpdatedToDeviceSpec() bool {
	if d == nil || d.Metadata.Annotations == nil {
		// devices without a rendered version cannot be out-of-date
		return true
	}
	if d.Status == nil {
		// devices without status cannot be up to date
		return false
	}
	renderedVersionString, ok := (*d.Metadata.Annotations)[DeviceAnnotationRenderedVersion]
	if !ok {
		// devices without a rendered version cannot be out-of-date
		return true
	}
	return d.Status.Config.RenderedVersion == renderedVersionString
}

// IsUpdatedToFleetSpec() is true if the IsUpdatedToDeviceSpec() and
// device spec's current rendered version matches its fleet's rendered version.
func (d *Device) IsUpdatedToFleetSpec(f *Fleet) bool {
	if !d.IsManagedBy(f) {
		// a device cannot be up to date relative to a fleet it is not managed by
		return false
	}
	if d.Metadata.Annotations == nil || f.Metadata.Annotations == nil {
		return false
	}
	fleetTemplateVersion, ok := (*f.Metadata.Annotations)[FleetAnnotationTemplateVersion]
	if !ok {
		return false
	}
	deviceTemplateVersion, ok := (*d.Metadata.Annotations)[DeviceAnnotationRenderedTemplateVersion]
	if !ok {
		return false
	}
	return d.IsUpdatedToDeviceSpec() && deviceTemplateVersion == fleetTemplateVersion
}

func (d *Device) Version() string {
	if d == nil || d.Metadata.Annotations == nil {
		return ""
	}
	deviceVersion, ok := (*d.Metadata.Annotations)[DeviceAnnotationRenderedVersion]
	if !ok {
		return ""
	}
	return deviceVersion
}

// IsDecomStarted() is true if the Condition is a DeviceDecommissioning Condition Type with 'True' Status and 'Started' Reason.
func (c *Condition) IsDecomStarted() bool {
	if c.Type == ConditionTypeDeviceDecommissioning && c.Status == ConditionStatusTrue && c.Reason == string(DecommissionStateStarted) {
		return true
	}
	return false
}

// IsDecomComplete() is true if the Condition is a DeviceDecommissioning Condition Type with 'True' Status and 'Complete' Reason.
func (c *Condition) IsDecomComplete() bool {
	if c.Type == ConditionTypeDeviceDecommissioning && c.Status == ConditionStatusTrue && c.Reason == string(DecommissionStateComplete) {
		return true
	}
	return false
}

// IsDecomError() is true if the Condition is a DeviceDecommissioning Condition Type with 'True' Status and 'Error' Reason.
func (c *Condition) IsDecomError() bool {
	if c.Type == ConditionTypeDeviceDecommissioning && c.Status == ConditionStatusTrue && c.Reason == string(DecommissionStateError) {
		return true
	}
	return false
}

// GetAnnotation returns the value of the given annotation from the fleet and whether it exists.
func (f *Fleet) GetAnnotation(annotation string) (string, bool) {
	if f == nil {
		return "", false
	}
	return util.GetFromMap(lo.FromPtr(f.Metadata.Annotations), annotation)
}

// IsRolloutNew returns true if the fleet has a new rollout (deploying template version annotation exists on newFleet but not on oldFleet).
func (f *Fleet) IsRolloutNew(oldFleet *Fleet) bool {
	if f == nil {
		return false
	}
	var existsOldFleet bool
	if oldFleet != nil {
		_, existsOldFleet = oldFleet.GetAnnotation(FleetAnnotationDeployingTemplateVersion)
	}
	_, existsNewFleet := f.GetAnnotation(FleetAnnotationDeployingTemplateVersion)
	return !existsOldFleet && existsNewFleet
}

// IsRolloutBatchCompleted returns true if the fleet rollout batch is completed, and the completion report.
func (f *Fleet) IsRolloutBatchCompleted(oldFleet *Fleet) (bool, *RolloutBatchCompletionReport) {
	if f == nil {
		return false, nil
	}
	var existsOldFleet bool
	var oldReport string

	if oldFleet != nil {
		oldReport, existsOldFleet = oldFleet.GetAnnotation(FleetAnnotationLastBatchCompletionReport)
	}

	newReport, existsNewFleet := f.GetAnnotation(FleetAnnotationLastBatchCompletionReport)
	if !existsNewFleet {
		return false, nil
	}

	if existsOldFleet && oldReport == newReport {
		return false, nil
	}

	report := RolloutBatchCompletionReport{}
	if err := json.Unmarshal([]byte(newReport), &report); err != nil {
		return false, nil
	}

	return true, &report
}
