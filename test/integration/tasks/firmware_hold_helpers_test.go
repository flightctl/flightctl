package tasks_test

import (
	"context"
	"encoding/json"

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
