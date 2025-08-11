package periodic

import (
	"container/heap"
	"context"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/rand"
)

var (
	DefaultTaskTickerInterval = 1 * time.Second
	DefaultOrgSyncInterval    = 5 * time.Minute
)

type ScheduledTask struct {
	NextRun  time.Time
	OrgID    uuid.UUID
	TaskType PeriodicTaskType
	Interval time.Duration
	Retries  int
}

type TaskHeap []*ScheduledTask

func (h TaskHeap) Len() int           { return len(h) }
func (h TaskHeap) Less(i, j int) bool { return h[i].NextRun.Before(h[j].NextRun) }
func (h TaskHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *TaskHeap) Push(x interface{}) {
	*h = append(*h, x.(*ScheduledTask))
}

func (h *TaskHeap) Pop() interface{} {
	old := *h
	n := len(old)
	task := old[n-1]
	*h = old[0 : n-1]
	return task
}

func (h *TaskHeap) Peek() *ScheduledTask {
	if h.Len() == 0 {
		return nil
	}
	return (*h)[0]
}

func NewTaskHeap() *TaskHeap {
	h := &TaskHeap{}
	heap.Init(h)
	return h
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
	pollConfig     poll.Config

	// Heap for scheduled tasks
	taskHeap *TaskHeap
	heapMu   sync.Mutex

	// Organizations tracking
	organizations map[uuid.UUID]struct{}
	orgsMu        sync.Mutex

	// Configurable intervals
	orgSyncInterval time.Duration
	wg              sync.WaitGroup
}

type PeriodicTaskPublisherConfig struct {
	Log             logrus.FieldLogger
	OrgService      OrganizationService
	TasksMetadata   map[PeriodicTaskType]PeriodicTaskMetadata
	ChannelManager  TaskChannelManager
	OrgSyncInterval time.Duration
	PollConfig      poll.Config
}

func NewPeriodicTaskPublisher(publisherConfig PeriodicTaskPublisherConfig) (*PeriodicTaskPublisher, error) {
	orgSyncInterval := publisherConfig.OrgSyncInterval
	if orgSyncInterval == 0 {
		orgSyncInterval = DefaultOrgSyncInterval
	}

	return &PeriodicTaskPublisher{
		log:             publisherConfig.Log,
		orgService:      publisherConfig.OrgService,
		tasksMetadata:   publisherConfig.TasksMetadata,
		channelManager:  publisherConfig.ChannelManager,
		organizations:   make(map[uuid.UUID]struct{}),
		orgSyncInterval: orgSyncInterval,
		pollConfig:      publisherConfig.PollConfig,
		taskHeap:        NewTaskHeap(),
	}, nil
}

func (p *PeriodicTaskPublisher) Start(ctx context.Context) {
	p.log.Info("Starting periodic task publisher")
	p.wg.Add(1)
	// Start organization sync goroutine
	go p.organizationSyncLoop(ctx)

	p.wg.Add(1)
	// Main scheduling loop
	go p.schedulingLoop(ctx)

	<-ctx.Done()
	p.wg.Wait()
	p.clearHeap()
	p.log.Info("Periodic task publisher stopped")
}

func (p *PeriodicTaskPublisher) schedulingLoop(ctx context.Context) {
	defer p.wg.Done()

	taskTimer := time.NewTimer(DefaultTaskTickerInterval)
	defer taskTimer.Stop()

	for {
		// Calculate next wake time
		p.heapMu.Lock()
		var nextWake time.Duration
		if p.taskHeap.Len() > 0 && p.taskHeap.Peek() != nil {
			nextWake = time.Until(p.taskHeap.Peek().NextRun)
			if nextWake < 0 {
				nextWake = 0
			}
		} else {
			nextWake = DefaultTaskTickerInterval
		}
		p.heapMu.Unlock()

		taskTimer.Stop()
		taskTimer = time.NewTimer(nextWake)

		select {
		case <-taskTimer.C:
			for {
				task := p.getNextReadyTask()
				if task == nil {
					break
				}

				if !p.isOrgRegistered(task.OrgID) {
					p.log.Infof("Organization %s not registered, removing associated task %s from tracking",
						task.OrgID, task.TaskType)
					continue
				}

				if err := p.publishTask(ctx, task.TaskType, task.OrgID); err != nil {
					p.log.Errorf("Failed to process next task: %v", err)
					p.rescheduleTaskRetry(task)
				} else {
					p.rescheduleTask(task)
				}
			}
		case <-ctx.Done():
			return
		}
	}
}

func (p *PeriodicTaskPublisher) getNextReadyTask() *ScheduledTask {
	p.heapMu.Lock()
	defer p.heapMu.Unlock()

	if p.taskHeap.Len() == 0 {
		return nil
	}

	task := p.taskHeap.Peek()
	if task == nil || task.NextRun.After(time.Now()) {
		return nil
	}

	return heap.Pop(p.taskHeap).(*ScheduledTask)
}

