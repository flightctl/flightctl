package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

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

type Fleet interface {
	Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error)
	Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams, opts ...ListOption) (*api.FleetList, error)
	Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*api.Fleet, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, bool, error)
	CreateOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, fleets ...*api.Fleet) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error)
	UpdateStatusMultiple(ctx context.Context, orgId uuid.UUID, fleets ...*api.Fleet) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback FleetStoreAllDeletedCallback) error
	Delete(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, names ...string) error
	UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
	UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error
	ListIgnoreOrg() ([]model.Fleet, error)
	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)
	InitialMigration() error
}

type FleetStore struct {
	db  *gorm.DB
	log logrus.FieldLogger
}

type FleetStoreCallback func(before *model.Fleet, after *model.Fleet)
type FleetStoreAllDeletedCallback func(orgId uuid.UUID)

// Make sure we conform to Fleet interface
var _ Fleet = (*FleetStore)(nil)

func NewFleet(db *gorm.DB, log logrus.FieldLogger) Fleet {
	return &FleetStore{db: db, log: log}
}

func (s *FleetStore) InitialMigration() error {
	return s.db.AutoMigrate(&model.Fleet{})
}

func (s *FleetStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error) {
	updatedResource, _, _, err := s.createOrUpdate(orgId, resource, ModeCreateOnly, callback)
	return updatedResource, err
}

func (s *FleetStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error) {
	updatedResource, _, err := retryCreateOrUpdate(func() (*api.Fleet, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeUpdateOnly, callback)
	})
	return updatedResource, err
}

type ListOption func(*listOptions)

type listOptions struct {
	withDeviceCount bool
}

func WithDeviceCount(val bool) ListOption {
	return func(o *listOptions) {
		o.withDeviceCount = val
	}
}

type fleetWithCount struct {
	model.Fleet
	DeviceCount int
}

func fleetSelectStr(withDeviceCount bool) string {
	return lo.Ternary(withDeviceCount,
		fmt.Sprintf("*, (select count(*) from devices where org_id = fleets.org_id and owner = CONCAT('%s/', fleets.name)) as device_count", model.FleetKind),
		"*")
}

func (s *FleetStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams, opts ...ListOption) (*api.FleetList, error) {
	var fleetsWithCount []fleetWithCount
	var nextContinue *string
	var numRemaining *int64
	var options listOptions
	lo.ForEach(opts, func(opt ListOption, _ int) { opt(&options) })
	dbModel := s.db.Table("fleets").Select(fleetSelectStr(options.withDeviceCount))
	query := BuildBaseListQuery(dbModel, orgId, listParams)
	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue)
	}
	result := query.Scan(&fleetsWithCount)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(fleetsWithCount) > listParams.Limit {
		nextContinueStruct := Continue{
			Name:    fleetsWithCount[len(fleetsWithCount)-1].Name,
			Version: CurrentContinueVersion,
		}
		fleetsWithCount = fleetsWithCount[:len(fleetsWithCount)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery := BuildBaseListQuery(s.db.Model(&model.Fleet{}), orgId, listParams)
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Name)
		}
		nextContinueStruct.Count = numRemainingVal
		contByte, _ := json.Marshal(nextContinueStruct)
		contStr := b64.StdEncoding.EncodeToString(contByte)
		nextContinue = &contStr
		numRemaining = &numRemainingVal
	}
	fleets := model.FleetList(lo.Map(fleetsWithCount, func(f fleetWithCount, _ int) model.Fleet {
		if options.withDeviceCount {
			if f.Fleet.Status.Data.DevicesSummary == nil {
				f.Fleet.Status.Data.DevicesSummary = &api.DevicesSummary{}
			}
			f.Fleet.Status.Data.DevicesSummary.Total = f.DeviceCount
		}
		return f.Fleet
	}))
	apiFleetList := fleets.ToApiResource(nextContinue, numRemaining)
	return &apiFleetList, flterrors.ErrorFromGormError(result.Error)
}

