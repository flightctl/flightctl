package periodic

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

var (
	DefaultTaskTickerInterval = 5 * time.Second
	DefaultOrgTickerInterval  = 5 * time.Minute
)

const (
	// RedisKeyPeriodicTaskLastRun is the Redis key pattern for storing the last run time of periodic tasks
	RedisKeyPeriodicTaskLastRun = "periodic_task:last_run:"
)

type OrganizationService interface {
	ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status)
}

type PeriodicTaskPublisher struct {
	publisher     queues.Publisher
	log           logrus.FieldLogger
	tasksMetadata []PeriodicTaskMetadata
	orgService    OrganizationService
	kvStore       kvstore.KVStore
	organizations map[uuid.UUID]bool // Track which organizations are registered

	// Configurable intervals, currently only overridden for testing
	taskTickerInterval time.Duration
	orgTickerInterval  time.Duration
}

func NewPeriodicTaskPublisher(log logrus.FieldLogger, kvStore kvstore.KVStore, orgService OrganizationService, queuesProvider queues.Provider, tasksMetadata []PeriodicTaskMetadata) (*PeriodicTaskPublisher, error) {
	publisher, err := queuesProvider.NewPublisher(consts.PeriodicTaskQueue)
	if err != nil {
		log.WithError(err).Error("failed to create periodic task publisher")
		return nil, err
	}
	return &PeriodicTaskPublisher{
		publisher:          publisher,
		log:                log,
		orgService:         orgService,
		kvStore:            kvStore,
		tasksMetadata:      tasksMetadata,
		taskTickerInterval: DefaultTaskTickerInterval,
		orgTickerInterval:  DefaultOrgTickerInterval,
	}, nil
}

func (p *PeriodicTaskPublisher) publishTasks(ctx context.Context) {
	for orgID := range p.organizations {
		for _, taskMetadata := range p.tasksMetadata {
			// Check if context has been cancelled before processing each task
			select {
			case <-ctx.Done():
				p.log.Info("Context cancelled, stopping task publishing")
				return
			default:
			}

			taskKey := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastRun, taskMetadata.TaskType, orgID)

			// Default last run to 0 Unix time
			lastRun := PeriodicTaskLastRun{
				LastRun: time.Unix(0, 0),
			}

			lastRunBytes, err := p.kvStore.Get(ctx, taskKey)

			if err != nil {
				p.log.Errorf("Failed to get task %s: %v", taskKey, err)
				continue
			} else if lastRunBytes == nil {
				// Key may not exist on the first run, so log and proceed to publish task
				p.log.Infof("Task %s does not exist, publishing task", taskKey)
			} else {
				if err := json.Unmarshal(lastRunBytes, &lastRun); err != nil {
					// Invalid JSON, log and proceed with default lastRun
					p.log.Errorf("Failed to unmarshal task %s: %v", taskKey, err)
				}
			}

			if lastRun.LastRun.Before(time.Now().Add(-taskMetadata.Interval)) {
				if err := p.publishTask(ctx, taskMetadata.TaskType, orgID); err != nil {
					p.log.Errorf("Failed to publish task: %v", err)
					continue
				}
				lastRun.LastRun = time.Now()
				lastRunJSON, err := json.Marshal(lastRun)
				if err != nil {
					p.log.Errorf("Failed to marshal last run: %v", err)
					continue
				}
				if err := p.kvStore.Set(ctx, taskKey, lastRunJSON); err != nil {
					p.log.Errorf("Failed to set last run: %v", err)
					continue
				}
			}
		}
	}
}

func (p *PeriodicTaskPublisher) publishTask(ctx context.Context, taskType PeriodicTaskType, orgID uuid.UUID) error {
	taskReference := PeriodicTaskReference{
		Type:  taskType,
		OrgID: orgID,
	}

	taskReferenceJSON, err := json.Marshal(taskReference)
	if err != nil {
		p.log.Errorf("Failed to marshal task reference: %v", err)
		return err
	}

	if err := p.publisher.Publish(ctx, taskReferenceJSON); err != nil {
		p.log.Errorf("Failed to publish task: %v", err)
		return err
	}

	return nil
}

func (p *PeriodicTaskPublisher) Start(ctx context.Context) {
	p.syncOrganizations(ctx)

	taskTicker := time.NewTicker(p.taskTickerInterval)
	defer taskTicker.Stop()
	orgTicker := time.NewTicker(p.orgTickerInterval)
	defer orgTicker.Stop()

	for {
		select {
		case <-taskTicker.C:
			p.publishTasks(ctx)
		case <-orgTicker.C:
			p.syncOrganizations(ctx)
		case <-ctx.Done():
			p.stopAll()
			return
		}
	}
}

func (p *PeriodicTaskPublisher) syncOrganizations(ctx context.Context) {
	p.log.Info("Syncing organizations")

	orgList, status := p.orgService.ListOrganizations(ctx)
	if status.Code < 200 || status.Code >= 300 {
		p.log.Errorf("Failed to list organizations: %v", status)
		return
	}

	// Track which organizations we've seen
	organizations := make(map[uuid.UUID]bool)

	for _, org := range orgList.Items {
		orgId, err := uuid.Parse(*org.Metadata.Name)
		if err != nil {
			p.log.Errorf("Failed to parse organization ID %s: %v", *org.Metadata.Name, err)
			continue
		}

		if !p.organizations[orgId] {
			p.log.Infof("Registering organization %s", orgId)
		}

		organizations[orgId] = true
	}

	for orgID := range p.organizations {
		if !organizations[orgID] {
			p.log.Infof("Organization %s is no longer registered, removing from tracking", orgID)
			delete(p.organizations, orgID)
		}
	}

	p.organizations = organizations
}

func (p *PeriodicTaskPublisher) stopAll() {
	// Clear all organizations
	p.organizations = make(map[uuid.UUID]bool)
}
