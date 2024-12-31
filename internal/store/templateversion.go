package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type TemplateVersion interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, templateVersion *api.TemplateVersion, callback TemplateVersionStoreCallback) (*api.TemplateVersion, error)
	Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error)
	Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, fleet *string) error

	GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error)
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, valid *bool, callback TemplateVersionStoreCallback) error
}

type TemplateVersionStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.TemplateVersion, model.TemplateVersion, api.TemplateVersion, *model.TemplateVersionList, api.TemplateVersionList]
}

type TemplateVersionStoreCallback func(before *model.TemplateVersion, after *model.TemplateVersion)

// Make sure we conform to TemplateVersion interface
var _ TemplateVersion = (*TemplateVersionStore)(nil)

func NewTemplateVersion(db *gorm.DB, log logrus.FieldLogger) TemplateVersion {
	genericStore := NewGenericStore[*model.TemplateVersion, model.TemplateVersion, api.TemplateVersion, *model.TemplateVersionList, api.TemplateVersionList](
		db,
		log,
		model.NewTemplateVersionFromApiResource,
		(*model.TemplateVersion).ToApiResource,
		(*model.TemplateVersionList).ToApiResource,
		model.TemplateVersionPtrReturnSelf,
		model.TemplateVersionPtrReturnSelf,
	)
	return &TemplateVersionStore{db: db, log: log, genericStore: genericStore}
}

func (s *TemplateVersionStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.TemplateVersion{}); err != nil {
		return err
	}

	// Create GIN index for TemplateVersion labels
	if !s.db.Migrator().HasIndex(&model.TemplateVersion{}, "idx_template_versions_labels") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_template_versions_labels ON template_versions USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.TemplateVersion{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for TemplateVersion annotations
	if !s.db.Migrator().HasIndex(&model.TemplateVersion{}, "idx_template_versions_annotations") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_template_versions_annotations ON template_versions USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.TemplateVersion{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *TemplateVersionStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, callback TemplateVersionStoreCallback) (*api.TemplateVersion, error) {
	return s.genericStore.Create(ctx, orgId, resource, callback)
}

func (s *TemplateVersionStore) Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error) {
	return s.genericStore.Get(ctx, orgId, name)
}

func (s *TemplateVersionStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error) {
	var templateVersions model.TemplateVersionList
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitParamOutOfBounds
	}

	query, err := ListQuery(&templateVersions).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&templateVersions)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(templateVersions) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    templateVersions[len(templateVersions)-1].Name,
			Version: CurrentContinueVersion,
		}
		templateVersions = templateVersions[:len(templateVersions)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(&templateVersions).Build(ctx, s.db, orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiTemplateVersionList, _ := templateVersions.ToApiResource(nextContinue, numRemaining)
	return &apiTemplateVersionList, ErrorFromGormError(result.Error)
}

func (s *TemplateVersionStore) GetLatest(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error) {
	var templateVersion model.TemplateVersion
	result := s.db.Model(&templateVersion).Where("org_id = ? AND fleet_name = ?", orgId, fleet).Order("created_at DESC").First(&templateVersion)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	apiResource, _ := templateVersion.ToApiResource()
	return apiResource, nil
}

func (s *TemplateVersionStore) Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string) error {
	return s.genericStore.Delete(ctx, model.TemplateVersion{OrgID: orgId, Name: name, FleetName: fleet}, nil)
}

func (s *TemplateVersionStore) DeleteAll(ctx context.Context, orgId uuid.UUID, fleet *string) error {
	condition := model.TemplateVersion{}
	unscoped := s.db.Unscoped()
	var whereQuery *gorm.DB
	if fleet != nil {
		whereQuery = unscoped.Where("org_id = ? AND fleet_name = ?", orgId, *fleet)
	} else {
		whereQuery = unscoped.Where("org_id = ?", orgId)
	}

	result := whereQuery.Delete(&condition)
	return ErrorFromGormError(result.Error)
}

func (s *TemplateVersionStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, valid *bool, callback TemplateVersionStoreCallback) error {
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

	result := s.db.Model(&templateVersion).Updates(updates)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}

	if valid != nil && *valid {
		callback(nil, &templateVersion)
	}
	return nil
}
