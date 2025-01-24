package device_selection

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type RolloutDeviceSelector interface {
	HasMoreSelections(ctx context.Context) (bool, error)
	CurrentSelection(ctx context.Context) (Selection, error)
	Advance(ctx context.Context) error
	Reset(ctx context.Context) error
	IsRolloutNew() bool
	OnNewRollout(ctx context.Context) error
	UnmarkRolloutSelection(ctx context.Context) error
}

type Selection interface {
	Devices(ctx context.Context) (*api.DeviceList, error)
	Approve(ctx context.Context) error
	IsApproved() bool
	IsRolledOut(ctx context.Context) (bool, error)
	MayApproveAutomatically() (bool, error)
	IsComplete(ctx context.Context) (bool, error)
	SetSuccessPercentage(ctx context.Context) error
}

func getUpdateTimeout(defaultUpdateTimeoutStr *api.Duration) (time.Duration, error) {
	timeout := DefaultUpdateTimeout

	if defaultUpdateTimeoutStr != nil {
		d, err := time.ParseDuration(*defaultUpdateTimeoutStr)
		if err != nil {
			return 0, fmt.Errorf("failed to parse duration %s: %w", *defaultUpdateTimeoutStr, err)
		}
		if d != 0 {
			timeout = d
		}
	}
	return timeout, nil
}

func NewRolloutDeviceSelector(deviceSelection *api.RolloutDeviceSelection, defaultUpdateTimeoutStr *api.Duration, store store.Store, orgId uuid.UUID, fleet *api.Fleet, templateVersionName string, log logrus.FieldLogger) (RolloutDeviceSelector, error) {

	updateTimeout, err := getUpdateTimeout(defaultUpdateTimeoutStr)
	if err != nil {
		return nil, err
	}
	selectorInterface, err := deviceSelection.ValueByDiscriminator()
	if err != nil {
		return nil, err
	}
	switch v := selectorInterface.(type) {
	case api.BatchSequence:
		return newBatchSequenceSelector(v, updateTimeout, store, orgId, fleet, templateVersionName, log), nil
	default:
		return nil, fmt.Errorf("unexpected selector %T", selectorInterface)
	}
}
