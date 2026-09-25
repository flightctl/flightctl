package labelsyncmapping

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type Service interface {
	CreateLabelSyncMapping(context.Context, uuid.UUID, domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status)
	ListLabelSyncMappings(context.Context, uuid.UUID, domain.ListLabelSyncMappingsParams) (*domain.LabelSyncMappingList, domain.Status)
	GetLabelSyncMapping(context.Context, uuid.UUID, string) (*domain.LabelSyncMapping, domain.Status)
	ReplaceLabelSyncMapping(context.Context, uuid.UUID, string, domain.LabelSyncMapping) (*domain.LabelSyncMapping, domain.Status)
	PatchLabelSyncMapping(context.Context, uuid.UUID, string, domain.PatchRequest) (*domain.LabelSyncMapping, domain.Status)
	DeleteLabelSyncMapping(context.Context, uuid.UUID, string) domain.Status
}
