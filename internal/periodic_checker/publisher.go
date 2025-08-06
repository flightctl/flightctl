package periodic

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

var (
	DefaultTaskTickerInterval = 5 * time.Second
	DefaultOrgTickerInterval  = 5 * time.Minute
)

type OrgTaskMetadata struct {
	TaskLastPublish map[PeriodicTaskType]time.Time
}

type OrganizationService interface {
	ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status)
}

type TaskChannelManager interface {
	PublishTask(ctx context.Context, taskRef PeriodicTaskReference) error
}

type PeriodicTaskPublisher struct {
	log            logrus.FieldLogger
	tasksMetadata  map[PeriodicTaskType]PeriodicTaskMetadata
	orgService     OrganizationService
	channelManager TaskChannelManager

	orgTasksMetadata map[uuid.UUID]*OrgTaskMetadata

	// Configurable intervals
	taskTickerInterval time.Duration
	orgTickerInterval  time.Duration
}

type PeriodicTaskPublisherConfig struct {
	Log                logrus.FieldLogger
	OrgService         OrganizationService
	TasksMetadata      map[PeriodicTaskType]PeriodicTaskMetadata
	ChannelManager     TaskChannelManager
	TaskTickerInterval time.Duration
	OrgTickerInterval  time.Duration
}

func NewPeriodicTaskPublisher(publisherConfig PeriodicTaskPublisherConfig) (*PeriodicTaskPublisher, error) {
	var taskTickerInterval, orgTickerInterval = publisherConfig.TaskTickerInterval, publisherConfig.OrgTickerInterval

	// if tickers are not set use default values
	if taskTickerInterval == 0 {
		taskTickerInterval = DefaultTaskTickerInterval
	}
	if orgTickerInterval == 0 {
		orgTickerInterval = DefaultOrgTickerInterval
	}
	return &PeriodicTaskPublisher{
		log:                publisherConfig.Log,
		orgService:         publisherConfig.OrgService,
		tasksMetadata:      publisherConfig.TasksMetadata,
		channelManager:     publisherConfig.ChannelManager,
		taskTickerInterval: taskTickerInterval,
		orgTickerInterval:  orgTickerInterval,
		orgTasksMetadata:   make(map[uuid.UUID]*OrgTaskMetadata),
	}, nil
}

func (p *PeriodicTaskPublisher) publishTasks(ctx context.Context) {
	for orgID, metaData := range p.orgTasksMetadata {
		for taskType, lastPublish := range metaData.TaskLastPublish {
			if p.shouldPublishTask(lastPublish, p.tasksMetadata[taskType].Interval) {
				if err := p.publishTaskAndUpdateTime(ctx, orgID, taskType); err != nil {
					p.log.Errorf("Failed to publish task %s for organization %s: %v", taskType, orgID, err)
				}
			}
		}
	}
}

func (p *PeriodicTaskPublisher) shouldPublishTask(lastPublish time.Time, interval time.Duration) bool {
	return lastPublish.Before(time.Now().Add(-interval))
}

func (p *PeriodicTaskPublisher) publishTaskAndUpdateTime(ctx context.Context, orgID uuid.UUID, taskType PeriodicTaskType) error {
	if err := p.publishTask(ctx, taskType, orgID); err != nil {
		return fmt.Errorf("failed to publish task: %w", err)
	}

	// Ensure org metadata exists
	orgMetadata, exists := p.orgTasksMetadata[orgID]
	if !exists {
		orgMetadata = &OrgTaskMetadata{
			TaskLastPublish: make(map[PeriodicTaskType]time.Time),
		}
		p.orgTasksMetadata[orgID] = orgMetadata
	}

	// Update the last publish time for this task type
	orgMetadata.TaskLastPublish[taskType] = time.Now()

	return nil
}

func (p *PeriodicTaskPublisher) publishTask(ctx context.Context, taskType PeriodicTaskType, orgID uuid.UUID) error {
	taskReference := PeriodicTaskReference{
		Type:  taskType,
		OrgID: orgID,
	}

	if err := p.channelManager.PublishTask(ctx, taskReference); err != nil {
		p.log.Errorf("Failed to schedule task: %v", err)
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
			p.clearOrganizations()
			p.log.Info("Publisher stopped")
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
	currentOrgs := make(map[uuid.UUID]bool)

	for _, org := range orgList.Items {
		orgID, err := uuid.Parse(*org.Metadata.Name)
		if err != nil {
			p.log.Errorf("Failed to parse organization ID %s: %v", *org.Metadata.Name, err)
			continue
		}

		_, exists := p.orgTasksMetadata[orgID]
		if !exists {
			p.log.Infof("Registering organization %s", orgID)
		}
		currentOrgs[orgID] = true
	}

	// Remove entries for organizations that are no longer tracked
	for orgID := range p.orgTasksMetadata {
		if !currentOrgs[orgID] {
			p.log.Infof("Organization %s is no longer registered, removing from tracking", orgID)
			delete(p.orgTasksMetadata, orgID)
		}
	}

	// Add new organizations to tracking
	// Task last publish times are set to 0 unix time, which will result in a publish
	// when the tasks are processed
	for orgID := range currentOrgs {
		if _, exists := p.orgTasksMetadata[orgID]; !exists {
			taskLastPublish := make(map[PeriodicTaskType]time.Time)
			for taskType := range p.tasksMetadata {
				taskLastPublish[taskType] = time.Unix(0, 0)
			}
			p.orgTasksMetadata[orgID] = &OrgTaskMetadata{
				TaskLastPublish: taskLastPublish,
			}
		}
	}
}

func (p *PeriodicTaskPublisher) clearOrganizations() {
	// Clear all organization task metadata
	p.orgTasksMetadata = make(map[uuid.UUID]*OrgTaskMetadata)
}
