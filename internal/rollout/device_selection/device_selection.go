package device_selection

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
	Devices(ctx context.Context) (*v1alpha1.DeviceList, error)
	Approve(ctx context.Context) error
	IsApproved() bool
	IsRolledOut(ctx context.Context) (bool, error)
	MayApproveAutomatically() (bool, error)
	IsComplete(ctx context.Context) (bool, error)
	SetSuccessPercentage(ctx context.Context) error
}

func NewRolloutDeviceSelector(deviceSelection *v1alpha1.RolloutDeviceSelection, store store.Store, orgId uuid.UUID, fleet *v1alpha1.Fleet, templateVersionName string, log logrus.FieldLogger) (RolloutDeviceSelector, error) {
	selectorInterface, err := deviceSelection.ValueByDiscriminator()
	if err != nil {
		return nil, err
	}
	switch v := selectorInterface.(type) {
	case v1alpha1.BatchSequence:
		return newBatchSequenceSelector(v, store, orgId, fleet, templateVersionName, log), nil
	default:
		return nil, fmt.Errorf("unexpected selector %T", selectorInterface)
	}
}
