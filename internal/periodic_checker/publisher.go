package periodic

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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
	// RedisKeyPeriodicTaskLastPublish is the Redis key pattern for storing the last publish time of periodic tasks
	RedisKeyPeriodicTaskLastPublish = "periodic-task:last-publish:"
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

	mu            sync.RWMutex
	organizations map[uuid.UUID]bool

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
	orgIDs := p.getTrackedOrganizations()

	for _, orgID := range orgIDs {
		for _, taskMetadata := range p.tasksMetadata {
			if err := ctx.Err(); err != nil {
				p.log.Info("Context cancelled, stopping task publishing")
				return
			}

			p.processPeriodicTask(ctx, orgID, taskMetadata)
		}
	}
}

func (p *PeriodicTaskPublisher) getTrackedOrganizations() []uuid.UUID {
	p.mu.RLock()
	defer p.mu.RUnlock()

	orgIDs := make([]uuid.UUID, 0, len(p.organizations))
	for orgID := range p.organizations {
		orgIDs = append(orgIDs, orgID)
	}
	return orgIDs
}

func (p *PeriodicTaskPublisher) processPeriodicTask(ctx context.Context, orgID uuid.UUID, taskMetadata PeriodicTaskMetadata) {
	taskKey := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastPublish, taskMetadata.TaskType, orgID)

	lastPublish, err := p.getLastPublishTime(ctx, taskKey)
	if err != nil {
		p.log.Errorf("Failed to get last publish time for task %s: %v", taskKey, err)
		return
	}

	if p.shouldPublishTask(lastPublish, taskMetadata.Interval) {
		if err := p.publishTaskAndUpdateTime(ctx, taskKey, taskMetadata.TaskType, orgID); err != nil {
			p.log.Errorf("Failed to publish and update task %s: %v", taskKey, err)
		}
	}
}

func (p *PeriodicTaskPublisher) getLastPublishTime(ctx context.Context, taskKey string) (time.Time, error) {
	lastPublishBytes, err := p.kvStore.Get(ctx, taskKey)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get task from kvstore: %w", err)
	}

	if lastPublishBytes == nil {
		p.log.Infof("Task %s does not exist, will publish task", taskKey)
		return time.Unix(0, 0), nil
	}

	var lastPublish PeriodicTaskLastPublish
	if err := json.Unmarshal(lastPublishBytes, &lastPublish); err != nil {
		p.log.Errorf("Failed to unmarshal task %s, using default time: %v", taskKey, err)
		return time.Unix(0, 0), nil
	}

	return lastPublish.LastPublish, nil
}

func (p *PeriodicTaskPublisher) shouldPublishTask(lastPublish time.Time, interval time.Duration) bool {
	return lastPublish.Before(time.Now().Add(-interval))
}

func (p *PeriodicTaskPublisher) publishTaskAndUpdateTime(ctx context.Context, taskKey string, taskType PeriodicTaskType, orgID uuid.UUID) error {
	if err := p.publishTask(ctx, taskType, orgID); err != nil {
		return fmt.Errorf("failed to publish task: %w", err)
	}

	lastPublish := PeriodicTaskLastPublish{
		LastPublish: time.Now(),
	}

	lastPublishJSON, err := json.Marshal(lastPublish)
	if err != nil {
		return fmt.Errorf("failed to marshal last publish: %w", err)
	}

	if err := p.kvStore.Set(ctx, taskKey, lastPublishJSON); err != nil {
		return fmt.Errorf("failed to set last publish: %w", err)
	}

	return nil
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
	go func() {
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
	}()
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

		p.mu.RLock()
		isRegistered := p.organizations[orgId]
		p.mu.RUnlock()

		if !isRegistered {
			p.log.Infof("Registering organization %s", orgId)
		}

		organizations[orgId] = true
	}

	p.mu.Lock()
	defer p.mu.Unlock()

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
	p.mu.Lock()
	p.organizations = make(map[uuid.UUID]bool)
	p.mu.Unlock()
}

// Helper functions used in testing
func (p *PeriodicTaskPublisher) getOrganizationCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.organizations)
}

func (p *PeriodicTaskPublisher) addOrganization(orgID uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.organizations == nil {
		p.organizations = make(map[uuid.UUID]bool)
	}
	p.organizations[orgID] = true
}

func (p *PeriodicTaskPublisher) hasOrganization(orgID uuid.UUID) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.organizations[orgID]
}

func (p *PeriodicTaskPublisher) SetTaskTicker(interval time.Duration) {
	p.taskTickerInterval = interval
}
