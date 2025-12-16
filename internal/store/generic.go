package store

import (
	"context"
	"errors"
	"reflect"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
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
	model.AuthProvider | model.CertificateSigningRequest | model.Device | model.EnrollmentRequest | model.Fleet | model.Repository | model.ResourceSync | model.TemplateVersion | model.Event
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

func (s *GenericStore[P, M, A, AL]) Create(ctx context.Context, orgId uuid.UUID, resource *A) (*A, error) {
	updated, _, _, _, err := s.createOrUpdate(ctx, orgId, resource, nil, true, ModeCreateOnly, nil)
	return updated, err
}

func (s *GenericStore[P, M, A, AL]) Update(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, validationCallback func(ctx context.Context, before, after *A) error) (*A, *A, error) {
	updated, before, _, err := retryCreateOrUpdate(func() (*A, *A, bool, bool, error) {
		return s.createOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, ModeUpdateOnly, validationCallback)
	})
	return updated, before, err
}

func (s *GenericStore[P, M, A, AL]) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, validationCallback func(ctx context.Context, before, after *A) error) (*A, *A, bool, error) {
	return retryCreateOrUpdate(func() (*A, *A, bool, bool, error) {
		return s.createOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, ModeCreateOrUpdate, validationCallback)
	})
}

func (s *GenericStore[P, M, A, AL]) createOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, mode CreateOrUpdateMode, validationCallback func(ctx context.Context, before, after *A) error) (*A, *A, bool, bool, error) {
	if resource == nil {
		return nil, nil, false, false, flterrors.ErrResourceIsNil
	}

	modelInst, err := s.apiToModelPtr(resource)
	if err != nil {
		return nil, nil, false, false, err
	}
	modelInst.SetOrgID(orgId)
	if fromAPI {
		modelInst.SetAnnotations(nil)
	}

	existing, err := s.getExistingResource(ctx, modelInst.GetName(), orgId)
	if err != nil {
		return nil, nil, false, false, err
	}
	exists := existing != nil
	creating := !exists || P(existing).HasNilSpec()

	var existingAPIResource *A
	if existing != nil {
		existingAPIResource, err = s.modelPtrToAPI(existing)
		if err != nil {
			return nil, nil, creating, false, err
		}
	}

	if validationCallback != nil {
		modifiedAPIResource, err := s.modelPtrToAPI(modelInst)
		if err != nil {
			return nil, nil, creating, false, err
		}

		err = validationCallback(ctx, existingAPIResource, modifiedAPIResource)
		if err != nil {
			return nil, nil, creating, false, err
		}
	}

	if !creating && mode == ModeCreateOnly {
		return nil, nil, creating, false, flterrors.ErrDuplicateName
	}

	if creating && mode == ModeUpdateOnly {
		return nil, existingAPIResource, creating, false, flterrors.ErrResourceNotFound
	}

	s.IntegrationTestCreateOrUpdateCallback()

	var retry bool
	if !exists {
		retry, err = s.createResource(ctx, modelInst)
	} else {
		retry, err = s.updateResource(ctx, fromAPI, existing, modelInst, fieldsToUnset)
	}
	if err != nil {
		return nil, existingAPIResource, creating, retry, err
	}

	apiResource, err := s.modelPtrToAPI(modelInst)

	return apiResource, existingAPIResource, creating, false, err
}

func (s *GenericStore[P, M, A, AL]) getExistingResource(ctx context.Context, name string, orgId uuid.UUID) (*M, error) {
	var existingResource M
	if err := s.getDB(ctx).Where("name = ? and org_id = ?", name, orgId).Take(&existingResource).Error; err != nil {
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
		return err == flterrors.ErrDuplicateName || err == flterrors.ErrDuplicateOIDCProvider || err == flterrors.ErrDuplicateOAuth2Provider, err
	}
	return false, nil
}

func (s *GenericStore[P, M, A, AL]) updateResource(ctx context.Context, fromAPI bool, existing, resource P, fieldsToUnset []string) (bool, error) {
	hasOwner := len(lo.FromPtr(existing.GetOwner())) != 0

	// TODO: Move ownership validation checks to the service layer. The store layer should
	// focus on data access, while authorization and business rules belong in the service layer.
	// This allows resource sync to update resources it owns, but ideally this should be
	// handled in service.ReplaceFleet() by checking ownership before calling the store.
	allowResourceSyncUpdate := false
	if rs, ok := ctx.Value(consts.ResourceSyncRequestCtxKey).(bool); ok && rs {
		allowResourceSyncUpdate = true
	}

	sameSpec := resource.HasSameSpecAs(existing)
	if !sameSpec {
		if fromAPI && hasOwner && !allowResourceSyncUpdate {
			// Don't let the user update the spec if it has an owner
			return false, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}

		// Update the generation if the spec was updated
		resource.SetGeneration(lo.ToPtr(lo.FromPtr(existing.GetGeneration()) + 1))
	}

	// Don't let the user update a fleet's labels if it has an owner
	if fromAPI && hasOwner && !allowResourceSyncUpdate && resource.GetKind() == api.FleetKind {
		sameLabels := reflect.DeepEqual(existing.GetLabels(), resource.GetLabels())
		if !sameLabels {
			return false, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
		}
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
	selectFields = append(selectFields, resource.GetNonNilFieldsFromResource()...)
	selectFields = append(selectFields, fieldsToUnset...)

	query := s.getDB(ctx).Model(resource).
		Where("org_id = ? AND name = ? AND (resource_version IS NULL OR resource_version = ?)",
			resource.GetOrgID(),
			resource.GetName(),
			lo.FromPtr(existing.GetResourceVersion())).
		Select(selectFields)

	result := query.Clauses(clause.Returning{}).Updates(resource)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}

	// Merge preserved fields from existing into resource
	// Fields that are nil in resource weren't included in the update (not in selectFields),
	// so they're preserved from existing. Copy them to resource so the returned model
	// accurately reflects the database state.
	// However, don't preserve fields that are explicitly being unset via fieldsToUnset.
	if resource.GetOwner() == nil && !lo.Contains(fieldsToUnset, "owner") {
		resource.SetOwner(existing.GetOwner())
	}
	// Preserve annotations if they were nil (not updated)
	// When fromAPI=true, annotations are set to nil in createOrUpdate to preserve existing ones
	// When fromAPI=false, if annotations are nil, they weren't updated, so preserve from existing
	if resource.GetAnnotations() == nil && !lo.Contains(fieldsToUnset, "annotations") {
		resource.SetAnnotations(existing.GetAnnotations())
	}
	if resource.GetLabels() == nil && !lo.Contains(fieldsToUnset, "labels") {
		resource.SetLabels(existing.GetLabels())
	}

	return false, nil
}

func (s *GenericStore[P, M, A, AL]) Get(ctx context.Context, orgId uuid.UUID, name string) (*A, error) {
	var resource M
	result := s.getDB(ctx).Where("org_id = ? AND name = ? AND spec IS NOT NULL", orgId, name).Take(&resource)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	apiResource, err := s.modelPtrToAPI(&resource)
	return apiResource, err
}

func (s *GenericStore[P, M, A, AL]) Delete(ctx context.Context, resource M, associatedResources ...Resource) (bool, error) {
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
