package fleet

import (
	"context"
	b64 "encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// FleetMutation is the unit apply mutates. Handlers decide all field changes.
// Fleet is nil on the create path until apply assigns it.
type FleetMutation struct {
	Fleet *domain.Fleet
}

func (m *FleetMutation) Resource() *domain.Fleet { return m.Fleet }

func (m *FleetMutation) SetResource(fleet *domain.Fleet) { m.Fleet = fleet }

func (m *FleetMutation) Clone() (store.ResourceMutation[domain.Fleet], error) {
	out := &FleetMutation{}
	if m.Fleet != nil {
		cloned, err := store.CloneJSON(m.Fleet)
		if err != nil {
			return nil, err
		}
		out.Fleet = cloned
	}
	return out, nil
}

// RequireExisting returns ErrResourceNotFound when Fleet is nil (create path).
// Update-only callers should invoke this at the start of apply.
func (m *FleetMutation) RequireExisting() error {
	if m.Fleet == nil {
		return flterrors.ErrResourceNotFound
	}
	return nil
}

// FleetApplyFunc mutates m in place on every Mutate attempt.
type FleetApplyFunc func(m *FleetMutation) error

var _ store.ResourceMutation[domain.Fleet] = (*FleetMutation)(nil)

type Store interface {
	InitialMigration(ctx context.Context) error

	// Create inserts a fleet. Duplicate names return ErrDuplicateName.
	// No events; the caller fires its own callback.
	Create(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, error)
	// Mutate loads (or uses previous once), runs apply, and persists via Create / Update.
	// If the fleet does not exist, m.Fleet is nil and apply must assign it (create);
	// otherwise apply mutates a clone. Returns created and the pre-mutation snapshot so
	// the caller can fire its own event callback; Mutate itself never calls one.
	// Create-only API callers should use Create instead. Update-only callers should call
	// m.RequireExisting() in apply.
	Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Fleet, apply FleetApplyFunc) (updated *domain.Fleet, before *domain.Fleet, created bool, err error)
	// UpdateStatus sets fleet.Status via Mutate. No events; the caller fires its own callback.
	UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (updated *domain.Fleet, before *domain.Fleet, err error)
	// UpdateAnnotations merges annotations (and applies deleteKeys) via Mutate.
	// No events; the caller fires its own callback using before/updated.
	UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) (updated *domain.Fleet, before *domain.Fleet, err error)
	Get(ctx context.Context, orgId uuid.UUID, name string, opts ...GetOption) (*domain.Fleet, error)
	List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, opts ...ListOption) (*domain.FleetList, error)
	Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error

	ListRolloutDeviceSelection(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error)
	ListDisruptionBudgetFleets(ctx context.Context, orgId uuid.UUID) (*domain.FleetList, error)
	UnsetOwner(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, owner string) error
	UnsetOwnerByKind(ctx context.Context, tx *gorm.DB, orgId uuid.UUID, resourceKind string) error
	OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error
	GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error)

	// Used by catalog
	ListFleetsByOsCatalogItemRef(ctx context.Context, orgId uuid.UUID, catalog string, item string, listParams store.ListParams) (*domain.FleetList, error)
	ListFleetsByAppCatalogItemRef(ctx context.Context, orgId uuid.UUID, catalog string, item string, listParams store.ListParams) (*domain.FleetList, error)
	ListFleetsByVolumeCatalogItemRef(ctx context.Context, orgId uuid.UUID, catalog string, item string, listParams store.ListParams) (*domain.FleetList, error)

	// Used by domain metrics
	CountByRolloutStatus(ctx context.Context, orgId *uuid.UUID, _ *string) ([]CountByRolloutStatusResult, error)
}

type FleetStore struct {
	dbHandler    *gorm.DB
	log          logrus.FieldLogger
	genericStore *store.GenericStore[*model.Fleet, model.Fleet, domain.Fleet, domain.FleetList]
}

// Make sure we conform to the Store interface
var _ Store = (*FleetStore)(nil)

func NewFleetStore(db *gorm.DB, log logrus.FieldLogger) Store {
	genericStore := store.NewGenericStore[*model.Fleet, model.Fleet, domain.Fleet, domain.FleetList](
		db,
		log,
		model.NewFleetFromApiResource,
		(*model.Fleet).ToApiResource,
		model.FleetsToApiResource,
	)
	return &FleetStore{dbHandler: db, log: log, genericStore: genericStore}
}

