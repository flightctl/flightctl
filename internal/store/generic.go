package store

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// Resource represents any API resource that can be stored
type GenericResourcePtr interface {
	GetKind() string
	GetName() string
	GetOrgID() uuid.UUID
	SetOrgID(orgId uuid.UUID)
	GetResourceVersion() *int64
	SetResourceVersion(version *int64)
	GetGeneration() *int64
	SetGeneration(generation *int64)
	GetOwner() *string
	SetOwner(owner *string)
	GetLabels() pq.StringArray
	SetLabels(labbels pq.StringArray)
	GetAnnotations() pq.StringArray
	SetAnnotations(annotations pq.StringArray)
	HasSameSpecAs(otherResource any) bool
}

type IntegrationTestCallback func()

// GenericStore provides generic CRUD operations for resources
// P is a pointer to a model, for example: *model.Device
// M is the model, for example: model.Device
// A is the API resource, for example: api.Device
type GenericStore[P GenericResourcePtr, M any, A any] struct {
	db  *gorm.DB
	log logrus.FieldLogger

	// Conversion functions between API and model types
	apiToModelPtr   func(*A) (P, error)
	modelPtrToAPI   func(P, ...model.APIResourceOption) *A
	modelPtrToModel func(*M) P

	// Callback for integration tests to inject logic
	IntegrationTestCreateOrUpdateCallback IntegrationTestCallback
}

func NewGenericStore[P GenericResourcePtr, M any, A any](
	db *gorm.DB,
	log logrus.FieldLogger,
	apiToModelPtr func(*A) (P, error),
	modelPtrToAPI func(P, ...model.APIResourceOption) *A,
	modelToModelPtr func(*M) P,
) *GenericStore[P, M, A] {
	return &GenericStore[P, M, A]{
		db:                                    db,
		log:                                   log,
		apiToModelPtr:                         apiToModelPtr,
		modelPtrToAPI:                         modelPtrToAPI,
		modelPtrToModel:                       modelToModelPtr,
		IntegrationTestCreateOrUpdateCallback: func() {},
	}
}

func (s *GenericStore[P, M, A]) Create(ctx context.Context, orgId uuid.UUID, resource *A, callback func(before, after P)) (*A, error) {
	updated, _, _, err := s.createOrUpdate(orgId, resource, nil, true, ModeCreateOnly, callback)
	return updated, err
}

func (s *GenericStore[P, M, A]) Update(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, callback func(before, after P)) (*A, error) {
	updated, _, err := retryCreateOrUpdate(func() (*A, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, fieldsToUnset, fromAPI, ModeUpdateOnly, callback)
	})
	return updated, err
}

func (s *GenericStore[P, M, A]) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, callback func(before, after P)) (*A, bool, error) {
	return retryCreateOrUpdate(func() (*A, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, fieldsToUnset, fromAPI, ModeCreateOrUpdate, callback)
	})
}

func (s *GenericStore[P, M, A]) createOrUpdate(orgId uuid.UUID, resource *A, fieldsToUnset []string, fromAPI bool, mode CreateOrUpdateMode, callback func(before, after P)) (*A, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}

	model, err := s.apiToModelPtr(resource)
	if err != nil {
		return nil, false, false, err

	}
	model.SetOrgID(orgId)
	model.SetAnnotations(nil)

	existing, err := s.getExistingResource(model.GetName(), orgId)
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
		retry, err = s.createResource(model)
	} else {
		retry, err = s.updateResource(fromAPI, s.modelPtrToModel(existing), model, fieldsToUnset)
	}
	if err != nil {
		return nil, false, retry, err
	}

	if callback != nil {
		callback(s.modelPtrToModel(existing), model)
	}

	apiResource := s.modelPtrToAPI(model)
	return apiResource, !exists, false, nil
}

func (s *GenericStore[P, M, A]) getExistingResource(name string, orgId uuid.UUID) (*M, error) {
	var existingResource M
	if err := s.db.Where("name = ? and org_id = ?", name, orgId).First(&existingResource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, ErrorFromGormError(err)
	}
	return &existingResource, nil
}

func (s *GenericStore[P, M, A]) createResource(resource P) (bool, error) {
	resource.SetGeneration(util.Int64ToPtr(1))
	resource.SetResourceVersion(util.Int64ToPtr(1))

	result := s.db.Create(resource)
	if result.Error != nil {
		err := ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *GenericStore[P, M, A]) updateResource(fromAPI bool, existing, resource P, fieldsToUnset []string) (bool, error) {
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
					existingAnnotations := util.LabelArrayToMap(existing.GetAnnotations())
					if existingAnnotations[api.DeviceAnnotationTemplateVersion] != "" {
						delete(existingAnnotations, api.DeviceAnnotationTemplateVersion)
						annotationsArray := util.LabelMapToArray(&existingAnnotations)
						resource.SetAnnotations(pq.StringArray(annotationsArray))
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

	query := s.db.Model(resource).
		Where("org_id = ? AND name = ? AND resource_version = ?",
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

func (s *GenericStore[P, M, A]) getNonNilFieldsFromResource(resource P) []string {
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

	if resource.GetGeneration() != nil {
		ret = append(ret, "generation")
	}

	if resource.GetResourceVersion() != nil {
		ret = append(ret, "resource_version")
	}

	return ret
}

func (s *GenericStore[P, M, A]) Get(ctx context.Context, orgId uuid.UUID, name string) (*A, error) {
	var resource M
	result := s.db.Where("org_id = ? AND name = ?", orgId, name).First(&resource)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	apiResource := s.modelPtrToAPI(s.modelPtrToModel(&resource))
	return apiResource, nil
}

/*
func (s *GenericStore[P, M, A]) Delete(ctx context.Context, orgId uuid.UUID, name string) error {
	var existingRecord M
	result := s.db.Where("org_id = ? AND name = ?", orgId, name).First(&existingRecord)
	if result.Error != nil {
		if result.Error == gorm.ErrRecordNotFound {
			return nil
		}
		return ErrorFromGormError(result.Error)
	}

	if err := s.db.Delete(&existingRecord).Error; err != nil {
		return ErrorFromGormError(err)
	}

	if s.resourceCallback != nil {
		s.resourceCallback(&existingRecord, nil)
	}

	return nil
}*/

/*
func (s *GenericStore[P, M, A]) List(ctx context.Context, orgId uuid.UUID, listParams ListParams) (*api.ResourceList[A], error) {
	var resources []M
	var nextContinue *string
	var numRemaining *int64

	if listParams.Limit < 0 {
		return nil, flterrors.ErrLimitParamOutOfBounds
	}

	query, err := ListQuery(new(M)).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}

	if listParams.Limit > 0 {
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}

	result := query.Find(&resources)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}

	if listParams.Limit > 0 && len(resources) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    resources[len(resources)-1].GetName(),
			Version: CurrentContinueVersion,
		}
		resources = resources[:len(resources)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := ListQuery(new(M)).Build(ctx, s.db, orgId, listParams)
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

	items := make([]A, len(resources))
	for i, r := range resources {
		items[i] = s.toAPI(r)
	}

	return &api.ResourceList[A]{
		Items:     items,
		Continue:  nextContinue,
		Remaining: numRemaining,
	}, nil
}


*/
