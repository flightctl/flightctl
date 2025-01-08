package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
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

type IntegrationTestCallback func()

// GenericStore provides generic CRUD operations for resources
// P is a pointer to a model, for example: *model.Device
// M is the model, for example: model.Device
// A is the API resource, for example: api.Device
// ML is the model list, for example: model.DeviceList
// AL is the API list, for example: api.DeviceList
type GenericStore[P model.Generic, M any, A any, ML model.GenericList, AL any] struct {
	db  *gorm.DB
	log logrus.FieldLogger

	// Conversion functions between API and model types
	apiToModelPtr   func(*A) (P, error)
	modelPtrToAPI   func(P, ...model.APIResourceOption) (*A, error)
	listModelToAPI  func(ML, *string, *int64) (AL, error)
	modelPtrToModel func(*M) P
	modelModelToPtr func(P) *M

	// Callback for integration tests to inject logic
	IntegrationTestCreateOrUpdateCallback IntegrationTestCallback
}

type Resource struct {
	Table string
	OrgID string
	Name  string
}

func NewGenericStore[P model.Generic, M any, A any, ML model.GenericList, AL any](
	db *gorm.DB,
	log logrus.FieldLogger,
	apiToModelPtr func(*A) (P, error),
	modelPtrToAPI func(P, ...model.APIResourceOption) (*A, error),
	listModelToAPI func(ML, *string, *int64) (AL, error),
	modelToModelPtr func(*M) P,
	modelModelToPtr func(P) *M,
) *GenericStore[P, M, A, ML, AL] {
	return &GenericStore[P, M, A, ML, AL]{
		db:                                    db,
		log:                                   log,
		apiToModelPtr:                         apiToModelPtr,
		modelPtrToAPI:                         modelPtrToAPI,
		listModelToAPI:                        listModelToAPI,
		modelPtrToModel:                       modelToModelPtr,
		modelModelToPtr:                       modelModelToPtr,
		IntegrationTestCreateOrUpdateCallback: func() {},
	}
}

func (s *GenericStore[P, M, A, ML, AL]) Create(ctx context.Context, orgId uuid.UUID, resource *A, callback func(before, after *M)) (*A, error) {
	updated, _, _, err := s.createOrUpdate(ctx, orgId, resource, nil, true, ModeCreateOnly, callback)
	return updated, err
}

func (s *GenericStore[P, M, A, ML, AL]) Update(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, callback func(before, after *M)) (*A, error) {
	updated, _, err := retryCreateOrUpdate(func() (*A, bool, bool, error) {
		return s.createOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, ModeUpdateOnly, callback)
	})
	return updated, err
}

func (s *GenericStore[P, M, A, ML, AL]) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, callback func(before, after *M)) (*A, bool, error) {
	return retryCreateOrUpdate(func() (*A, bool, bool, error) {
		return s.createOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, ModeCreateOrUpdate, callback)
	})
}

func (s *GenericStore[P, M, A, ML, AL]) createOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, mode CreateOrUpdateMode, callback func(before, after *M)) (*A, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}

	model, err := s.apiToModelPtr(resource)
	if err != nil {
		return nil, false, false, err
	}
	model.SetOrgID(orgId)
	model.SetAnnotations(nil)

	existing, err := s.getExistingResource(ctx, model.GetName(), orgId)
	if err != nil {
		return nil, false, false, err
	}
	exists := existing != nil

	if exists && mode == ModeCreateOnly {
		return nil, false, false, flterrors.ErrDuplicateName
	}
	if !exists && mode == ModeUpdateOnly {
		return nil, false, false, flterrors.ErrResourceNotFound
	}

	s.IntegrationTestCreateOrUpdateCallback()

	var retry bool
	if !exists {
		retry, err = s.createResource(ctx, model)
	} else {
		retry, err = s.updateResource(ctx, fromAPI, s.modelPtrToModel(existing), model, fieldsToUnset)
	}
	if err != nil {
		return nil, false, retry, err
	}

	if callback != nil {
		callback(existing, s.modelModelToPtr(model))
	}

	apiResource, err := s.modelPtrToAPI(model)

	return apiResource, !exists || s.modelPtrToModel(existing).HasNilSpec(), false, err
}

func (s *GenericStore[P, M, A, ML, AL]) getExistingResource(ctx context.Context, name string, orgId uuid.UUID) (*M, error) {
	var existingResource M
	if err := s.db.WithContext(ctx).Where("name = ? and org_id = ?", name, orgId).First(&existingResource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrorFromGormError(err)
	}
	return &existingResource, nil
}

