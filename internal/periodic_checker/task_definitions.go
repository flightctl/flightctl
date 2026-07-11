package periodic

import (
	"context"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	periodicmetrics "github.com/flightctl/flightctl/internal/instrumentation/metrics/periodic"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/rollout/device_selection"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	organizationservice "github.com/flightctl/flightctl/internal/service/organization"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	resourcesyncservice "github.com/flightctl/flightctl/internal/service/resourcesync"
	syncstateservice "github.com/flightctl/flightctl/internal/service/syncstate"
	vulnerabilityfindingstore "github.com/flightctl/flightctl/internal/store/vulnerabilityfinding"
	"github.com/flightctl/flightctl/internal/tasks"
	trustifyv2 "github.com/flightctl/flightctl/internal/trustify/v2"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

// QueueMaintenanceInterval is the interval for queue maintenance tasks
// It's set to 2x EventProcessingTimeout to ensure timely processing of timed out messages
const QueueMaintenanceInterval = 2 * tasks.EventProcessingTimeout

type PeriodicTaskType string

const (
	PeriodicTaskTypeRepositoryTester       PeriodicTaskType = "repository-tester"
	PeriodicTaskTypeResourceSync           PeriodicTaskType = "resource-sync"
	PeriodicTaskTypeDeviceConnection       PeriodicTaskType = "device-connection"
	PeriodicTaskTypeRolloutDeviceSelection PeriodicTaskType = "rollout-device-selection"
	PeriodicTaskTypeDisruptionBudget       PeriodicTaskType = "disruption-budget"
	PeriodicTaskTypeEventCleanup           PeriodicTaskType = "event-cleanup"
	PeriodicTaskTypeQueueMaintenance       PeriodicTaskType = "queue-maintenance"
	PeriodicTaskTypeVulnerabilitySync      PeriodicTaskType = "vulnerability-sync"
	PeriodicTaskTypeDependencySyncGit      PeriodicTaskType = "dependency-sync-git"
	PeriodicTaskTypeDependencySyncHttp     PeriodicTaskType = "dependency-sync-http"
)

type PeriodicTaskMetadata struct {
	Interval   time.Duration
	SystemWide bool // If true, task runs once for the entire system, not per organization
}

var periodicTasks = map[PeriodicTaskType]PeriodicTaskMetadata{
	PeriodicTaskTypeRepositoryTester:       {Interval: 2 * time.Minute, SystemWide: false},
	PeriodicTaskTypeResourceSync:           {Interval: 2 * time.Minute, SystemWide: false},
	PeriodicTaskTypeDeviceConnection:       {Interval: tasks.DeviceConnectionPollingInterval, SystemWide: false},
	PeriodicTaskTypeRolloutDeviceSelection: {Interval: device_selection.RolloutDeviceSelectionInterval, SystemWide: false},
	PeriodicTaskTypeDisruptionBudget:       {Interval: disruption_budget.DisruptionBudgetReconcilationInterval, SystemWide: false},
	PeriodicTaskTypeEventCleanup:           {Interval: tasks.EventCleanupPollingInterval, SystemWide: true},
	PeriodicTaskTypeQueueMaintenance:       {Interval: QueueMaintenanceInterval, SystemWide: true},
	PeriodicTaskTypeVulnerabilitySync:      {Interval: tasks.VulnerabilitySyncInterval, SystemWide: true},
	PeriodicTaskTypeDependencySyncGit:      {Interval: config.DefaultDependencySyncTaskInterval, SystemWide: false},
	PeriodicTaskTypeDependencySyncHttp:     {Interval: config.DefaultDependencySyncTaskInterval, SystemWide: false},
}

// MergeTasksWithConfig merges configured task intervals with defaults.
// Configured intervals override defaults; unconfigured tasks use defaults.
func MergeTasksWithConfig(cfg *config.Config) map[PeriodicTaskType]PeriodicTaskMetadata {
	merged := make(map[PeriodicTaskType]PeriodicTaskMetadata, len(periodicTasks))
	for k, v := range periodicTasks {
		merged[k] = v
	}

	vulnEnabled := cfg != nil && cfg.VulnerabilityReporting != nil && cfg.VulnerabilityReporting.Enabled
	if !vulnEnabled {
		delete(merged, PeriodicTaskTypeVulnerabilitySync)
	}

	if cfg == nil {
		return merged
	}

	if cfg.Periodic != nil {
		periodicTasks := cfg.Periodic.Tasks
		if periodicTasks.ResourceSync.Schedule.Interval > 0 {
			meta := merged[PeriodicTaskTypeResourceSync]
			meta.Interval = time.Duration(periodicTasks.ResourceSync.Schedule.Interval)
			merged[PeriodicTaskTypeResourceSync] = meta
		}
		if periodicTasks.DependencySync.Schedule.Interval > 0 {
			interval := time.Duration(periodicTasks.DependencySync.Schedule.Interval)
			for _, taskType := range []PeriodicTaskType{PeriodicTaskTypeDependencySyncGit, PeriodicTaskTypeDependencySyncHttp} {
				meta := merged[taskType]
				meta.Interval = interval
				merged[taskType] = meta
			}
		}
	}

	if vulnEnabled && cfg.VulnerabilityReporting.SyncInterval > 0 {
		meta := merged[PeriodicTaskTypeVulnerabilitySync]
		meta.Interval = time.Duration(cfg.VulnerabilityReporting.SyncInterval)
		merged[PeriodicTaskTypeVulnerabilitySync] = meta
	}

	return merged
}

type PeriodicTaskReference struct {
	Type  PeriodicTaskType
	OrgID uuid.UUID
}

type PeriodicTaskExecutor interface {
	Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID)
}