func (p *PeriodicTaskPublisher) isOrgRegistered(orgID uuid.UUID) bool {
	p.orgsMu.Lock()
	defer p.orgsMu.Unlock()
	_, exists := p.organizations[orgID]
	return exists
}

func (p *PeriodicTaskPublisher) calculateBackoffDelay(retries int) time.Duration {
	return poll.CalculateBackoffDelay(&p.pollConfig, retries)
}

func (p *PeriodicTaskPublisher) rescheduleTask(task *ScheduledTask) {
	p.heapMu.Lock()
	defer p.heapMu.Unlock()

	task.NextRun = time.Now().Add(task.Interval)
	task.Retries = 0
	heap.Push(p.taskHeap, task)
}

func (p *PeriodicTaskPublisher) rescheduleTaskRetry(task *ScheduledTask) {
	p.heapMu.Lock()
	defer p.heapMu.Unlock()

	task.Retries++
	backoff := p.calculateBackoffDelay(task.Retries)
	task.NextRun = time.Now().Add(backoff)
	heap.Push(p.taskHeap, task)
}

func (p *PeriodicTaskPublisher) publishTask(ctx context.Context, taskType PeriodicTaskType, orgID uuid.UUID) error {
	taskReference := PeriodicTaskReference{
		Type:  taskType,
		OrgID: orgID,
	}

	return p.channelManager.PublishTask(ctx, taskReference)
}

func (p *PeriodicTaskPublisher) organizationSyncLoop(ctx context.Context) {
	defer p.wg.Done()

	// Initial sync of organizations
	p.syncOrganizations(ctx)

	ticker := time.NewTicker(p.orgSyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			p.syncOrganizations(ctx)
		case <-ctx.Done():
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

	currentOrgs := make(map[uuid.UUID]struct{})
	for _, org := range orgList.Items {
		orgID, err := uuid.Parse(*org.Metadata.Name)
		if err != nil {
			p.log.Errorf("Failed to parse organization ID %s: %v", *org.Metadata.Name, err)
			continue
		}
		currentOrgs[orgID] = struct{}{}
	}

	toAdd, toRemove := p.diffOrganizations(currentOrgs)

	for _, orgID := range toAdd {
		p.log.Infof("Registering organization %s", orgID)
		p.addOrganizationTasks(orgID)
	}

	if len(toRemove) > 0 {
		p.removeOrganizationTasks(toRemove)
	}
}

func (p *PeriodicTaskPublisher) addOrganizationTasks(orgID uuid.UUID) {
	tasks := make([]*ScheduledTask, 0, len(p.tasksMetadata))
	now := time.Now()

	for taskType, metadata := range p.tasksMetadata {
		// Stagger task runs by a random amount up to 50% of the interval
		stagger := time.Duration(rand.Intn(int(metadata.Interval / 2)))
		nextRun := now.Add(stagger)

		task := &ScheduledTask{
			NextRun:  nextRun,
			OrgID:    orgID,
			TaskType: taskType,
			Interval: metadata.Interval,
			Retries:  0,
		}
		tasks = append(tasks, task)
	}

	p.heapMu.Lock()
	for _, task := range tasks {
		heap.Push(p.taskHeap, task)
	}
	p.heapMu.Unlock()
}

func (p *PeriodicTaskPublisher) diffOrganizations(currentOrgs map[uuid.UUID]struct{}) (toAdd []uuid.UUID, toRemove []uuid.UUID) {
	p.orgsMu.Lock()
	defer p.orgsMu.Unlock()

	for orgID := range currentOrgs {
		if _, exists := p.organizations[orgID]; !exists {
			toAdd = append(toAdd, orgID)
		}
	}

	for orgID := range p.organizations {
		if _, exists := currentOrgs[orgID]; !exists {
			toRemove = append(toRemove, orgID)
		}
	}

	p.organizations = currentOrgs

	return toAdd, toRemove
}

func (p *PeriodicTaskPublisher) removeOrganizationTasks(orgIDs []uuid.UUID) {
	if len(orgIDs) == 0 {
		return
	}

	removeSet := make(map[uuid.UUID]struct{})
	for _, orgID := range orgIDs {
		removeSet[orgID] = struct{}{}
		p.log.Infof("Removing tasks for organization %s", orgID)
	}

	p.heapMu.Lock()
	defer p.heapMu.Unlock()

	newHeap := TaskHeap{}
	for _, task := range *p.taskHeap {
		if _, shouldRemove := removeSet[task.OrgID]; !shouldRemove {
			newHeap = append(newHeap, task)
		}
	}

	// Re-build the heap with remaining tasks
	heap.Init(&newHeap)
	*p.taskHeap = newHeap
}

func (p *PeriodicTaskPublisher) clearHeap() {
	p.heapMu.Lock()
	*p.taskHeap = TaskHeap{}
	p.heapMu.Unlock()
}
