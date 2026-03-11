package periodic

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/instrumentation/metrics/worker"
	"github.com/flightctl/flightctl/internal/rollout/device_selection"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks"
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
}

// MergeTasksWithConfig merges configured task intervals with defaults.
// Configured intervals override defaults; unconfigured tasks use defaults.
func MergeTasksWithConfig(cfg *config.Config) map[PeriodicTaskType]PeriodicTaskMetadata {
	merged := make(map[PeriodicTaskType]PeriodicTaskMetadata, len(periodicTasks))
	for k, v := range periodicTasks {
		merged[k] = v
	}

	if cfg == nil || cfg.Periodic == nil {
		return merged
	}

	tasks := cfg.Periodic.Tasks
	if tasks.ResourceSync.Schedule.Interval > 0 {
		meta := merged[PeriodicTaskTypeResourceSync]
		meta.Interval = time.Duration(tasks.ResourceSync.Schedule.Interval)
		merged[PeriodicTaskTypeResourceSync] = meta
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
	log            logrus.FieldLogger
	serviceHandler service.Service
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
	repoTester := tasks.NewRepoTester(e.log, e.serviceHandler, nil)
	repoTester.TestRepositories(taskCtx, orgId)
}

type ResourceSyncExecutor struct {
	serviceHandler service.Service
	log            logrus.FieldLogger
	cfg            *config.Config
}

func (e *ResourceSyncExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeResourceSync)
	var ignoreResourceUpdates []string
	if e.cfg != nil && e.cfg.GitOps != nil {
		ignoreResourceUpdates = e.cfg.GitOps.IgnoreResourceUpdates
	}
	resourceSync := tasks.NewResourceSync(e.serviceHandler, e.log, e.cfg, ignoreResourceUpdates)
	resourceSync.Poll(taskCtx, orgId)
}

type DeviceConnectionExecutor struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
}

func (e *DeviceConnectionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDeviceConnection)
	deviceConnection := tasks.NewDeviceConnection(e.log, e.serviceHandler)
	deviceConnection.Poll(taskCtx, orgId)
}

type RolloutDeviceSelectionExecutor struct {
	serviceHandler service.Service
	log            logrus.FieldLogger
}

func (e *RolloutDeviceSelectionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeRolloutDeviceSelection)
	rolloutDeviceSelection := device_selection.NewReconciler(e.serviceHandler, e.log)
	rolloutDeviceSelection.Reconcile(taskCtx, orgId)
}

type DisruptionBudgetExecutor struct {
	serviceHandler service.Service
	log            logrus.FieldLogger
}

func (e *DisruptionBudgetExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDisruptionBudget)
	disruptionBudget := disruption_budget.NewReconciler(e.serviceHandler, e.log)
	disruptionBudget.Reconcile(taskCtx, orgId)
}

type EventCleanupExecutor struct {
	log                  logrus.FieldLogger
	serviceHandler       service.Service
	eventRetentionPeriod util.Duration
}

func (e *EventCleanupExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeEventCleanup)
	// Note: Event cleanup is system-wide, orgId is not used
	eventCleanup := tasks.NewEventCleanup(e.log, e.serviceHandler, e.eventRetentionPeriod)
	eventCleanup.Poll(taskCtx)
}

type QueueMaintenanceExecutor struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	queuesProvider queues.Provider
	workerClient   worker_client.WorkerClient
	workerMetrics  *worker.WorkerCollector
}

func (e *QueueMaintenanceExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgId uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeQueueMaintenance)

	// Create and execute the queue maintenance task
	// Note: Queue maintenance is system-wide, orgId is not used
	task := tasks.NewQueueMaintenanceTask(e.log, e.serviceHandler, e.queuesProvider, e.workerClient, e.workerMetrics)

	if err := task.Execute(taskCtx); err != nil {
		e.log.WithError(err).Error("Queue maintenance task failed")
	}
}

func InitializeTaskExecutors(log logrus.FieldLogger, serviceHandler service.Service, cfg *config.Config, queuesProvider queues.Provider, workerClient worker_client.WorkerClient, workerMetrics *worker.WorkerCollector) map[PeriodicTaskType]PeriodicTaskExecutor {
	return map[PeriodicTaskType]PeriodicTaskExecutor{
		PeriodicTaskTypeRepositoryTester: &RepositoryTesterExecutor{
			log:            log.WithField("pkg", "repository-tester"),
			serviceHandler: serviceHandler,
		},
		PeriodicTaskTypeResourceSync: &ResourceSyncExecutor{
			serviceHandler: serviceHandler,
			log:            log.WithField("pkg", "resource-sync"),
			cfg:            cfg,
		},
		PeriodicTaskTypeDeviceConnection: &DeviceConnectionExecutor{
			log:            log.WithField("pkg", "device-connection"),
			serviceHandler: serviceHandler,
		},
		PeriodicTaskTypeRolloutDeviceSelection: &RolloutDeviceSelectionExecutor{
			serviceHandler: serviceHandler,
			log:            log.WithField("pkg", "rollout-device-selection"),
		},
		PeriodicTaskTypeDisruptionBudget: &DisruptionBudgetExecutor{
			serviceHandler: serviceHandler,
			log:            log.WithField("pkg", "disruption-budget"),
		},
		PeriodicTaskTypeEventCleanup: &EventCleanupExecutor{
			log:                  log.WithField("pkg", "event-cleanup"),
			serviceHandler:       serviceHandler,
			eventRetentionPeriod: cfg.Service.EventRetentionPeriod,
		},
		PeriodicTaskTypeQueueMaintenance: &QueueMaintenanceExecutor{
			log:            log.WithField("pkg", "queue-maintenance"),
			serviceHandler: serviceHandler,
			queuesProvider: queuesProvider,
			workerClient:   workerClient,
			workerMetrics:  workerMetrics,
		},
	}
}