type RepositoryTesterExecutor struct {
	log           logrus.FieldLogger
	repositorySvc repositoryservice.Service
}

// createTaskContext creates a task context with request ID and event actor
func createTaskContext(ctx context.Context, taskType PeriodicTaskType) context.Context {
	taskName := string(taskType)
	reqid.OverridePrefix(taskName)
	requestID := reqid.NextRequestID()
	ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)

	return context.WithValue(ctx, consts.EventActorCtxKey, taskName)
}

func (e *RepositoryTesterExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeRepositoryTester)
	repoTester := tasks.NewRepoTester(e.log, e.repositorySvc, nil)
	repoTester.TestRepositories(taskCtx, orgId)
}

type ResourceSyncExecutor struct {
	repositorySvc   repositoryservice.Service
	fleetSvc        fleetservice.Service
	resourcesyncSvc resourcesyncservice.Service
	catalogSvc      catalogservice.Service
	log             logrus.FieldLogger
	cfg             *config.Config
}

func (e *ResourceSyncExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeResourceSync)
	var ignoreResourceUpdates []string
	if e.cfg != nil && e.cfg.GitOps != nil {
		ignoreResourceUpdates = e.cfg.GitOps.IgnoreResourceUpdates
	}
	resourceSync := tasks.NewResourceSync(e.repositorySvc, e.fleetSvc, e.resourcesyncSvc, e.catalogSvc, e.log, e.cfg, ignoreResourceUpdates)
	resourceSync.Poll(taskCtx, orgId)
}

type DeviceConnectionExecutor struct {
	log       logrus.FieldLogger
	deviceSvc deviceservice.Service
}

func (e *DeviceConnectionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDeviceConnection)
	deviceConnection := tasks.NewDeviceConnection(e.log, e.deviceSvc)
	deviceConnection.Poll(taskCtx, orgId)
}

type RolloutDeviceSelectionExecutor struct {
	deviceSvc deviceservice.Service
	fleetSvc  fleetservice.Service
	eventSvc  eventservice.Service
	log       logrus.FieldLogger
}

func (e *RolloutDeviceSelectionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeRolloutDeviceSelection)
	rolloutDeviceSelection := device_selection.NewReconciler(e.deviceSvc, e.fleetSvc, e.eventSvc, e.log)
	rolloutDeviceSelection.Reconcile(taskCtx, orgId)
}

type DisruptionBudgetExecutor struct {
	deviceSvc deviceservice.Service
	fleetSvc  fleetservice.Service
	eventSvc  eventservice.Service
	log       logrus.FieldLogger
}

func (e *DisruptionBudgetExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDisruptionBudget)
	disruptionBudget := disruption_budget.NewReconciler(e.deviceSvc, e.fleetSvc, e.eventSvc, e.log)
	disruptionBudget.Reconcile(taskCtx, orgId)
}

