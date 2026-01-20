package store

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
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
	InitialMigration(ctx context.Context) error

	Create(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, eventCallback EventCallback) (*domain.Fleet, error)
	Update(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*domain.Fleet, error)
	CreateOrUpdate(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*domain.Fleet, bool, error)
	Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*domain.Fleet, error)
	List(ctx context.Context, orgId uuid.UUID, listParams ListParams, opts ...ListOption) (*domain.FleetList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, error)

	ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error)
	ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error)
	UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
	UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error
	UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, eventCallback EventCallback) error
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string, eventCallback EventCallback) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error)

	// Used by domain metrics
	CountByRolloutStatus(ctx context.Context, orgId *uuid.UUID, _ *string) ([]CountByRolloutStatusResult, error)
}

type FleetStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *GenericStore[*model.Fleet, model.Fleet, domain.Fleet, domain.FleetList]
}

// Make sure we conform to Fleet interface
var _ Fleet = (*FleetStore)(nil)

func NewFleet(db *gorm.DB, log logrus.FieldLogger) Fleet {
	genericStore := NewGenericStore[*model.Fleet, model.Fleet, domain.Fleet, domain.FleetList](
		db,
		log,
		model.NewFleetFromApiResource,
		(*model.Fleet).ToApiResource,
		model.FleetsToApiResource,
	)
	return &FleetStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *FleetStore) callEventCallback(ctx context.Context, eventCallback EventCallback, orgId uuid.UUID, name string, oldFleet, newFleet *domain.Fleet, created bool, err error) {
	if eventCallback == nil {
		return
	}

	SafeEventCallback(s.log, func() {
		eventCallback(ctx, domain.FleetKind, orgId, name, oldFleet, newFleet, created, err)
	})
}
func (s *FleetStore) getDB(ctx context.Context) *gorm.DB {
	return s.dbHandler.WithContext(ctx)
}

func (s *FleetStore) InitialMigration(ctx context.Context) error {
	db := s.getDB(ctx)

	if err := db.AutoMigrate(&model.Fleet{}); err != nil {
		return err
	}

	// Create GIN index for Fleet labels
	if !db.Migrator().HasIndex(&model.Fleet{}, "idx_fleet_labels") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_fleet_labels ON fleets USING GIN (labels)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.Fleet{}, "Labels"); err != nil {
				return err
			}
		}
	}

	// Create GIN index for Fleet annotations
	if !db.Migrator().HasIndex(&model.Fleet{}, "idx_fleet_annotations") {
		if db.Dialector.Name() == "postgres" {
			if err := db.Exec("CREATE INDEX idx_fleet_annotations ON fleets USING GIN (annotations)").Error; err != nil {
				return err
			}
		} else {
			if err := db.Migrator().CreateIndex(&model.Fleet{}, "Annotations"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *FleetStore) Create(ctx context.Context, orgId uuid.UUID, resource *domain.Fleet, eventCallback EventCallback) (*domain.Fleet, error) {
	fleet, err := s.genericStore.Create(ctx, orgId, resource)
	name := lo.FromPtr(resource.Metadata.Name)
	s.callEventCallback(ctx, eventCallback, orgId, name, nil, fleet, true, err)
	return fleet, err
}

func (s *FleetStore) Update(ctx context.Context, orgId uuid.UUID, resource *domain.Fleet, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*domain.Fleet, error) {
	newFleet, oldFleet, err := s.genericStore.Update(ctx, orgId, resource, fieldsToUnset, fromAPI, nil)
	s.callEventCallback(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldFleet, newFleet, false, err)
	return newFleet, err
}

func (s *FleetStore) CreateOrUpdate(ctx context.Context, orgId uuid.UUID, resource *domain.Fleet, fieldsToUnset []string, fromAPI bool, eventCallback EventCallback) (*domain.Fleet, bool, error) {
	newFleet, oldFleet, created, err := s.genericStore.CreateOrUpdate(ctx, orgId, resource, fieldsToUnset, fromAPI, nil)
	s.callEventCallback(ctx, eventCallback, orgId, lo.FromPtr(resource.Metadata.Name), oldFleet, newFleet, created, err)
	return newFleet, created, err
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

func (s *FleetStore) Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*domain.Fleet, error) {
	options := getOptions{}
	for _, opt := range opts {
		opt(&options)
	}

	var fleet fleetWithCount

	result := s.getDB(ctx).Table("fleets").Where("org_id = ? and name = ?", orgId, name).
		Select(fleetSelectStr(options.withDeviceSummary)).
		Scan(&fleet)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	} else if result.RowsAffected == 0 {
		return nil, flterrors.ErrResourceNotFound
	}

	var summary *domain.DevicesSummary // Remains nil unless withDeviceSummary is true; will be omitted in JSON if not set

	if options.withDeviceSummary {
		summary = &domain.DevicesSummary{Total: fleet.DeviceCount}
		err := s.addStatusSummary(ctx, orgId, name, summary)
		if err != nil {
			return nil, err
		}
	}

	// Passing summary (nil if not set), handled downstream
	apiFleet, _ := fleet.ToApiResource(model.WithDevicesSummary(summary))
	return apiFleet, nil
}

func (s *FleetStore) addStatusSummary(ctx context.Context, orgId uuid.UUID, fleetName string, summary *domain.DevicesSummary) error {
	fs, err := selector.NewFieldSelectorFromMap(
		map[string]string{"metadata.owner": util.ResourceOwner(domain.FleetKind, fleetName)})
	if err != nil {
		return err
	}
	deviceQuery, err := ListQuery(&model.Device{}).Build(ctx, s.getDB(ctx), orgId, ListParams{FieldSelector: fs})
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
		fmt.Sprintf("*, (select count(*) from devices where org_id = fleets.org_id and owner = CONCAT('%s/', fleets.name)) as device_count", domain.FleetKind),
		"*")
}

