package domain

import "github.com/google/uuid"

// DeviceLabelOwnership is a label value and the mapping that currently owns it.
type DeviceLabelOwnership struct {
	Key       string
	Value     string
	MappingID *uuid.UUID
}

// DeviceLabelSnapshot is a consistent device resource and label-ownership view.
type DeviceLabelSnapshot struct {
	Device Device
	Labels []DeviceLabelOwnership
}

// DesiredDeviceLabel is the reconciled value and optional mapping owner for a key.
type DesiredDeviceLabel struct {
	Value     string
	MappingID *uuid.UUID
}

// DeviceLabelApplyResult describes changes committed by label reconciliation.
type DeviceLabelApplyResult struct {
	Device               *Device
	LabelsChanged        bool
	ManagedLabelsChanged bool
	OwnershipChanged     bool
}
