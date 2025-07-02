package store

import (
	"context"
	"errors"
	"slices"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type IntegrationTestCallback func()

// GenericStore provides generic CRUD operations for resources
// P is a pointer to a model, for example: *model.Device
// M is the model, for example: model.Device
// A is the API resource, for example: api.Device
// AL is the API list, for example: api.DeviceList
type Model interface {
	model.CertificateSigningRequest | model.Device | model.EnrollmentRequest | model.Fleet | model.Repository | model.ResourceSync | model.TemplateVersion | model.Event
}
type extInt[M any] interface {
	model.ResourceInterface
	*M
}
type GenericStore[P extInt[M], M Model, A any, AL any] struct {
	dbHandler *gorm.DB
	log       logrus.FieldLogger

	// Conversion functions between API and model types
	apiToModelPtr  func(*A) (P, error)
	modelPtrToAPI  func(P, ...model.APIResourceOption) (*A, error)
	listModelToAPI func([]M, *string, *int64) (AL, error)

	// Callback for integration tests to inject logic
	IntegrationTestCreateOrUpdateCallback IntegrationTestCallback
}

type Resource struct {
	Table string
	OrgID string
	Name  string
}

func NewGenericStore[P extInt[M], M Model, A any, AL any](
	db *gorm.DB,
	log logrus.FieldLogger,
	apiToModelPtr func(*A) (P, error),
	modelPtrToAPI func(P, ...model.APIResourceOption) (*A, error),
	listModelToAPI func([]M, *string, *int64) (AL, error),
) *GenericStore[P, M, A, AL] {
	return &GenericStore[P, M, A, AL]{
		dbHandler:                             db,
		log:                                   log,
		apiToModelPtr:                         apiToModelPtr,
		modelPtrToAPI:                         modelPtrToAPI,
		listModelToAPI:                        listModelToAPI,
		IntegrationTestCreateOrUpdateCallback: func() {},
	}
}

func (s *GenericStore[P, M, A, AL]) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *GenericStore[P, M, A, AL]) Create(ctx context.Context, orgId uuid.UUID, resource *A, callback func(ctx context.Context, orgId uuid.UUID, before, after *A)) (*A, error) {
	updated, _, _, _, err := s.createOrUpdate(ctx, orgId, resource, nil, true, ModeCreateOnly, nil, callback)
	return updated, err
}

func (s *GenericStore[P, M, A, AL]) Update(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, validationCallback func(ctx context.Context, before, after *A) error, callback func(ctx context.Context, orgId uuid.UUID, before, after *A)) (*A, api.ResourceUpdatedDetails, error) {
	updated, _, updateDesc, err := retryCreateOrUpdate(func() (*A, bool, bool, api.ResourceUpdatedDetails, error) {
		return s.createOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, ModeUpdateOnly, validationCallback, callback)
	})
	return updated, updateDesc, err
}

func (s *GenericStore[P, M, A, AL]) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, validationCallback func(ctx context.Context, before, after *A) error, callback func(ctx context.Context, orgId uuid.UUID, before, after *A)) (*A, bool, api.ResourceUpdatedDetails, error) {
	return retryCreateOrUpdate(func() (*A, bool, bool, api.ResourceUpdatedDetails, error) {
		return s.createOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, ModeCreateOrUpdate, validationCallback, callback)
	})
}

