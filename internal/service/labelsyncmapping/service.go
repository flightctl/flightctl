package labelsyncmapping

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// MaxProvenanceLabelKeys is the maximum number of exact label keys accepted by one organization query.
const MaxProvenanceLabelKeys = 50

type Service interface {
	CreateLabelSyncMapping(context.Context, uuid.UUID, domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status)
	ListLabelSyncMappings(context.Context, uuid.UUID, domain.ListLabelSyncMappingsParams) (*domain.LabelSyncMappingList, domain.Status)
	GetLabelSyncMapping(context.Context, uuid.UUID, string) (*domain.LabelSyncMapping, domain.Status)
	ReplaceLabelSyncMapping(context.Context, uuid.UUID, string, domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status)
	PatchLabelSyncMapping(context.Context, uuid.UUID, string, domain.PatchRequest) (*domain.LabelSyncMapping, domain.Status)
	DeleteLabelSyncMapping(context.Context, uuid.UUID, string) domain.Status
	GetDeviceLabelSyncProvenance(context.Context, uuid.UUID, string) (*domain.LabelSyncProvenanceList, domain.Status)
	GetLabelSyncProvenance(context.Context, uuid.UUID, []string) (*domain.LabelSyncProvenanceList, domain.Status)
	ReconcileDeviceLabels(context.Context, uuid.UUID, string) (ReconciliationResult, error)
	ListMappingScanTargets(context.Context, uuid.UUID) ([]MappingScanToken, error)
	RecordMappingScanFailure(context.Context, uuid.UUID, MappingScanToken, string) (MappingScanToken, bool, error)
	CompleteMappingScan(context.Context, uuid.UUID, []MappingScanToken) (map[uuid.UUID]bool, error)
}
