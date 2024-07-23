package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"

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
	Create(ctx context.Context, orgId uuid.UUID, templateVersion *api.TemplateVersion, callback TemplateVersionStoreCallback) (*api.TemplateVersion, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error)
	DeleteAll(ctx context.Context, orgId uuid.UUID, fleet *string) error
	Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error)
	Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, valid *bool, callback TemplateVersionStoreCallback) error
	GetNewestValid(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error)
	InitialMigration() error
}

type TemplateVersionStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type TemplateVersionStoreCallback func(tv *model.TemplateVersion)

// Make sure we conform to TemplateVersion interface
var _ TemplateVersion = (*TemplateVersionStore)(nil)

func NewTemplateVersion(db *gorm.DB, log logrus.FieldLogger) TemplateVersion {
	return &TemplateVersionStore{db: db, log: log}
}

func (s *TemplateVersionStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.TemplateVersion{})
}

func (s *TemplateVersionStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.TemplateVersion, callback TemplateVersionStoreCallback) (*api.TemplateVersion, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	templateVersion, err := model.NewTemplateVersionFromApiResource(resource)
	if err != nil {
		return nil, err
	}
	templateVersion.OrgID = orgId
	templateVersion.Generation = lo.ToPtr[int64](1)
	templateVersion.ResourceVersion = lo.ToPtr[int64](1)
	status := api.TemplateVersionStatus{Conditions: []api.Condition{}}
	api.SetStatusCondition(&status.Conditions, api.Condition{Type: api.TemplateVersionValid, Status: api.ConditionStatusUnknown})
	templateVersion.Status = model.MakeJSONField(status)
	fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: resource.Spec.Fleet}}
	if err = s.db.First(&fleet).Error; err != nil {
		return nil, flterrors.ErrorFromGormError(err)
	}

	if err = s.db.Create(templateVersion).Error; err != nil {
		return nil, flterrors.ErrorFromGormError(err)
	}
	callback(templateVersion)
	return lo.ToPtr(templateVersion.ToApiResource()), err
}

func (s *TemplateVersionStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.TemplateVersionList, error) {
	var templateVersions model.TemplateVersionList
	var nextContinue *string
	var numRemaining *int64

	query := BuildBaseListQuery(s.db.Model(&templateVersions), orgId, listParams)
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
			countQuery := BuildBaseListQuery(s.db.Model(&templateVersions), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}

	apiTemplateVersionList := templateVersions.ToApiResource(nextContinue, numRemaining)
	return &apiTemplateVersionList, flterrors.ErrorFromGormError(result.Error)
}

func (s *TemplateVersionStore) GetNewestValid(ctx context.Context, orgId uuid.UUID, fleet string) (*api.TemplateVersion, error) {
	var templateVersion model.TemplateVersion
	result := s.db.Model(&templateVersion).Where("org_id = ? AND fleet_name = ? AND valid = ?", orgId, fleet, true).Order("created_at DESC").First(&templateVersion)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	apiResource := templateVersion.ToApiResource()
	return &apiResource, nil
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
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *TemplateVersionStore) Get(ctx context.Context, orgId uuid.UUID, fleet string, name string) (*api.TemplateVersion, error) {
	templateVersion := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: fleet,
		Name:      name,
	}
	result := s.db.First(&templateVersion)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	apiTemplateVersion := templateVersion.ToApiResource()
	return &apiTemplateVersion, nil
}

func (s *TemplateVersionStore) Delete(ctx context.Context, orgId uuid.UUID, fleet string, name string) error {
	condition := model.TemplateVersion{
		OrgID:     orgId,
		FleetName: fleet,
		Name:      name,
	}
	result := s.db.Unscoped().Delete(&condition)
	if errors.Is(result.Error, gorm.ErrRecordNotFound) {
		return nil
	}
	return flterrors.ErrorFromGormError(result.Error)
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
		return flterrors.ErrorFromGormError(result.Error)
	}

	if valid != nil && *valid {
		callback(&templateVersion)
	}
	return nil
}
