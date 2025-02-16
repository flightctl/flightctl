package rollout

import (
	"fmt"
	"strconv"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

type Stage int

const (
	Inactive Stage = iota
	ConfiguredBatch
	FinalImplicitBatch
)

func (s Stage) String() string {
	switch s {
	case Inactive:
		return "Rollout inactive"
	case ConfiguredBatch:
		return "Configured batch rollout"
	case FinalImplicitBatch:
		return "Final implicit batch rollout"
	default:
		return fmt.Sprintf("unexpected stage %d", s)
	}
}

func batchSequenceProgressStage(fleet *api.Fleet, sequence api.BatchSequence) (Stage, error) {
	batchNumberStr, exists := util.GetFromMap(lo.FromPtr(fleet.Metadata.Annotations), api.FleetAnnotationBatchNumber)
	if !exists {
		return Inactive, nil
	}
	batchNumber, err := strconv.ParseInt(batchNumberStr, 10, 64)
	if err != nil {
		return Inactive, fmt.Errorf("failed to parse batch number: %w", err)
	}
	switch {
	case int(batchNumber) < len(lo.FromPtr(sequence.Sequence)):
		return ConfiguredBatch, nil
	case int(batchNumber) == len(lo.FromPtr(sequence.Sequence)):
		return FinalImplicitBatch, nil
	default:
		return Inactive, nil
	}
}

func ProgressStage(fleet *api.Fleet) (Stage, error) {
	if fleet.Spec.RolloutPolicy == nil || fleet.Spec.RolloutPolicy.DeviceSelection == nil {
		return Inactive, nil
	}
	intf, err := fleet.Spec.RolloutPolicy.DeviceSelection.ValueByDiscriminator()
	if err != nil {
		return Inactive, fmt.Errorf("value by discriminator: %w", err)
	}
	switch value := intf.(type) {
	case api.BatchSequence:
		return batchSequenceProgressStage(fleet, value)
	default:
		return Inactive, fmt.Errorf("unexpected type for device selection %T", intf)
	}
}
