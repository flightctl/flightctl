package model

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"gorm.io/gorm"
)

type LabelSyncMapping struct {
	Resource
	ID                uuid.UUID `gorm:"type:uuid;not null;default:gen_random_uuid()"`
	FailureRevision   int64     `gorm:"not null;default:0"`
	DeletionRevision  *int64
	DeletionTimestamp *time.Time
	Spec              *JSONField[domain.LabelSyncMappingSpec]   `gorm:"type:jsonb"`
	Status            *JSONField[domain.LabelSyncMappingStatus] `gorm:"type:jsonb"`
}

func (LabelSyncMapping) TableName() string { return "label_sync_mappings" }

func (m *LabelSyncMapping) BeforeCreate(_ *gorm.DB) error {
	if m.ID == uuid.Nil {
		m.ID = uuid.New()
	}
	return nil
}

func (m LabelSyncMapping) String() string {
	value, _ := json.Marshal(m)
	return string(value)
}

func NewLabelSyncMappingFromApiResource(resource *domain.LabelSyncMapping) (*LabelSyncMapping, error) {
	if resource == nil || resource.Metadata.Name == nil {
		return &LabelSyncMapping{}, nil
	}
	var resourceVersion *int64
	if resource.Metadata.ResourceVersion != nil {
		value, err := strconv.ParseInt(lo.FromPtr(resource.Metadata.ResourceVersion), 10, 64)
		if err != nil {
			return nil, flterrors.ErrIllegalResourceVersionFormat
		}
		resourceVersion = &value
	}
	status := domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{}}
	if resource.Status != nil {
		status = *resource.Status
	}
	return &LabelSyncMapping{
		Resource: Resource{
			Name:            *resource.Metadata.Name,
			Labels:          lo.FromPtrOr(resource.Metadata.Labels, make(map[string]string)),
			Annotations:     lo.FromPtr(resource.Metadata.Annotations),
			Generation:      clonePtr(resource.Metadata.Generation),
			ResourceVersion: resourceVersion,
		},
		Spec:   MakeJSONField(resource.Spec),
		Status: MakeJSONField(status),
	}, nil
}

func LabelSyncMappingAPIVersion() string {
	return fmt.Sprintf("%s/%s", domain.APIGroup, domain.LabelSyncMappingAPIVersion)
}

func (m *LabelSyncMapping) ToApiResource(_ ...APIResourceOption) (*domain.LabelSyncMapping, error) {
	if m == nil {
		return &domain.LabelSyncMapping{}, nil
	}
	spec := domain.LabelSyncMappingSpec{}
	if m.Spec != nil {
		spec = m.Spec.Data
	}
	status := domain.LabelSyncMappingStatus{Conditions: &[]domain.Condition{}}
	if m.Status != nil {
		status = m.Status.Data
	}
	return &domain.LabelSyncMapping{
		ApiVersion: LabelSyncMappingAPIVersion(),
		Kind:       domain.LabelSyncMappingKind,
		Metadata: domain.ObjectMeta{
			Name:              lo.ToPtr(m.Name),
			CreationTimestamp: lo.ToPtr(m.CreatedAt.UTC()),
			Labels:            lo.ToPtr(util.EnsureMap(m.Labels)),
			Annotations:       lo.ToPtr(util.EnsureMap(m.Annotations)),
			Generation:        clonePtr(m.Generation),
			ResourceVersion:   lo.Ternary(m.ResourceVersion != nil, lo.ToPtr(strconv.FormatInt(lo.FromPtr(m.ResourceVersion), 10)), nil),
			DeletionTimestamp: clonePtr(m.DeletionTimestamp),
		},
		Spec:   spec,
		Status: &status,
	}, nil
}

func LabelSyncMappingsToApiResource(mappings []LabelSyncMapping, cont *string, remaining *int64) (domain.LabelSyncMappingList, error) {
	items := make([]domain.LabelSyncMapping, len(mappings))
	for i := range mappings {
		item, err := mappings[i].ToApiResource()
		if err != nil {
			return domain.LabelSyncMappingList{}, err
		}
		items[i] = *item
	}
	return domain.LabelSyncMappingList{
		ApiVersion: LabelSyncMappingAPIVersion(),
		Kind:       domain.LabelSyncMappingListKind,
		Metadata:   domain.ListMeta{Continue: cont, RemainingItemCount: remaining},
		Items:      items,
	}, nil
}

func (*LabelSyncMapping) GetKind() string    { return domain.LabelSyncMappingKind }
func (m *LabelSyncMapping) HasNilSpec() bool { return m.Spec == nil }
func (m *LabelSyncMapping) HasSameSpecAs(otherResource any) bool {
	other, ok := otherResource.(*LabelSyncMapping)
	if !ok || other == nil {
		return false
	}
	if m.Spec == nil || other.Spec == nil {
		return m.Spec == nil && other.Spec == nil
	}
	return reflect.DeepEqual(m.Spec.Data, other.Spec.Data)
}
func (m *LabelSyncMapping) GetStatusAsJson() ([]byte, error) {
	if m.Status == nil {
		return []byte("null"), nil
	}
	return m.Status.MarshalJSON()
}

var _ ResourceInterface = (*LabelSyncMapping)(nil)
