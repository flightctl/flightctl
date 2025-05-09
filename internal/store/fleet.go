package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

type Fleet interface {
	InitialMigration() error

	Create(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error)
	Update(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback FleetStoreCallback) (*api.Fleet, api.ResourceUpdatedDetails, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet, fieldsToUnset []string, fromAPI bool, callback FleetStoreCallback) (*api.Fleet, bool, api.ResourceUpdatedDetails, error)
	Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*api.Fleet, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams, opts ...ListOption) (*api.FleetList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, callback FleetStoreCallback) error
	DeleteAll(ctx context.Context, orgId uuid.UUID, callback FleetStoreAllDeletedCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *api.Fleet) (*api.Fleet, error)

	ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*api.FleetList, error)
	ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*api.FleetList, error)
	UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
	UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error
	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []api.Condition) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error)
}

type FleetStore struct {
	db           *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.Fleet, model.Fleet, api.Fleet, api.FleetList]
}

type FleetStoreCallback func(orgId uuid.UUID, before *api.Fleet, after *api.Fleet)
type FleetStoreAllDeletedCallback func(orgId uuid.UUID)

// Make sure we conform to Fleet interface
var _ Fleet = (*FleetStore)(nil)

func NewFleet(db *gorm.DB, log logrus.FieldLogger) Fleet {
	genericStore := NewGenericStore[*model.Fleet, model.Fleet, api.Fleet, api.FleetList](
		db,
		log,
		model.NewFleetFromApiResource,
		(*model.Fleet).ToApiResource,
		model.FleetsToApiResource,
	)
	return &FleetStore{db: db, log: log, genericStore: genericStore}
}