// ListRolloutDeviceSelection attempts to get all relevant fleets for rollout device selection.
// A relevant fleet contains at least 1 device that at least one of the conditions below is true:
// - marked as selected for rollout
// - the template version of the fleet is not the same the template version in the annotation 'device-controller/renderedTemplateVersion'
// - the field 'status.config.renderedVersion' is not the same as the annotation 'device-controller/renderedVersion'
func (s *FleetStore) ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error) {
	var fleets []model.Fleet
	err := s.getDB(ctx).Raw(fmt.Sprintf(`select * from (select *, annotations ->> '%s' as tv from fleets) as main_query
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
		domain.FleetAnnotationTemplateVersion, domain.DeviceAnnotationSelectedForRollout, domain.DeviceAnnotationRenderedTemplateVersion, domain.DeviceAnnotationRenderedVersion,
		domain.FleetKind), orgId, gorm.Expr("?"), orgId).Scan(&fleets).Error
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
func (s *FleetStore) ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error) {
	var fleets []model.Fleet
	err := s.getDB(ctx).Raw(fmt.Sprintf(`select * from (select *, annotations ->> '%s' as tv from fleets) as main_query
         where
             org_id = ? and
             deleted_at is null and
             exists 
                 (select 1 from devices where deleted_at is null and org_id = ? and owner = CONCAT('%s/', main_query.name) and
					main_query.tv = annotations ->> '%s' and
                    main_query.tv <> COALESCE(annotations ->> '%s', '') limit 1)`,
		domain.FleetAnnotationTemplateVersion,
		domain.FleetKind, domain.DeviceAnnotationTemplateVersion, domain.DeviceAnnotationRenderedTemplateVersion), orgId, orgId).Scan(&fleets).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	apiFleets, err := model.FleetsToApiResource(fleets, nil, nil)
	if err != nil {
		return nil, err
	}
	return &apiFleets, nil
}

func (s *FleetStore) List(ctx context.Context, orgId uuid.UUID, listParams ListParams, opts ...ListOption) (*domain.FleetList, error) {
	var fleetsWithCount []fleetWithCount
	var nextContinue *string
	var numRemaining *int64
	var options listOptions

	lo.ForEach(opts, func(opt ListOption, _ int) { opt(&options) })
	query, err := ListQuery(&model.Fleet{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}
	query = query.Select(fleetSelectStr(options.withDeviceSummary))

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}
	result := query.Scan(&fleetsWithCount)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(fleetsWithCount) > listParams.Limit {
		nextContinueStruct := Continue{
			Names:   []string{fleetsWithCount[len(fleetsWithCount)-1].Name},
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
			countQuery, err := ListQuery(&model.Fleet{}).Build(ctx, s.getDB(ctx), orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = CountRemainingItems(countQuery, nextContinueStruct.Names, listParams)
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
				f.Fleet.Status.Data.DevicesSummary = &domain.DevicesSummary{}
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
func (s *FleetStore) ListIgnoreOrg(ctx context.Context) ([]model.Fleet, error) {
	var fleets []model.Fleet

	result := s.getDB(ctx).Model(&fleets).Find(&fleets)
	if result.Error != nil {
		return nil, ErrorFromGormError(result.Error)
	}
	return fleets, nil
}

func (s *FleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback EventCallback) error {
	deleted, err := s.genericStore.Delete(
		ctx,
		model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}},
	)
	if deleted && eventCallback != nil {
		s.callEventCallback(ctx, eventCallback, orgId, name, nil, nil, false, err)
	}
	return err
}
func (s *FleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *domain.Fleet) (*domain.Fleet, error) {
	return s.genericStore.UpdateStatus(ctx, orgId, resource)
}

func (s *FleetStore) UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error {
	db := s.getDB(ctx)
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
	db := s.getDB(ctx)
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

func (s *FleetStore) updateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, eventCallback EventCallback) (bool, error) {
	existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return false, ErrorFromGormError(result.Error)
	}

	if existingRecord.Status == nil {
		existingRecord.Status = model.MakeJSONField(domain.FleetStatus{})
	}
	if existingRecord.Status.Data.Conditions == nil {
		existingRecord.Status.Data.Conditions = []domain.Condition{}
	}

	// Make a full copy of the existing conditions
	existingConditions := make([]domain.Condition, len(existingRecord.Status.Data.Conditions))
	copy(existingConditions, existingRecord.Status.Data.Conditions)

	changed := false
	for _, condition := range conditions {
		if domain.SetStatusCondition(&existingRecord.Status.Data.Conditions, condition) {
			changed = true
		}
	}
	if !changed {
		return false, nil
	}

	result = s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
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

	oldFleet, _ := existingRecord.ToApiResource()
	oldFleet.Status.Conditions = existingConditions
	newFleet, _ := existingRecord.ToApiResource()
	s.callEventCallback(ctx, eventCallback, orgId, name, oldFleet, newFleet, false, err)
	return false, nil
}

func (s *FleetStore) UpdateConditions(ctx context.Context, orgId uuid.UUID, name string, conditions []domain.Condition, eventCallback EventCallback) error {
	return retryUpdate(func() (bool, error) {
		return s.updateConditions(ctx, orgId, name, conditions, eventCallback)
	})
}

func (s *FleetStore) updateAnnotations(ctx context.Context, existingRecord model.Fleet, existingAnnotations map[string]string) (bool, error) {
	result := s.getDB(ctx).Model(existingRecord).Where("resource_version = ?", lo.FromPtr(existingRecord.ResourceVersion)).Updates(map[string]interface{}{
		"annotations":      model.MakeJSONMap(existingAnnotations),
		"resource_version": gorm.Expr("resource_version + 1"),
	})
	if err := ErrorFromGormError(result.Error); err != nil {
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}
	return false, nil
}

func (s *FleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string, eventCallback EventCallback) error {
	existingRecord := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	result := s.getDB(ctx).Take(&existingRecord)
	if result.Error != nil {
		return ErrorFromGormError(result.Error)
	}

	existingAnnotations := util.EnsureMap(existingRecord.Annotations)
	newAnnotations := util.MergeLabels(existingAnnotations, annotations)
	for _, deleteKey := range deleteKeys {
		delete(newAnnotations, deleteKey)
	}
	err := retryUpdate(func() (bool, error) {
		return s.updateAnnotations(ctx, existingRecord, newAnnotations)
	})

	oldFleet := &domain.Fleet{Metadata: domain.ObjectMeta{
		Annotations: &existingAnnotations,
	}}
	newFleet := &domain.Fleet{Metadata: domain.ObjectMeta{
		Annotations: &newAnnotations,
	}}
	s.callEventCallback(ctx, eventCallback, orgId, name, oldFleet, newFleet, false, err)
	return err
}

func (s *FleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	repos := []model.Repository{}
	for _, repoName := range repositoryNames {
		repos = append(repos, model.Repository{Resource: model.Resource{OrgID: orgId, Name: repoName}})
	}
	return s.getDB(ctx).Transaction(func(innerTx *gorm.DB) error {
		fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
		if err := innerTx.Model(&fleet).Association("Repositories").Replace(repos); err != nil {
			return ErrorFromGormError(err)
		}
		return nil
	})
}

func (s *FleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error) {
	fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	var repos []model.Repository
	err := s.getDB(ctx).Model(&fleet).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	repositories, err := model.RepositoriesToApiResource(repos, nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}

// CountByRolloutStatusResult holds the result of the group by query
// for fleet rollout status.
type CountByRolloutStatusResult struct {
	OrgID  string
	Status string
	Count  int64
}

// CountByRolloutStatus returns the count of fleets grouped by org_id and rollout status.
func (s *FleetStore) CountByRolloutStatus(ctx context.Context, orgId *uuid.UUID, _ *string) ([]CountByRolloutStatusResult, error) {
	var query *gorm.DB
	var err error

	if orgId != nil {
		query, err = ListQuery(&model.Fleet{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, ListParams{})
	} else {
		// When orgId is nil, we don't filter by org_id
		query = s.getDB(ctx).Model(&model.Fleet{})
	}

	if err != nil {
		return nil, err
	}

	// Extract the reason from RolloutInProgress condition
	// The status JSON structure: {"conditions": [{"type": "RolloutInProgress", "reason": "Active|Inactive|Suspended|Waiting", ...}]}
	statusField := `COALESCE(
		(SELECT condition->>'reason' 
		 FROM jsonb_array_elements(status->'conditions') AS condition 
		 WHERE condition->>'type' = 'RolloutInProgress'
		 LIMIT 1), 
		'Inactive'
	)`
	query = query.Select(
		"org_id as org_id",
		statusField+" as status",
		"COUNT(*) as count",
	).Group("org_id, " + statusField)

	var results []CountByRolloutStatusResult
	err = query.Scan(&results).Error
	if err != nil {
		return nil, ErrorFromGormError(err)
	}
	return results, nil
}