type EventCleanupExecutor struct {
	log                  logrus.FieldLogger
	eventSvc             eventservice.Service
	eventRetentionPeriod util.Duration
}

func (e *EventCleanupExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeEventCleanup)
	// Note: Event cleanup is system-wide, orgId is not used
	eventCleanup := tasks.NewEventCleanup(e.log, e.eventSvc, e.eventRetentionPeriod)
	eventCleanup.Poll(taskCtx)
}

type QueueMaintenanceExecutor struct {
	log             logrus.FieldLogger
	checkpointSvc   checkpointservice.Service
	eventSvc        eventservice.Service
	organizationSvc organizationservice.Service
	queuesProvider  queues.Provider
	workerClient    worker_client.WorkerClient
	workerMetrics   *worker.WorkerCollector
}

func (e *QueueMaintenanceExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeQueueMaintenance)

	// Create and execute the queue maintenance task
	// Note: Queue maintenance is system-wide, orgId is not used
	task := tasks.NewQueueMaintenanceTask(e.log, e.checkpointSvc, e.eventSvc, e.organizationSvc, e.queuesProvider, e.workerClient, e.workerMetrics)

	if err := task.Execute(taskCtx); err != nil {
		e.log.WithError(err).Error("Queue maintenance task failed")
	}
}

type VulnerabilitySyncExecutor struct {
	log           logrus.FieldLogger
	vulnClient    trustifyv2.VulnerabilityClient
	findingStore  vulnerabilityfindingstore.Store
	checkpointSvc checkpointservice.Service
	eventSvc      eventservice.Service
}

func (e *VulnerabilitySyncExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeVulnerabilitySync)
	checkpoint := &serviceCheckpointAdapter{svc: e.checkpointSvc}
	vulnSync := tasks.NewVulnerabilitySync(e.log, e.vulnClient, e.findingStore, checkpoint, e.eventSvc)
	vulnSync.Poll(taskCtx)
}

// serviceCheckpointAdapter adapts checkpoint.Service to tasks.CheckpointStore interface.
type serviceCheckpointAdapter struct {
	svc checkpointservice.Service
}

func (a *serviceCheckpointAdapter) Set(ctx context.Context, consumer, key string, value []byte) error {
	st := a.svc.SetCheckpoint(ctx, consumer, key, value)
	return statusToError(st)
}

func (a *serviceCheckpointAdapter) Get(ctx context.Context, consumer, key string) ([]byte, error) {
	data, st := a.svc.GetCheckpoint(ctx, consumer, key)
	return data, statusToError(st)
}

func (a *serviceCheckpointAdapter) GetDatabaseTime(ctx context.Context) (time.Time, error) {
	t, st := a.svc.GetDatabaseTime(ctx)
	return t, statusToError(st)
}

// statusToError converts a domain.Status to an error if it indicates a failure.
func statusToError(st domain.Status) error {
	if st.Code >= 200 && st.Code < 300 {
		return nil
	}
	if st.Code == 404 {
		return flterrors.ErrResourceNotFound
	}
	return fmt.Errorf("%s: %s", st.Reason, st.Message)
}

type DependencySyncGitExecutor struct {
	log              logrus.FieldLogger
	dependencyrefSvc dependencyrefservice.Service
	eventSvc         eventservice.Service
	syncstateSvc     syncstateservice.Service
	cfg              *config.Config
	metrics          *periodicmetrics.DependencySyncCollector
}

func (e *DependencySyncGitExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDependencySyncGit)
	depSync := tasks.NewDependencySyncGit(e.log, e.dependencyrefSvc, e.eventSvc, e.syncstateSvc, e.cfg, e.metrics)
	depSync.Poll(taskCtx, orgId)
}

type DependencySyncHttpExecutor struct {
	log              logrus.FieldLogger
	dependencyrefSvc dependencyrefservice.Service
	eventSvc         eventservice.Service
	syncstateSvc     syncstateservice.Service
	cfg              *config.Config
	metrics          *periodicmetrics.DependencySyncCollector
}

func (e *DependencySyncHttpExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDependencySyncHttp)
	depSync := tasks.NewDependencySyncHttp(e.log, e.dependencyrefSvc, e.eventSvc, e.syncstateSvc, e.cfg, e.metrics)
	depSync.Poll(taskCtx, orgId)
}