func (s *FleetStore) InitialMigration() error {
	if err := s.db.AutoMigrate(&model.Fleet{}); err != nil {
		return err
	}

	// Create GIN index for Fleet labels
	if !s.db.Migrator().HasIndex(&model.Fleet{}, "idx_fleet_labels") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_fleet_labels ON fleets USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Fleet{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for Fleet annotations
	if !s.db.Migrator().HasIndex(&model.Fleet{}, "idx_fleet_annotations") {
		if s.db.Dialector.Name() == "postgres" {
			if err := s.db.Exec("CREATE INDEX idx_fleet_annotations ON fleets USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := s.db.Migrator().CreateIndex(&model.Fleet{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *FleetStore) Create(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, callback FleetStoreCallback) (*api.Fleet, error) {
	return s.genericStore.Create(ctx, orgId, resource, callback)
}

func (s *FleetStore) Update(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, fieldsToUnset []string, fromAPI bool, callback FleetStoreCallback) (*api.Fleet, api.ResourceUpdatedDetails, error) {
	return s.genericStore.Update(ctx, orgId, resource, fieldsToUnset, fromAPI, nil, callback)
}

func (s *FleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *api.Fleet, fieldsToUnset []string, fromAPI bool, callback FleetStoreCallback) (*api.Fleet, bool, api.ResourceUpdatedDetails, error) {
	return s.genericStore.CreateOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, nil, callback)
}

type GetOption func(*getOptions)

type getOptions struct {
	withDeviceSummary bool
}

func GetWithDeviceSummary(val bool) GetOption {
	return func(o *getOptions) {
		o.withDeviceSummary = val
	}
}

func (s *FleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*api.Fleet, error) {
	options := getOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	var fleet fleetWithCount

	result := s.db.Table("fleets").Where("org_id = ? and name = ?", orgId, name).
		Select(fleetSelectStr(options.withDeviceSummary)).
		Scan(&fleet)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	} else if result.RowsAffected == 0 {
		return nil, flterrors.ErrResourceNotFound
	}

	var summary *api.DevicesSummary // Remains nil unless withDeviceSummary is true; will be omitted in JSON if not set

	if options.withDeviceSummary {
		summary = &api.DevicesSummary{Total: fleet.DeviceCount}
		err := s.addStatusSummary(ctx, orgId, name, summary)
		if err != nil {
			return nil, err
		}
	}

	// Passing summary (nil if not set), handled downstream
	apiFleet, _ := fleet.ToApiResource(model.WithDevicesSummary(summary))
	return apiFleet, nil
}

func (s *FleetStore) addStatusSummary(ctx context.Context, orgId uuid.UUID, fleetName string, summary *api.DevicesSummary) error {
	fs, err := selector.NewFieldSelectorFromMap(
		map[string]string{"metadata.owner": util.ResourceOwner(api.FleetKind, fleetName)})
	if err != nil {
		return err
	}
	deviceQuery, err := ListQuery(&model.Device{}).Build(ctx, s.db, orgId, ListParams{FieldSelector: fs})
	if err != nil {
		return err
	}

	statusCount, err := CountStatusList(ctx, deviceQuery,
		"status.applicationsSummary.status",
		"status.summary.status",
		"status.updated.status")
	if err != nil {
		return ErrorFromGormError(err)
	}

	applicationStatus := statusCount.List("status.applicationsSummary.status")
	summary.ApplicationStatus = applicationStatus

	summaryStatus := statusCount.List("status.summary.status")
	summary.SummaryStatus = summaryStatus

	updateStatus := statusCount.List("status.updated.status")
	summary.UpdateStatus = updateStatus

	return nil
}

type ListOption func(*listOptions)

type listOptions struct {
	withDeviceSummary bool
}

func ListWithDevicesSummary(val bool) ListOption {
	return func(o *listOptions) {
		o.withDeviceSummary = val
	}
}

type fleetWithCount struct {
	model.Fleet
	DeviceCount int64
}

func fleetSelectStr(withDeviceSummary bool) string {
	return lo.Ternary(withDeviceSummary,
		fmt.Sprintf("*, (select count(*) from devices where org_id = fleets.org_id and owner = CONCAT('%s/', fleets.name)) as device_count", api.FleetKind),
		"*")
}

// ListRolloutDeviceSelection attempts to get all relevant fleets for rollout device selection.
// A relevant fleet contains at least 1 device that at least one of the conditions below is true:
// - marked as selected for rollout
// - the template version of the fleet is not the same the template version in the annotation 'device-controller/renderedTemplateVersion'
// - the field 'status.config.renderedVersion' is not the same as the annotation 'device-controller/renderedVersion'
func (s *FleetStore) ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*api.FleetList, error) {
	var fleets []model.Fleet
	err := s.db.Raw(fmt.Sprintf(`select * from (select *, annotations ->> '%s' as tv from fleets) as main_query
         where
             org_id = ? and
             deleted_at is null and
             exists 
                 (select 1 from devices d where
                           deleted_at is null and
                           (annotations ? '%s' or
                                 main_query.tv <> COALESCE(annotations ->> '%s', '') or
                           		 status -> 'config' ->> 'renderedVersion' <> COALESCE(annotations ->> '%s', '')) and
                           		 org_id = ? and owner = CONCAT('%s/', main_query.name) limit 1)`,
		api.FleetAnnotationTemplateVersion, api.DeviceAnnotationSelectedForRollout, api.DeviceAnnotationRenderedTemplateVersion, api.DeviceAnnotationRenderedVersion,
		api.FleetKind), orgId, gorm.Expr("?"), orgId).Scan(&fleets).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	apiFleets, err := model.FleetsToApiResource(fleets, nil, nil)
	if err != nil {
		return nil, err
	}
	return &apiFleets, nil
}

// ListDisruptionBudgetFleets attempts to get fleets for disruption budget.  Since the disruption budget acts like
// a gate to device rendering, the query searches for fleets that each contains at least 1 device that has different value set
// between tha annotation 'device-controller/templateVersion' which is set before rollout and 'device-controller/renderedTemplateVersion'
// which is set after rollout.
func (s *FleetStore) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*api.FleetList, error) {
	var fleets []model.Fleet
	err := s.db.Raw(fmt.Sprintf(`select * from (select *, annotations ->> '%s' as tv from fleets) as main_query
         where
             org_id = ? and
             deleted_at is null and
             exists 
                 (select 1 from devices where deleted_at is null and org_id = ? and owner = CONCAT('%s/', main_query.name) and
					main_query.tv = annotations ->> '%s' and
                    main_query.tv <> COALESCE(annotations ->> '%s', '') limit 1)`,
		api.FleetAnnotationTemplateVersion,
		api.FleetKind, api.DeviceAnnotationTemplateVersion, api.DeviceAnnotationRenderedTemplateVersion), orgId, orgId).Scan(&fleets).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	apiFleets, err := model.FleetsToApiResource(fleets, nil, nil)
	if err != nil {
		return nil, err
	}
	return &apiFleets, nil
}