func (s *GenericStore[P, M, A, AL]) createOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, mode CreateOrUpdateMode, validationCallback func(ctx context.Context, before, after *A) error, callback func(ctx context.Context, orgId uuid.UUID, before, after *A)) (*A, bool, bool, api.ResourceUpdatedDetails, error) {
	var updateDesc api.ResourceUpdatedDetails
	if resource == nil {
		return nil, false, false, updateDesc, flterrors.ErrResourceIsNil
	}

	modelInst, err := s.apiToModelPtr(resource)
	if err != nil {
		return nil, false, false, updateDesc, err
	}
	modelInst.SetOrgID(orgId)
	if fromAPI {
		modelInst.SetAnnotations(nil)
	}

	existing, err := s.getExistingResource(ctx, modelInst.GetName(), orgId)
	if err != nil {
		return nil, false, false, updateDesc, err
	}
	exists := existing != nil
	creating := !exists || P(existing).HasNilSpec()

	var existingAPIResource *A
	if existing != nil {
		existingAPIResource, err = s.modelPtrToAPI(existing)
		if err != nil {
			return nil, creating, false, updateDesc, err
		}
	}

	if validationCallback != nil {
		modifiedAPIResource, err := s.modelPtrToAPI(modelInst)
		if err != nil {
			return nil, creating, false, updateDesc, err
		}

		err = validationCallback(ctx, existingAPIResource, modifiedAPIResource)
		if err != nil {
			return nil, creating, false, updateDesc, err
		}
	}

	if !creating && mode == ModeCreateOnly {
		return nil, creating, false, updateDesc, flterrors.ErrDuplicateName
	}

	if creating && mode == ModeUpdateOnly {
		return nil, creating, false, updateDesc, flterrors.ErrResourceNotFound
	}

	s.IntegrationTestCreateOrUpdateCallback()

	var retry bool
	if !exists {
		retry, err = s.createResource(ctx, modelInst)
	} else {
		retry, updateDesc, err = s.updateResource(ctx, fromAPI, existing, modelInst, fieldsToUnset)
	}
	if err != nil {
		return nil, creating, retry, updateDesc, err
	}

	if callback != nil {
		modifiedAPIResource, err := s.modelPtrToAPI(modelInst)
		if err != nil {
			return nil, creating, false, updateDesc, err
		}

		callback(ctx, orgId, existingAPIResource, modifiedAPIResource)
	}

	apiResource, err := s.modelPtrToAPI(modelInst)

	return apiResource, creating, false, updateDesc, err
}

func (s *GenericStore[P, M, A, AL]) getExistingResource(ctx context.Context, name string, orgId uuid.UUID) (*M, error) {
	var existingResource M
	if err := s.getDB(ctx).Where("name = ? and org_id = ?", name, orgId).First(&existingResource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrorFromGormError(err)
	}
	return &existingResource, nil
}

func (s *GenericStore[P, M, A, AL]) createResource(ctx context.Context, resource P) (bool, error) {
	resource.SetGeneration(lo.ToPtr(int64(1)))
	resource.SetResourceVersion(lo.ToPtr(int64(1)))

	result := s.getDB(ctx).Create(resource)
	if result.Error != nil {
		err := ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *GenericStore[P, M, A, AL]) updateResource(ctx context.Context, fromAPI bool, existing, resource P, fieldsToUnset []string) (bool, api.ResourceUpdatedDetails, error) {
	updateDesc := api.ResourceUpdatedDetails{}
	sameSpec := resource.HasSameSpecAs(existing)
	if !sameSpec {
		if fromAPI {
			hasOwner := len(lo.FromPtr(existing.GetOwner())) != 0
			if hasOwner {
				// Don't let the user update the spec if it has an owner
				return false, updateDesc, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
			}
		}

		// Update the generation if the spec was updated
		resource.SetGeneration(lo.ToPtr(lo.FromPtr(existing.GetGeneration()) + 1))
	}

	if resource.GetResourceVersion() != nil &&
		lo.FromPtr(existing.GetResourceVersion()) != lo.FromPtr(resource.GetResourceVersion()) {
		return false, updateDesc, flterrors.ErrResourceVersionConflict
	}

	resource.SetResourceVersion(lo.ToPtr(lo.FromPtr(existing.GetResourceVersion()) + 1))

	selectFields := []string{"spec"}
	if resource.GetKind() == api.DeviceKind {
		selectFields = append(selectFields, "alias")
	}
	selectFields = append(selectFields, resource.GetNonNilFieldsFromResource()...)
	selectFields = append(selectFields, fieldsToUnset...)

	if !sameSpec && slices.Contains(selectFields, "spec") {
		updateDesc.UpdatedFields = append(updateDesc.UpdatedFields, api.Spec)
	}
	if !resource.GetLabels().Equals(existing.GetLabels()) && slices.Contains(selectFields, "labels") {
		updateDesc.UpdatedFields = append(updateDesc.UpdatedFields, api.Labels)
	}
	if !util.StringsAreEqual(resource.GetOwner(), existing.GetOwner()) && slices.Contains(selectFields, "owner") {
		updateDesc.UpdatedFields = append(updateDesc.UpdatedFields, api.Owner)
	}

	query := s.getDB(ctx).Model(resource).
		Where("org_id = ? AND name = ? AND (resource_version IS NULL OR resource_version = ?)",
			resource.GetOrgID(),
			resource.GetName(),
			lo.FromPtr(existing.GetResourceVersion())).
		Select(selectFields)

	result := query.Clauses(clause.Returning{}).Updates(resource)
	if result.Error != nil {
		return false, updateDesc, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, updateDesc, flterrors.ErrNoRowsUpdated
	}
	return false, updateDesc, nil
}

func (s *GenericStore[P, M, A, AL]) Get(ctx context.Context, orgId uuid.UUID, name string) (*A, error) {
	var resource M
	result := s.getDB(ctx).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgId, name).First(&resource)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	apiResource, err := s.modelPtrToAPI(&resource)
	return apiResource, err
}