// A method to get all Fleets regardless of ownership. Used internally by the DeviceUpdater.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *FleetStore) ListIgnoreOrg() ([]model.Fleet, error) {
	var fleets model.FleetList

	result := s.db.Model(&fleets).Find(&fleets)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	}
	return fleets, nil
}

func (s *FleetStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback FleetStoreAllDeletedCallback) error {
	condition := model.Fleet{}
	result := s.db.Unscoped().Where("org_id = ?", orgId).Delete(&condition)
	if result.Error == nil {
		callback(orgId)
	}
	return flterrors.ErrorFromGormError(result.Error)
}

type GetOption func(*getOptions)

type getOptions struct {
	withSummary bool
}

func WithSummary(val bool) GetOption {
	return func(o *getOptions) {
		o.withSummary = val
	}
}

func (s *FleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*api.Fleet, error) {
	options := getOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	var fleet fleetWithCount
	result := s.db.Table("fleets").Where("org_id = ? and name = ?", orgId, name).
		Select(fleetSelectStr(true)).
		Scan(&fleet)
	if result.Error != nil {
		return nil, flterrors.ErrorFromGormError(result.Error)
	} else if result.RowsAffected == 0 {
		return nil, flterrors.ErrResourceNotFound
	}

	summary := api.DevicesSummary{
		Total: fleet.DeviceCount,
	}
	if options.withSummary {
		var err error
		summary.SummaryStatus, err = s.getDeviceSummary(ctx, orgId, name, "summary")
		if err != nil {
			return nil, flterrors.ErrorFromGormError(err)
		}

		summary.UpdateStatus, err = s.getDeviceSummary(ctx, orgId, name, "updated")
		if err != nil {
			return nil, flterrors.ErrorFromGormError(err)
		}
	}

	apiFleet := fleet.ToApiResource(model.WithSummary(&summary))
	return &apiFleet, nil
}

type StatusCount struct {
	Status string
	Count  int
}

func (s *FleetStore) getDeviceSummary(ctx context.Context, orgId uuid.UUID, fleetName string, summaryField string) (map[string]int, error) {
	queryStr := `
	SELECT count(*) as count, status::jsonb->'%s'->>'status' as status
	FROM devices
	WHERE owner = '%s' AND org_id = '%s'
	GROUP BY status::jsonb->'%s'->>'status'`
	summaryQueryStr := fmt.Sprintf(queryStr, summaryField, *util.SetResourceOwner(model.FleetKind, fleetName), orgId, summaryField)

	var statusCounts []StatusCount
	if err := s.db.WithContext(ctx).Raw(summaryQueryStr).Scan(&statusCounts).Error; err != nil {
		return nil, err
	}
	return lo.SliceToMap(statusCounts, func(s StatusCount) (string, int) { return s.Status, s.Count }), nil
}

func (s *FleetStore) createFleet(fleet *model.Fleet) (bool, error) {
	if fleet.Spec.Data.Template.Metadata == nil {
		fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
	}
	fleet.Spec.Data.Template.Metadata.Generation = lo.ToPtr[int64](1)
	fleet.Generation = lo.ToPtr[int64](1)
	fleet.ResourceVersion = lo.ToPtr[int64](1)
	if result := s.db.Create(fleet); result.Error != nil {
		err := flterrors.ErrorFromGormError(result.Error)
		return err == flterrors.ErrDuplicateName, err
	}
	return false, nil
}