func (s *FleetStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams, opts ...ListOption) (*api.FleetList, error) {
	var fleetsWithCount []fleetWithCount
	var nextContinue *string
	var numRemaining *int64
	var options listOptions

	lo.ForEach(opts, func(opt ListOption, _ int) { opt(&options) })
	query, err := ListQuery(&model.Fleet{}).Build(ctx, s.db, orgId, listParams)
	if err != nil {
		return nil, err
	}
	query = query.Select(fleetSelectStr(options.withDeviceSummary))

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
			countQuery, err := ListQuery(&model.Fleet{}).Build(ctx, s.db, orgId, listParams)
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

	fleets := []model.Fleet{}
	for _, f := range fleetsWithCount {
		if options.withDeviceSummary {
			if f.Fleet.Status.Data.DevicesSummary == nil {
				f.Fleet.Status.Data.DevicesSummary = &api.DevicesSummary{}
			}
			f.Fleet.Status.Data.DevicesSummary.Total = f.DeviceCount
			err = s.addStatusSummary(ctx, orgId, f.Fleet.Name, f.Fleet.Status.Data.DevicesSummary)
			if err != nil {
				return nil, err
			}
		}
		fleets = append(fleets, f.Fleet)
	}

	apiFleetList, _ := model.FleetsToApiResource(fleets, nextContinue, numRemaining)
	return &apiFleetList, ErrorFromGormError(result.Error)
}

// A method to get all Fleets regardless of ownership. Used internally by the DeviceUpdater.
// TODO: Add pagination, perhaps via gorm scopes.
func (s *FleetStore) ListIgnoreOrg() ([]model.Fleet, error) {
	var fleets []model.Fleet

	result := s.db.Model(&fleets).Find(&fleets)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return fleets, nil
}

func (s *FleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string, callback FleetStoreCallback) error {
	return s.genericStore.Delete(
		ctx,
		model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}},
		callback)
}

func (s *FleetStore) DeleteAll(ctx context.Context, orgId uuid.UUID, callback FleetStoreAllDeletedCallback) error {
	return s.genericStore.DeleteAll(ctx, orgId, callback)
}

func (s *FleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *api.Fleet) (*api.Fleet, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
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
	return ErrorFromGormError(result.Error)
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
	return ErrorFromGormError(result.Error)
}

func (s *FleetStore) updateConditions(orgId uuid.UUID, name string, conditions []api.Condition) (bool, error) {
	existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.db.First(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
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
	err := ErrorFromGormError(result.Error)
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
		return false, ErrorFromGormError(result.Error)
	}
	existingAnnotations := util.EnsureMap(existingRecord.Annotations)
	existingAnnotations = util.MergeLabels(existingAnnotations, annotations)

	for _, deleteKey := range deleteKeys {
		delete(existingAnnotations, deleteKey)
	}

	result = s.db.Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":      model.MakeJSONMap(existingAnnotations),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	err := ErrorFromGormError(result.Error)
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
			return ErrorFromGormError(err)
		}
		return nil
	})
}

func (s *FleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*api.RepositoryList, error) {
	fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	var repos []model.Repository
	err := s.db.Model(&fleet).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	repositories, err := model.RepositoriesToApiResource(repos, nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}