func InitializeTaskExecutors(
	log logrus.FieldLogger,
	repositorySvc repositoryservice.Service,
	fleetSvc fleetservice.Service,
	resourcesyncSvc resourcesyncservice.Service,
	catalogSvc catalogservice.Service,
	deviceSvc deviceservice.Service,
	eventSvc eventservice.Service,
	checkpointSvc checkpointservice.Service,
	organizationSvc organizationservice.Service,
	dependencyrefSvc dependencyrefservice.Service,
	syncstateSvc syncstateservice.Service,
	cfg *config.Config,
	queuesProvider queues.Provider,
	workerClient worker_client.WorkerClient,
	workerMetrics *worker.WorkerCollector,
	findingStore vulnerabilityfindingstore.Store,
	vulnClient trustifyv2.VulnerabilityClient,
	depSyncMetrics *periodicmetrics.DependencySyncCollector,
) map[PeriodicTaskType]PeriodicTaskExecutor {
	executors := map[PeriodicTaskType]PeriodicTaskExecutor{
		PeriodicTaskTypeRepositoryTester: &RepositoryTesterExecutor{
			log:           log.WithField("pkg", "repository-tester"),
			repositorySvc: repositorySvc,
		},
		PeriodicTaskTypeResourceSync: &ResourceSyncExecutor{
			repositorySvc:   repositorySvc,
			fleetSvc:        fleetSvc,
			resourcesyncSvc: resourcesyncSvc,
			catalogSvc:      catalogSvc,
			log:             log.WithField("pkg", "resource-sync"),
			cfg:             cfg,
		},
		PeriodicTaskTypeDeviceConnection: &DeviceConnectionExecutor{
			log:       log.WithField("pkg", "device-connection"),
			deviceSvc: deviceSvc,
		},
		PeriodicTaskTypeRolloutDeviceSelection: &RolloutDeviceSelectionExecutor{
			deviceSvc: deviceSvc,
			fleetSvc:  fleetSvc,
			eventSvc:  eventSvc,
			log:       log.WithField("pkg", "rollout-device-selection"),
		},
		PeriodicTaskTypeDisruptionBudget: &DisruptionBudgetExecutor{
			deviceSvc: deviceSvc,
			fleetSvc:  fleetSvc,
			eventSvc:  eventSvc,
			log:       log.WithField("pkg", "disruption-budget"),
		},
		PeriodicTaskTypeEventCleanup: &EventCleanupExecutor{
			log:                  log.WithField("pkg", "event-cleanup"),
			eventSvc:             eventSvc,
			eventRetentionPeriod: cfg.Service.EventRetentionPeriod,
		},
		PeriodicTaskTypeQueueMaintenance: &QueueMaintenanceExecutor{
			log:             log.WithField("pkg", "queue-maintenance"),
			checkpointSvc:   checkpointSvc,
			eventSvc:        eventSvc,
			organizationSvc: organizationSvc,
			queuesProvider:  queuesProvider,
			workerClient:    workerClient,
			workerMetrics:   workerMetrics,
		},
	}

	if cfg.VulnerabilityReporting != nil && cfg.VulnerabilityReporting.Enabled && vulnClient != nil && findingStore != nil {
		executors[PeriodicTaskTypeVulnerabilitySync] = &VulnerabilitySyncExecutor{
			log:           log.WithField("pkg", "vulnerability-sync"),
			vulnClient:    vulnClient,
			findingStore:  findingStore,
			checkpointSvc: checkpointSvc,
			eventSvc:      eventSvc,
		}
	}

	executors[PeriodicTaskTypeDependencySyncGit] = &DependencySyncGitExecutor{
		log:              log.WithField("pkg", "dependency-sync-git"),
		dependencyrefSvc: dependencyrefSvc,
		eventSvc:         eventSvc,
		syncstateSvc:     syncstateSvc,
		cfg:              cfg,
		metrics:          depSyncMetrics,
	}

	executors[PeriodicTaskTypeDependencySyncHttp] = &DependencySyncHttpExecutor{
		log:              log.WithField("pkg", "dependency-sync-http"),
		dependencyrefSvc: dependencyrefSvc,
		eventSvc:         eventSvc,
		syncstateSvc:     syncstateSvc,
		cfg:              cfg,
		metrics:          depSyncMetrics,
	}

	return executors
}