func (s *FleetStore) callEventCallback(ctx context.Context, eventCallback store.EventCallback, orgId uuid.UUID, name string, oldFleet, newFleet *domain.Fleet, created bool, err error) {
	if eventCallback == nil {
		return
	}

	store.SafeEventCallback(s.log, func() {
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

	if err := s.createFleetOsCatalogRefIndex(db); err != nil {
		return err
	}

	if err := s.createFleetAppCatalogRefIndex(db); err != nil {
		return err
	}

	if err := s.createFleetVolumeCatalogRefIndex(db); err != nil {
		return err
	}

	return nil
}

func (s *FleetStore) createFleetOsCatalogRefIndex(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_fleets_os_catalog_ref
		ON fleets ((spec->'template'->'spec'->'os'->'catalogItemRef'->>'catalog'), (spec->'template'->'spec'->'os'->'catalogItemRef'->>'item'))
		WHERE deleted_at IS NULL`).Error
}

func (s *FleetStore) createFleetAppCatalogRefIndex(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_fleets_app_catalog_refs
		ON fleets USING GIN ((jsonb_path_query_array(spec, '$.template.spec.applications[*].catalogItemRef')) jsonb_path_ops)
		WHERE deleted_at IS NULL`).Error
}

func (s *FleetStore) createFleetVolumeCatalogRefIndex(db *gorm.DB) error {
	if db.Dialector.Name() != "postgres" {
		return nil
	}
	return db.Exec(`CREATE INDEX IF NOT EXISTS idx_fleets_volume_catalog_refs
		ON fleets USING GIN ((jsonb_path_query_array(spec, '$.template.spec.applications[*].volumes[*].image.catalogItemRef')) jsonb_path_ops)
		WHERE deleted_at IS NULL`).Error
}

// Mutate loads the named fleet (or uses previous on the first attempt), runs apply,
// and persists via GenericStore.Mutate using Create / Update.
// Missing fleets are created (apply must set Fleet); existing ones are updated.
// The caller is responsible for firing any event callback using the returned
// before/updated/created values.
func (s *FleetStore) Mutate(ctx context.Context, orgId uuid.UUID, name string, previous *domain.Fleet, apply FleetApplyFunc) (*domain.Fleet, *domain.Fleet, bool, error) {
	if previous != nil && lo.FromPtr(previous.Metadata.Name) != name {
		previous = nil
	}
	return s.genericStore.Mutate(ctx, orgId, name, previous, store.MutateHooks[domain.Fleet]{
		Wrap: func(fleet *domain.Fleet) store.ResourceMutation[domain.Fleet] {
			return &FleetMutation{Fleet: fleet}
		},
		PersistCreate: func(ctx context.Context, orgId uuid.UUID, m store.ResourceMutation[domain.Fleet]) (*domain.Fleet, error) {
			return s.Create(ctx, orgId, m.Resource())
		},
		PersistUpdate: func(ctx context.Context, orgId uuid.UUID, _ string, before *domain.Fleet, m store.ResourceMutation[domain.Fleet]) (bool, error) {
			return s.Update(ctx, orgId, before, m.Resource())
		},
	}, func(m store.ResourceMutation[domain.Fleet]) error {
		return apply(m.(*FleetMutation))
	})
}

// UpdateStatus sets fleet.Status via Mutate.
func (s *FleetStore) UpdateStatus(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, *domain.Fleet, error) {
	if fleet == nil {
		return nil, nil, flterrors.ErrResourceIsNil
	}
	name := lo.FromPtr(fleet.Metadata.Name)
	updated, before, _, err := s.Mutate(ctx, orgId, name, nil, func(m *FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		m.Fleet.Status = fleet.Status
		return nil
	})
	return updated, before, err
}

// UpdateAnnotations merges annotations via Mutate.
func (s *FleetStore) UpdateAnnotations(ctx context.Context, orgId uuid.UUID, name string, annotations map[string]string, deleteKeys []string) (*domain.Fleet, *domain.Fleet, error) {
	updated, before, _, err := s.Mutate(ctx, orgId, name, nil, func(m *FleetMutation) error {
		if err := m.RequireExisting(); err != nil {
			return err
		}
		merged := store.MergeAnnotations(m.Fleet.Metadata.Annotations, annotations, deleteKeys)
		m.Fleet.Metadata.Annotations = &merged
		return nil
	})
	return updated, before, err
}

// Create inserts a fleet. No events; callers fire their own callbacks.
func (s *FleetStore) Create(ctx context.Context, orgId uuid.UUID, fleet *domain.Fleet) (*domain.Fleet, error) {
	if fleet == nil {
		return nil, flterrors.ErrResourceIsNil
	}
	fleetModel, err := model.NewFleetFromApiResource(fleet)
	if err != nil {
		return nil, err
	}
	fleetModel.OrgID = orgId
	fleetModel.Generation = lo.ToPtr(int64(1))
	fleetModel.ResourceVersion = lo.ToPtr(int64(1))

	result := s.getDB(ctx).Create(fleetModel)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return fleetModel.ToApiResource()
}

// Update writes a fleet update. Returns retry=true on lost optimistic lock / deadlock.
func (s *FleetStore) Update(ctx context.Context, orgId uuid.UUID, before, fleet *domain.Fleet) (bool, error) {
	existing, err := model.NewFleetFromApiResource(before)
	if err != nil {
		return false, err
	}
	existing.OrgID = orgId

	fromAPI, err := model.NewFleetFromApiResource(fleet)
	if err != nil {
		return false, err
	}
	fromAPI.OrgID = orgId

	// Prefer API-level Spec comparison so generation tracks the same Spec delta
	// that event emission uses (HasSameSpecAs alone can miss union/ref changes
	// if model conversion ever loses fields).
	generation := lo.FromPtr(existing.Generation)
	apiSpecChanged := before != nil && fleet != nil &&
		!domain.FleetSpecsAreEqual(before.Spec, fleet.Spec)
	if apiSpecChanged || !fromAPI.HasSameSpecAs(existing) {
		generation++
	}

	updates := map[string]interface{}{
		"spec":             fromAPI.Spec,
		"labels":           model.MakeJSONMap(fromAPI.Labels),
		"annotations":      model.MakeJSONMap(fromAPI.Annotations),
		"owner":            fromAPI.Owner,
		"generation":       generation,
		"status":           fromAPI.Status,
		"resource_version": gorm.Expr("resource_version + 1"),
	}

	result := s.getDB(ctx).Model(existing).Where("resource_version = ?", lo.FromPtr(existing.ResourceVersion)).Updates(updates)
	if result.Error != nil {
		err := store.ErrorFromGormError(result.Error)
		return strings.Contains(err.Error(), "deadlock"), err
	}
	if result.RowsAffected == 0 {
		return true, flterrors.ErrNoRowsUpdated
	}

	fleet.Metadata.Generation = lo.ToPtr(generation)
	fleet.Metadata.ResourceVersion = lo.ToPtr(strconv.FormatInt(lo.FromPtr(existing.ResourceVersion)+1, 10))
	return false, nil
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
		return nil, store.ErrorFromGormError(result.Error)
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
	deviceQuery, err := store.ListQuery(&model.Device{}).Build(ctx, s.getDB(ctx), orgId, store.ListParams{FieldSelector: fs})
	if err != nil {
		return err
	}

	statusCount, err := store.CountStatusList(ctx, deviceQuery,
		"status.applicationsSummary.status",
		"status.summary.status",
		"status.updated.status",
		"status.capabilities.osMode",
		"status.capabilities.deltaEligible")
	if err != nil {
		return store.ErrorFromGormError(err)
	}

	applicationStatus := statusCount.List("status.applicationsSummary.status")
	summary.ApplicationStatus = applicationStatus

	summaryStatus := statusCount.List("status.summary.status")
	summary.SummaryStatus = summaryStatus

	updateStatus := statusCount.List("status.updated.status")
	summary.UpdateStatus = updateStatus

	osModeStatus := statusCount.List("status.capabilities.osMode")
	deltaEligibleStatus := statusCount.List("status.capabilities.deltaEligible")
	summary.Capabilities = model.NewDevicesSummaryCapabilities(osModeStatus, deltaEligibleStatus)

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
		return nil, store.ErrorFromGormError(err)
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
		return nil, store.ErrorFromGormError(err)
	}
	apiFleets, err := model.FleetsToApiResource(fleets, nil, nil)
	if err != nil {
		return nil, err
	}
	return &apiFleets, nil
}