func (s *FleetStore) updateFleet(existingRecord, fleet *model.Fleet) (bool, error) {
	if existingRecord.Owner != nil && *existingRecord.Owner != lo.FromPtr(fleet.Owner) {
		return false, flterrors.ErrUpdatingResourceWithOwnerNotAllowed
	}
	if fleet.ResourceVersion != nil && lo.FromPtr(existingRecord.ResourceVersion) != lo.FromPtr(fleet.ResourceVersion) {
		return false, flterrors.ErrResourceVersionConflict
	}

	sameSpec := reflect.DeepEqual(existingRecord.Spec, fleet.Spec)

	// Update the generation if the spec was updated
	fleet.Generation = lo.Ternary(!sameSpec, lo.ToPtr(lo.FromPtr(existingRecord.Generation)+1), existingRecord.Generation)

	sameTemplateSpec := reflect.DeepEqual(existingRecord.Spec.Data.Template.Spec, fleet.Spec.Data.Template.Spec)
	if fleet.Spec.Data.Template.Metadata == nil {
		fleet.Spec.Data.Template.Metadata = &api.ObjectMeta{}
	}
	var existingMetadataGeneration int64
	if existingRecord.Spec.Data.Template.Metadata != nil {
		existingMetadataGeneration = lo.FromPtr(existingRecord.Spec.Data.Template.Metadata.Generation)
	}
	fleet.Spec.Data.Template.Metadata.Generation = lo.Ternary(!sameTemplateSpec, lo.ToPtr(existingMetadataGeneration+1), lo.ToPtr(existingMetadataGeneration))

	fleet.ResourceVersion = lo.ToPtr(lo.FromPtr(existingRecord.ResourceVersion) + 1)

	query := s.db.Model(&model.Fleet{}).Where("org_id = ? and name = ? and resource_version = ?", fleet.OrgID, fleet.Name, lo.FromPtr(existingRecord.ResourceVersion))

	selectFields := []string{"spec"}
	selectFields = append(selectFields, GetNonNilFieldsFromResource(fleet.Resource)...)
	query = query.Select(selectFields)
	result := query.Updates(&fleet)
	if result.Error != nil {
		return false, flterrors.ErrorFromGormError(result.Error)
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *FleetStore) createOrUpdate(orgId uuid.UUID, resource *api.Fleet, mode CreateOrUpdateMode, callback FleetStoreCallback) (*api.Fleet, bool, bool, error) {
	if resource == nil {
		return nil, false, false, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, false, false, flterrors.ErrResourceNameIsNil
	}

	fleet, err := model.NewFleetFromApiResource(resource)
	if err != nil {
		return nil, false, false, err
	}
	fleet.OrgID = orgId

	// Use the dedicated API to update annotations
	fleet.Annotations = nil

	fleet.Owner = resource.Metadata.Owner

	existingRecord, err := getExistingRecord[model.Fleet](s.db, fleet.Name, orgId)
	if err != nil {
		return nil, false, false, err
	}
	exists := existingRecord != nil

	if exists && mode == ModeCreateOnly {
		return nil, false, false, flterrors.ErrDuplicateName
	}
	if !exists && mode == ModeUpdateOnly {
		return nil, false, false, flterrors.ErrResourceNotFound
	}

	if !exists {
		if retry, err := s.createFleet(fleet); err != nil {
			return nil, false, retry, err
		}
	} else {
		if retry, err := s.updateFleet(existingRecord, fleet); err != nil {
			return nil, false, retry, err
		}
	}

	callback(existingRecord, fleet)

	updatedResource := fleet.ToApiResource()
	return &updatedResource, !exists, false, nil
}

func (s *FleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, bool, error) {
	return retryCreateOrUpdate(func() (*api.Fleet, bool, bool, error) {
		return s.createOrUpdate(orgId, resource, ModeCreateOrUpdate, callback)
	})
}

func (s *FleetStore) CreateOrUpdateMultiple(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, resources ...*api.Fleet) error {
	var errs []error
	for _, resource := range resources {
		_, _, err := s.CreateOrUpdate(ctx, orgId, resource, callback)
		if err == flterrors.ErrUpdatingResourceWithOwnerNotAllowed {
			err = fmt.Errorf("one or more fleets are managed by a different resource. %w", err)
		}
		errs = append(errs, err)
	}
	return errors.Join(lo.Uniq(errs)...)
}

func (s *FleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	return s.updateStatus(s.db, orgId, resource)
}

func (s *FleetStore) UpdateStatusMultiple(ctx context.Context, orgId uuid.UUID, resources ...*api.Fleet) error {
	var errs []error
	for _, resource := range resources {
		_, err := s.updateStatus(s.db, orgId, resource)
		errs = append(errs, err)
	}
	return errors.Join(lo.Uniq(errs)...)
}

func (s *FleetStore) updateStatus(tx *gorm.DB, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	if resource == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	if resource.Metadata.Name == nil {
		return nil, flterrors.ErrResourceNameIsNil
	}
	fleet := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Name: *resource.Metadata.Name},
	}
	result := tx.Model(&fleet).Updates(map[string]interface{}{
		"status":           model.MakeJSONField(resource.Status),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return resource, flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	db := s.db
	if tx != nil {
		db = tx
	}
	fleetCondition := model.Fleet{
		Resource: model.Resource{OrgID: orgId, Owner: &owner},
	}
	result := db.Model(fleetCondition).Where("org_id = ? and owner = ?", orgId, owner).Updates(map[string]interface{}{
		"owner":            nil,
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error {
	db := s.db
	if tx != nil {
		db = tx
	}
	fleetCondition := model.Fleet{
		Resource: model.Resource{OrgID: orgId},
	}
	result := db.Model(model.Fleet{}).Where(fleetCondition).Where("owner like ?", "%"+resourceKind+"/%").Updates(map[string]interface{}{
		"owner":            nil,
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	return flterrors.ErrorFromGormError(result.Error)
}

func (s *FleetStore) Delete(ctx context.Context, orgId uuid.UUID, callback FleetStoreCallback, names ...string) error {
	deleted := []model.Fleet{}
	if err := s.db.Raw(`delete from fleets where org_id = ? and name in (?) returning *`, orgId, names).Scan(&deleted).Error; err != nil {
		return flterrors.ErrorFromGormError(err)
	}
	for i := range deleted {
		callback(&deleted[i], nil)
	}
	return nil
}

func (s *FleetStore) updateConditions(orgId uuid.UUID, name string, conditions []api.Condition) (bool, error) {
	existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, flterrors.ErrorFromGormError(result.Error)
	}

	if existingRecord.Status == nil {
		existingRecord.Status = model.MakeJSONField(api.FleetStatus{})
	}
	if existingRecord.Status.Data.Conditions == nil {
		existingRecord.Status.Data.Conditions = []api.Condition{}
	}
	changed := false
	for _, condition := range conditions {
		changed = api.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition)
	}
	if !changed {
		return false, nil
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"status":           existingRecord.Status,
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	err := flterrors.ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *FleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error {
	return retryUpdate(func() (bool, error) {
		return s.updateConditions(orgId, name, conditions)
	})
}

func (s *FleetStore) updateAnnotations(orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) (bool, error) {
	existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, flterrors.ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.LabelArrayToMap(existingRecord.Annotations)
	existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

	for _, deleteKey := range deleteKeys {
		delete(existingAnnotations, deleteKey)
	}
	annotationsArray := util.LabelMapToArray(&existingAnnotations)

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":      pq.StringArray(annotationsArray),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	err := flterrors.ErrorFromGormError(result.Error)
	if err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *FleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error {
	return retryUpdate(func() (bool, error) {
		return s.updateAnnotations(orgId, name, annotations, deleteKeys)
	})
}

func (s *FleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	repos := []model.Repository{}
	for _, repoName := range repositoryNames {
		repos = append(repos, model.Repository{Resource: model.Resource{OrgID: orgId, Name: repoName}})
	}
	return s.db.Transaction(func(innerTx *gorm.DB) error {
		fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
		if err := innerTx.Model(&fleet).Association("Repositories").Replace(repos); err != nil {
			return flterrors.ErrorFromGormError(err)
		}
		return nil
	})
}

func (s *FleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error) {
	fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	var repos model.RepositoryList
	err := s.db.Model(&fleet).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, flterrors.ErrorFromGormError(err)
	}
	repositories, err := repos.ToApiResource(nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}
