package tasks_test

import (
	"context"
	"encoding/json"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/worker_client"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	holdUpdaterName    = "vendor-updater"
	holdUpdaterVersion = "1.0.0"
)

// holdFirmwareSpec returns desired firmware whose updater configuration names
// repository as the source of its artifacts.
func holdFirmwareSpec(repository string) *api.FirmwareSpec {
	return &api.FirmwareSpec{
		Profile: "board-a",
		Updater: api.FirmwareUpdaterSpec{
			Name:    holdUpdaterName,
			Version: holdUpdaterVersion,
			Config:  map[string]interface{}{"source": map[string]interface{}{"repository": repository}},
		},
		Components: []api.FirmwareComponentSpec{{Name: "bios", Version: api.FirmwareVersionConstraints{Exact: lo.ToPtr("2.0")}}},
		Overlays:   []api.FirmwareOverlaySpec{},
	}
}

// holdFirmwareStatus returns reported firmware with an attempt in phase.
func holdFirmwareStatus(phase api.FirmwareAttemptPhase) *api.FirmwareStatus {
	observedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &api.FirmwareStatus{
		BootID:     "boot-1",
		ObservedAt: observedAt,
		Support:    api.FirmwareSupportStatus{ApiVersion: 1, Supported: true, Updaters: []api.FirmwareSupportedUpdater{}},
		Components: []api.FirmwareComponentStatus{{
			Name:       "bios",
			Type:       api.FirmwareComponentTypeFirmware,
			State:      api.FirmwareComponentStateActive,
			Managed:    true,
			BootID:     "boot-1",
			ObservedAt: observedAt,
		}},
		Attempt: &api.FirmwareAttemptStatus{
			Id:              "attempt-1",
			Phase:           phase,
			RenderedVersion: "3",
			SpecHash:        "spec-hash",
			StartedAt:       observedAt,
			Recovery:        []api.FirmwareRecoveryAction{},
		},
	}
}

// holdDescriptors approves one updater release whose configuration names its
// artifact repository at source.repository.
type holdDescriptors struct{}

func (holdDescriptors) LookupRelease(_ context.Context, name, version string) (*common.ApprovedDescriptor, error) {
	if name != holdUpdaterName || version != holdUpdaterVersion {
		return nil, nil
	}
	return &common.ApprovedDescriptor{
		Name:           name,
		Version:        version,
		Approved:       true,
		BuildDigest:    "sha256:" + lo.RandomString(64, []rune("0123456789abcdef")),
		SchemaDigest:   "sha256:" + lo.RandomString(64, []rune("0123456789abcdef")),
		ConfigSchema:   json.RawMessage(`{"type":"object"}`),
		ReferencePaths: []common.FirmwareReferencePath{{Path: "source.repository", Kind: common.FirmwareReferenceArtifact}},
	}, nil
}

func (holdDescriptors) LookupSchemaByDigest(context.Context, string, string) (*common.RetainedSchemaEvidence, error) {
	return nil, nil
}

// queuedEventReasons decodes the reasons of the events in queue payloads.
func queuedEventReasons(payloads [][]byte) []api.EventReason {
	reasons := make([]api.EventReason, 0, len(payloads))
	for _, payload := range payloads {
		var queued worker_client.EventWithOrgId
		Expect(json.Unmarshal(payload, &queued)).To(Succeed())
		reasons = append(reasons, queued.Event.Reason)
	}
	return reasons
}

// conditionOf returns the condition of type t, or nil.
func conditionOf(conditions []api.Condition, t api.ConditionType) *api.Condition {
	return api.FindStatusCondition(conditions, t)
}