func (s *FleetStore) List(ctx context.Context, orgId uuid.UUID, listParams store.ListParams, opts ...ListOption) (*domain.FleetList, error) {
	var fleetsWithCount []fleetWithCount
	var nextContinue *string
	var numRemaining *int64
	var options listOptions

	lo.ForEach(opts, func(opt ListOption, _ int) { opt(&options) })
	query, err := store.ListQuery(&model.Fleet{}).Build(ctx, s.getDB(ctx), orgId, listParams)
	if err != nil {
		return nil, err
	}
	query = query.Select(fleetSelectStr(options.withDeviceSummary))

	if listParams.Limit > 0 {
		// Request 1 more than the user asked for to see if we need to return "continue"
		query = store.AddPaginationToQuery(query, listParams.Limit+1, listParams.Continue, listParams)
	}
	result := query.Scan(&fleetsWithCount)

	// If we got more than the user requested, remove one record and calculate "continue"
	if listParams.Limit > 0 && len(fleetsWithCount) > listParams.Limit {
		nextContinueStruct := store.Continue{
			Names:   []string{fleetsWithCount[len(fleetsWithCount)-1].Name},
			Version: store.CurrentContinueVersion,
		}
		fleetsWithCount = fleetsWithCount[:len(fleetsWithCount)-1]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			countQuery, err := store.ListQuery(&model.Fleet{}).Build(ctx, s.getDB(ctx), orgId, listParams)
			if err != nil {
				return nil, err
			}
			numRemainingVal = store.CountRemainingItems(countQuery, nextContinueStruct.Names, listParams)
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
	return &apiFleetList, store.ErrorFromGormError(result.Error)
}

func (s *FleetStore) ListIgnoreOrg(ctx context.Context) ([]model.Fleet, error) {
	var fleets []model.Fleet

	result := s.getDB(ctx).Model(&fleets).Find(&fleets)
	if result.Error != nil {
		return nil, store.ErrorFromGormError(result.Error)
	}
	return fleets, nil
}

func (s *FleetStore) Delete(ctx context.Context, orgId uuid.UUID, name string, eventCallback store.EventCallback) error {
	deleted, err := s.genericStore.Delete(
		ctx,
		model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}},
	)
	if deleted && eventCallback != nil {
		s.callEventCallback(ctx, eventCallback, orgId, name, nil, nil, false, err)
	}
	return err
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
	return store.ErrorFromGormError(result.Error)
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
	return store.ErrorFromGormError(result.Error)
}

