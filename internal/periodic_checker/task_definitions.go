package periodic

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/rollout/device_selection"
	"github.com/flightctl/flightctl/internal/rollout/disruption_budget"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/reqid"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type PeriodicTaskType string

const (
	PeriodicTaskTypeRepositoryTester       PeriodicTaskType = "repository-tester"
	PeriodicTaskTypeResourceSync           PeriodicTaskType = "resource-sync"
	PeriodicTaskTypeDeviceDisconnected     PeriodicTaskType = "device-disconnected"
	PeriodicTaskTypeRolloutDeviceSelection PeriodicTaskType = "rollout-device-selection"
	PeriodicTaskTypeDisruptionBudget       PeriodicTaskType = "disruption-budget"
	PeriodicTaskTypeEventCleanup           PeriodicTaskType = "event-cleanup"
)

type PeriodicTaskMetadata struct {
	Interval time.Duration
}

var periodicTasks = map[PeriodicTaskType]PeriodicTaskMetadata{
	PeriodicTaskTypeRepositoryTester:       {Interval: 2 * time.Minute},
	PeriodicTaskTypeResourceSync:           {Interval: 2 * time.Minute},
	PeriodicTaskTypeDeviceDisconnected:     {Interval: tasks.DeviceDisconnectedPollingInterval},
	PeriodicTaskTypeRolloutDeviceSelection: {Interval: device_selection.RolloutDeviceSelectionInterval},
	PeriodicTaskTypeDisruptionBudget:       {Interval: disruption_budget.DisruptionBudgetReconcilationInterval},
	PeriodicTaskTypeEventCleanup:           {Interval: tasks.EventCleanupPollingInterval},
}

type PeriodicTaskReference struct {
	Type  PeriodicTaskType
	OrgID uuid.UUID
}

type PeriodicTaskExecutor interface {
	Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID)
}

type RepositoryTesterExecutor struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
}

// createTaskContext creates a task context with request ID, orgID, and event actor
func createTaskContext(ctx context.Context, taskType PeriodicTaskType, orgID uuid.UUID) context.Context {
	taskName := string(taskType)
	reqid.OverridePrefix(taskName)
	requestID := reqid.NextRequestID()
	ctx = context.WithValue(ctx, middleware.RequestIDKey, requestID)

	ctx = util.WithOrganizationID(ctx, orgID)

	return context.WithValue(ctx, consts.EventActorCtxKey, taskName)
}

func (e *RepositoryTesterExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeRepositoryTester, orgID)
	repoTester := tasks.NewRepoTester(e.log, e.serviceHandler)
	repoTester.TestRepositories(taskCtx)
}

type ResourceSyncExecutor struct {
	serviceHandler        service.Service
	log                   logrus.FieldLogger
	ignoreResourceUpdates []string
}

func (e *ResourceSyncExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeResourceSync, orgID)
	resourceSync := tasks.NewResourceSync(e.serviceHandler, e.log, e.ignoreResourceUpdates)
	resourceSync.Poll(taskCtx)
}

type DeviceDisconnectedExecutor struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
}

func (e *DeviceDisconnectedExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDeviceDisconnected, orgID)
	deviceDisconnected := tasks.NewDeviceDisconnected(e.log, e.serviceHandler)
	deviceDisconnected.Poll(taskCtx)
}

type RolloutDeviceSelectionExecutor struct {
	serviceHandler service.Service
	log            logrus.FieldLogger
}

func (e *RolloutDeviceSelectionExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeRolloutDeviceSelection, orgID)
	rolloutDeviceSelection := device_selection.NewReconciler(e.serviceHandler, e.log)
	rolloutDeviceSelection.Reconcile(taskCtx)
}

type DisruptionBudgetExecutor struct {
	serviceHandler service.Service
	log            logrus.FieldLogger
}

func (e *DisruptionBudgetExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeDisruptionBudget, orgID)
	disruptionBudget := disruption_budget.NewReconciler(e.serviceHandler, e.log)
	disruptionBudget.Reconcile(taskCtx)
}

type EventCleanupExecutor struct {
	log                  logrus.FieldLogger
	serviceHandler       service.Service
	eventRetentionPeriod util.Duration
}

func (e *EventCleanupExecutor) Execute(ctx context.Context, log logrus.FieldLogger, orgID uuid.UUID) {
	taskCtx := createTaskContext(ctx, PeriodicTaskTypeEventCleanup, orgID)
	eventCleanup := tasks.NewEventCleanup(e.log, e.serviceHandler, e.eventRetentionPeriod)
	eventCleanup.Poll(taskCtx)
}

func InitializeTaskExecutors(log logrus.FieldLogger, serviceHandler service.Service, cfg *config.Config) map[PeriodicTaskType]PeriodicTaskExecutor {
	return map[PeriodicTaskType]PeriodicTaskExecutor{
		PeriodicTaskTypeRepositoryTester: &RepositoryTesterExecutor{
			log:            log.WithField("pkg", "repository-tester"),
			serviceHandler: serviceHandler,
		},
		PeriodicTaskTypeResourceSync: &ResourceSyncExecutor{
			serviceHandler:        serviceHandler,
			log:                   log.WithField("pkg", "resource-sync"),
			ignoreResourceUpdates: cfg.GitOps.IgnoreResourceUpdates,
		},
		PeriodicTaskTypeDeviceDisconnected: &DeviceDisconnectedExecutor{
			log:            log.WithField("pkg", "device-disconnected"),
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
	}
}
