package store

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type TemplateVersion interface {
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, templateVersion *api.TemplateVersion, eventCallback EventCallback) (*api.TemplateVersion, error)
	Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error)
	Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string, eventCallback EventCallback) (bool, error)

	GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, valid *bool) error
}

type TemplateVersionStore struct {
	dbHandler           *gorm.DB
	log                 logrus.FieldLogger
	genericStore        *GenericStore[*model.TemplateVersion, model.TemplateVersion, api.TemplateVersion, api.TemplateVersionList]
	eventCallbackCaller EventCallbackCaller
}

// Make sure we conform to TemplateVersion interface
var _ TemplateVersion = (*TemplateVersionStore)(nil)

func NewTemplateVersion(db *gorm.DB, log logrus.FieldLogger) TemplateVersion {
	genericStore := NewGenericStore[*model.TemplateVersion, model.TemplateVersion, api.TemplateVersion, api.TemplateVersionList](
		db,
		log,
		model.NewTemplateVersionFromApiResource,
		(*model.TemplateVersion).ToApiResource,
		model.TemplateVersionsToApiResource,
	)
	return &TemplateVersionStore{dbHandler: db, log: log, genericStore: genericStore, eventCallbackCaller: CallEventCallback(api.TemplateVersionKind, log)}
}

func (s *TemplateVersionStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *TemplateVersionStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.TemplateVersion{}); err != nil {
		return err
	}

	// Create GIN index for TemplateVersion labels
	if !db.Migrator().HasIndex(&model.TemplateVersion{}, "idx_template_versions_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_template_versions_labels ON template_versions USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.TemplateVersion{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for TemplateVersion annotations
	if !db.Migrator().HasIndex(&model.TemplateVersion{}, "idx_template_versions_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_template_versions_annotations ON template_versions USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.TemplateVersion{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *TemplateVersionStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, eventCallback EventCallback) (*api.TemplateVersion, error) {
	tv, err := s.genericStore.Create(ctx, orgId, resource)
	name := fmt.Sprintf("%s/%s", lo.FromPtr(resource.Metadata.Owner), lo.FromPtr(resource.Metadata.Name))
	s.eventCallbackCaller(ctx, eventCallback, orgId, name, nil, tv, true, err)
	return tv, err
}

func (s *TemplateVersionStore) Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error) {
	templateVersion := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: fleet,
		Name:      name,
	}
	result := s.getDB(ctx).Take(&templateVersion)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiTemplateVersion, _ := templateVersion.ToApiResource()
	return apiTemplateVersion, nil
}

func (s *TemplateVersionStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error) {
	return s.genericStore.List(ctx, orgId, listParams)
}

func (s *TemplateVersionStore) GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error) {
	var templateVersion model.TemplateVersion
	result := s.getDB(ctx).Model(&templateVersion).Where("org_id = ? AND fleet_name = ?", orgId, fleet).Order("created_at DESC").First(&templateVersion)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiResource, _ := templateVersion.ToApiResource()
	return apiResource, nil
}

func (s *TemplateVersionStore) Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string, eventCallback EventCallback) (bool, error) {
	deleted, err := s.genericStore.Delete(ctx, model.TemplateVersion{OrgID: orgId, Name: name, FleetName: fleet})
	if deleted && eventCallback != nil {
		s.eventCallbackCaller(ctx, eventCallback, orgId, fmt.Sprintf("%s/%s", fleet, name), nil, nil, false, err)
	}
	return deleted, err
}

func (s *TemplateVersionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, valid *bool) error {
	if resource == nil {
		return flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return flterrors.ErrResourceNameIsNil
	}
	_, ownerName, _ := util.GetResourceOwner(resource.Metadata.Owner)
	templateVersion := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: ownerName,
		Name:      *resource.Metadata.Name,
	}

	updates := map[string]interface{}{
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version +1"),
	}
	if valid != nil {
		updates["valid"] = valid
	}

	result := s.getDB(ctx).Model(&templateVersion).Updates(updates)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}

	return nil
}