func (s *FleetStore) OverwriteRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string, repositoryNames ...string) error {
	repos := []model.Repository{}
	for _, repoName := range repositoryNames {
		repos = append(repos, model.Repository{Resource: model.Resource{OrgID: orgId, Name: repoName}})
	}
	return s.getDB(ctx).Transaction(func(innerTx *gorm.DB) error {
		fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
		if err := innerTx.Model(&fleet).Association("Repositories").Replace(repos); err != nil {
			return store.ErrorFromGormError(err)
		}
		return nil
	})
}

func (s *FleetStore) GetRepositoryRefs(ctx context.Context, orgId uuid.UUID, name string) (*domain.RepositoryList, error) {
	fleet := model.Fleet{Resource: model.Resource{OrgID: orgId, Name: name}}
	var repos []model.Repository
	err := s.getDB(ctx).Model(&fleet).Association("Repositories").Find(&repos)
	if err != nil {
		return nil, store.ErrorFromGormError(err)
	}
	repositories, err := model.RepositoriesToApiResource(repos, nil, nil)
	if err != nil {
		return nil, err
	}
	return &repositories, nil
}

func (s *FleetStore) ListFleetsByOsCatalogItemRef(ctx context.Context, orgId uuid.UUID, catalog string, item string, listParams store.ListParams) (*domain.FleetList, error) {
	var fleets []model.Fleet
	var nextContinue *string
	var numRemaining *int64

	querySQL := `
		SELECT * FROM fleets
		WHERE org_id = ?
			AND deleted_at IS NULL
			AND spec->'template'->'spec'->'os'->'catalogItemRef'->>'catalog' = ?
			AND spec->'template'->'spec'->'os'->'catalogItemRef'->>'item' = ?`

	args := []interface{}{orgId, catalog, item}

	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		querySQL += " AND name > ?"
		args = append(args, listParams.Continue.Names[0])
	}

	querySQL += " ORDER BY name ASC"

	if listParams.Limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, listParams.Limit+1)
	}

	if err := s.getDB(ctx).Raw(querySQL, args...).Scan(&fleets).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}

	if listParams.Limit > 0 && len(fleets) > listParams.Limit {
		fleets = fleets[:listParams.Limit]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			numRemainingVal = 1
		}

		nextContinue = store.BuildContinueString([]string{fleets[len(fleets)-1].Name}, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	result, err := model.FleetsToApiResource(fleets, nextContinue, numRemaining)
	return &result, err
}