func (s *GenericStore[P, M, A, AL]) Delete(ctx context.Context, resource M, callback func(ctx context.Context, orgId uuid.UUID, before, after *A), associatedResources ...Resource) (bool, error) {
	var deleted bool
	var err error
	if len(associatedResources) == 0 {
		deleted, err = s.delete(ctx, resource)
	} else {
		deleted, err = s.deleteWithAssociated(ctx, resource, associatedResources...)
	}
	if err != nil {
		return false, err
	}

	if deleted && callback != nil {
		apiResource, err := s.modelPtrToAPI(&resource)
		if err != nil {
			return false, err
		}
		callback(ctx, P(&resource).GetOrgID(), apiResource, nil)
	}
	return deleted, nil
}

func (s *GenericStore[P, M, A, AL]) delete(ctx context.Context, resource M) (bool, error) {
	result := s.getDB(ctx).Unscoped().Where("spec IS NOT NULL").Delete(&resource)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return false, nil
	}

	return true, nil
}

func (s *GenericStore[P, M, A, AL]) deleteWithAssociated(ctx context.Context, resource M, associatedResources ...Resource) (bool, error) {
	deleted := false
	err := s.getDB(ctx).Transaction(func(innerTx *gorm.DB) (err error) {
		result := innerTx.Unscoped().Delete(&resource)
		if result.Error != nil {
			return ErrorFromGormError(result.Error)
		}
		if result.RowsAffected != 0 {
			deleted = true
		}

		for _, associatedResource := range associatedResources {
			if err := innerTx.Unscoped().
				Table(associatedResource.Table).
				Where("org_id = ? AND name = ? AND spec IS NOT NULL", associatedResource.OrgID, associatedResource.Name).
				Delete(nil).Error; err != nil {
				return ErrorFromGormError(err)
			}
		}

		return nil
	})

	return deleted, err
}

func (s *GenericStore[P, M, A, AL]) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *A) (*A, error) {
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

	result := s.getDB(ctx).Model(model).Where("org_id = ? AND name = ?", orgId, model.GetName()).Clauses(clause.Returning{}).Updates(
		map[string]interface{}{
			"status":           json,
			"resource_version": gorm.Expr("resource_version + 1"),
		})
	if err := ErrorFromGormError(result.Error); err != nil {
		return nil, err
	}

	apiResource, err := s.modelPtrToAPI(model)
	if err != nil {
		return nil, err
	}
	return apiResource, nil
}

func hasSpecColumn[M Model]() bool {
	switch any(new(M)).(type) {
	case *model.Event:
		return false
	default:
		return true
	}
}

func (s *GenericStore[P, M, A, AL]) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*AL, error) {
	var resourceList []M
	var nextContinue *string
	var numRemaining *int64

	var resource M
	query, err := ListQuery(&resource).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}
	if hasSpecColumn[M]() {
		query = query.Where("spec IS NOT NULL")
	}

	result := query.Find(&resourceList)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(resourceList) > listParams.Limit {
		lastIndex := len(resourceList) - 1
		lastItem := resourceList[lastIndex]
		columns, _, _ := getSortColumns(listParams)

		// Build values for continue token
		continueValues := make([]string, len(columns))
		for i, col := range columns {
			switch col {
			case SortByName:
				continueValues[i] = P(&lastItem).GetName()
			case SortByCreatedAt:
				continueValues[i] = P(&lastItem).GetTimestamp().Format(time.RFC3339Nano)
			default:
				// Handle unsupported columns
				continueValues[i] = ""
			}
		}

		resourceList = resourceList[:lastIndex]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(&resource).Build(ctx, s.getDB(ctx), orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = CountRemainingItems(countQuery, continueValues, listParams)
		}

		nextContinue = BuildContinueString(continueValues, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	apiList, err := s.listModelToAPI(resourceList, nextContinue, numRemaining)
	return &apiList, err
}