func (s *GenericStore[P, M, A, ML, AL]) createResource(ctx context.Context, resource P) (bool, error) {
	resource.SetGeneration(util.Int64ToPtr(1))
	resource.SetResourceVersion(util.Int64ToPtr(1))

	result := s.db.WithContext(ctx).Create(resource)
	if result.Error != nil {
		err := ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *GenericStore[P, M, A, ML, AL]) updateResource(ctx context.Context, fromAPI bool, existing, resource P, fieldsToUnset []string) (bool, error) {
	if fromAPI && (util.DefaultIfNil(resource.GetOwner(), "<NIL>") != util.DefaultIfNil(existing.GetOwner(), "<NIL>")) {
		return false, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
	}

	sameSpec := resource.HasSameSpecAs(existing)
	if !sameSpec {
		if fromAPI {
			if len(lo.FromPtr(existing.GetOwner())) != 0 {
				// Don't let the user update the spec if it has an owner
				return false, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
			} else {
				// Remove the TemplateVersion annotation if the device has no owner
				if resource.GetKind() == api.DeviceKind {
					existingAnnotations := util.EnsureMap(existing.GetAnnotations())
					if existingAnnotations[api.DeviceAnnotationTemplateVersion] != "" {
						delete(existingAnnotations, api.DeviceAnnotationTemplateVersion)
						resource.SetAnnotations(existingAnnotations)
					}
				}
			}
		}

		// Update the generation if the spec was updated
		resource.SetGeneration(lo.ToPtr(lo.FromPtr(existing.GetGeneration()) + 1))
	}

	if resource.GetResourceVersion() != nil &&
		lo.FromPtr(existing.GetResourceVersion()) != lo.FromPtr(resource.GetResourceVersion()) {
		return false, flterrors.ErrResourceVersionConflict
	}

	resource.SetResourceVersion(lo.ToPtr(lo.FromPtr(existing.GetResourceVersion()) + 1))

	selectFields := []string{"spec"}
	if resource.GetKind() == api.DeviceKind {
		selectFields = append(selectFields, "alias")
	}
	selectFields = append(selectFields, s.getNonNilFieldsFromResource(resource)...)
	selectFields = append(selectFields, fieldsToUnset...)

	query := s.db.WithContext(ctx).Model(resource).
		Where("org_id = ? AND name = ? AND (resource_version IS NULL OR resource_version = ?)",
			resource.GetOrgID(),
			resource.GetName(),
			lo.FromPtr(existing.GetResourceVersion())).
		Select(selectFields)

	result := query.Updates(resource)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *GenericStore[P, M, A, ML, AL]) getNonNilFieldsFromResource(resource P) []string {
	ret := []string{}
	if resource.GetGeneration() != nil {
		ret = append(ret, "generation")
	}
	if resource.GetLabels() != nil {
		ret = append(ret, "labels")
	}
	if resource.GetOwner() != nil {
		ret = append(ret, "owner")
	}
	if resource.GetAnnotations() != nil {
		ret = append(ret, "annotations")
	}
	if resource.GetResourceVersion() != nil {
		ret = append(ret, "resource_version")
	}

	return ret
}

func (s *GenericStore[P, M, A, ML, AL]) Get(ctx context.Context, orgId uuid.UUID, name string) (*A, error) {
	resource, err := s.getModel(ctx, orgId, name)
	if err != nil {
		return nil, err
	}
	apiResource, err := s.modelPtrToAPI(s.modelPtrToModel(resource))
	return apiResource, err
}

func (s *GenericStore[P, M, A, ML, AL]) getModel(ctx context.Context, orgId uuid.UUID, name string) (*M, error) {
	var resource M
	result := s.db.WithContext(ctx).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgId, name).First(&resource)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	return &resource, nil
}

func (s *GenericStore[P, M, A, ML, AL]) Delete(ctx context.Context, resource M, callback func(before, after *M), associatedResources ...Resource) error {
	if len(associatedResources) == 0 {
		return s.delete(ctx, resource, callback)
	}
	return s.deleteWithAssociated(ctx, resource, callback, associatedResources...)
}

func (s *GenericStore[P, M, A, ML, AL]) delete(ctx context.Context, resource M, callback func(before, after *M)) error {
	result := s.db.WithContext(ctx).Unscoped().Where("spec IS NOT NULL").Delete(&resource)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil
	}

	if callback != nil {
		callback(&resource, nil)
	}
	return nil
}

func (s *GenericStore[P, M, A, ML, AL]) deleteWithAssociated(ctx context.Context, resource M, callback func(before, after *M), associatedResources ...Resource) error {
	deleted := false
	err := s.db.WithContext(ctx).Transaction(func(innerTx *gorm.DB) (err error) {
		result := innerTx.Unscoped().Delete(&resource)
		if result.Error != nil {
			return ErrorFromGormError(err)
		}
		if result.RowsAffected != 0 {
			deleted = true
		}

		for _, associatedResource := range associatedResources {
			queryStr := fmt.Sprintf(`DELETE FROM %s WHERE org_id = ? AND name = ? AND spec IS NOT NULL`, associatedResource.Table)
			if err := s.db.WithContext(ctx).Raw(queryStr, associatedResource.OrgID, associatedResource.Name).Error; err != nil {
				return ErrorFromGormError(err)
			}
		}

		return nil
	})

	if err != nil {
		return err
	}

	if deleted && callback != nil {
		callback(&resource, nil)
	}
	return nil
}

func (s *GenericStore[P, M, A, ML, AL]) DeleteAll(ctx context.Context, orgId uuid.UUID, callback func(orgId uuid.UUID)) error {
	var resource M
	result := s.db.WithContext(ctx).Unscoped().Where("org_id = ? AND spec IS NOT NULL", orgId).Delete(&resource)

	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}
	if callback != nil {
		callback(orgId)
	}

	return nil
}

func (s *GenericStore[P, M, A, ML, AL]) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *A) (*A, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}

	model, err := s.apiToModelPtr(resource)
	if err != nil {
		return nil, err
	}

	json, err := model.GetStatusAsJson()
	if err != nil {
		return nil, err
	}

	result := s.db.WithContext(ctx).Model(&model).Where("org_id = ? AND name = ?", orgId, model.GetName()).Updates(
		map[string]interface{}{
			"status":           json,
			"resource_version": gorm.Expr("resource_version + 1"),
		})
	return resource, ErrorFromGormError(result.Error)
}

func (s *GenericStore[P, M, A, ML, AL]) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*AL, error) {
	var resourceList ML
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitParamOutOfBounds
	}

	var resource M
	query, err := ListQuery(&resource).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Find(&resourceList)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && resourceList.Length() > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    resourceList.GetItem(resourceList.Length() - 1).GetName(),
			Version: CurrentContinueVersion,
		}
		resourceList.RemoveLast()

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, listParams)
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

	apiList, err := s.listModelToAPI(resourceList, nextContinue, numRemaining)
	return &apiList, err
}