func (s *FleetStore) ListFleetsByAppCatalogItemRef(ctx context.Context, orgId uuid.UUID, catalog string, item string, listParams store.ListParams) (*domain.FleetList, error) {
	var fleets []model.Fleet
	var nextContinue *string
	var numRemaining *int64

	querySQL := `
		SELECT * FROM fleets
		WHERE org_id = ?
			AND deleted_at IS NULL
			AND jsonb_path_query_array(spec, '$.template.spec.applications[*].catalogItemRef') @> ?::jsonb`

	catalogRef, err := json.Marshal([]map[string]string{{"catalog": catalog, "item": item}})
	if err != nil {
		return nil, err
	}
	args := []interface{}{orgId, string(catalogRef)}

	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		querySQL += " AND name > ?"
		args = append(args, listParams.Continue.Names[0])
	}

	querySQL += " ORDER BY name ASC"

	if listParams.Limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, listParams.Limit+1)
	}

	if err := s.getDB(ctx).Raw(querySQL, args...).Scan(&fleets).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}

	if listParams.Limit > 0 && len(fleets) > listParams.Limit {
		fleets = fleets[:listParams.Limit]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			numRemainingVal = 1
		}

		nextContinue = store.BuildContinueString([]string{fleets[len(fleets)-1].Name}, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	result, err := model.FleetsToApiResource(fleets, nextContinue, numRemaining)
	return &result, err
}

func (s *FleetStore) ListFleetsByVolumeCatalogItemRef(ctx context.Context, orgId uuid.UUID, catalog string, item string, listParams store.ListParams) (*domain.FleetList, error) {
	var fleets []model.Fleet
	var nextContinue *string
	var numRemaining *int64

	querySQL := `
		SELECT * FROM fleets
		WHERE org_id = ?
			AND deleted_at IS NULL
			AND jsonb_path_query_array(spec, '$.template.spec.applications[*].volumes[*].image.catalogItemRef') @> ?::jsonb`

	catalogRef, err := json.Marshal([]map[string]string{{"catalog": catalog, "item": item}})
	if err != nil {
		return nil, err
	}
	args := []interface{}{orgId, string(catalogRef)}

	if listParams.Continue != nil && len(listParams.Continue.Names) > 0 {
		querySQL += " AND name > ?"
		args = append(args, listParams.Continue.Names[0])
	}

	querySQL += " ORDER BY name ASC"

	if listParams.Limit > 0 {
		querySQL += " LIMIT ?"
		args = append(args, listParams.Limit+1)
	}

	if err := s.getDB(ctx).Raw(querySQL, args...).Scan(&fleets).Error; err != nil {
		return nil, store.ErrorFromGormError(err)
	}

	if listParams.Limit > 0 && len(fleets) > listParams.Limit {
		fleets = fleets[:listParams.Limit]

		var numRemainingVal int64
		if listParams.Continue != nil {
			numRemainingVal = listParams.Continue.Count - int64(listParams.Limit)
			if numRemainingVal < 1 {
				numRemainingVal = 1
			}
		} else {
			numRemainingVal = 1
		}

		nextContinue = store.BuildContinueString([]string{fleets[len(fleets)-1].Name}, numRemainingVal)
		numRemaining = &numRemainingVal
	}

	result, err := model.FleetsToApiResource(fleets, nextContinue, numRemaining)
	return &result, err
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
		query, err = store.ListQuery(&model.Fleet{}).BuildNoOrder(ctx, s.getDB(ctx), *orgId, store.ListParams{})
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
		return nil, store.ErrorFromGormError(err)
	}
	return results, nil
}
