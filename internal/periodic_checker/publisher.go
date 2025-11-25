package periodic

import (
	"container/heap"
	"context"
	"fmt"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"golang.org/x/exp/rand"
)

const (
	DefaultOrgSyncInterval = 5 * time.Minute
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
	ListOrganizations(ctx context.Context, params api.ListOrganizationsParams) (*api.OrganizationList, api.Status)
}

type TaskChannelManager interface {
	PublishTask(ctx context.Context, taskRef PeriodicTaskReference) error
}

type PeriodicTaskPublisher struct {
	log            logrus.FieldLogger
	tasksMetadata  map[PeriodicTaskType]PeriodicTaskMetadata
	orgService     OrganizationService
	channelManager TaskChannelManager
	taskBackoff    *poll.Config

	// Heap for scheduled tasks
	taskHeap *TaskHeap

	// Organizations tracking
	organizations map[uuid.UUID]struct{}

	// Configurable intervals
	orgSyncInterval time.Duration

	wakeup chan struct{}
	wg     sync.WaitGroup

	// Shared mutex for heap and organizations
	mu sync.Mutex
}

type PeriodicTaskPublisherConfig struct {
	Log             logrus.FieldLogger
	OrgService      OrganizationService
	TasksMetadata   map[PeriodicTaskType]PeriodicTaskMetadata
	ChannelManager  TaskChannelManager
	OrgSyncInterval time.Duration
	TaskBackoff     *poll.Config
}

func NewPeriodicTaskPublisher(publisherConfig PeriodicTaskPublisherConfig) (*PeriodicTaskPublisher, error) {
	if publisherConfig.Log == nil {
		return nil, fmt.Errorf("log is required")
	}
	if publisherConfig.OrgService == nil {
		return nil, fmt.Errorf("org service is required")
	}
	if publisherConfig.ChannelManager == nil {
		return nil, fmt.Errorf("channel manager is required")
	}
	if publisherConfig.TasksMetadata == nil {
		return nil, fmt.Errorf("tasks metadata is required")
	}
	if publisherConfig.TaskBackoff == nil {
		return nil, fmt.Errorf("task backoff is required")
	}

	orgSyncInterval := publisherConfig.OrgSyncInterval
	if orgSyncInterval <= 0 {
		orgSyncInterval = DefaultOrgSyncInterval
	}

	return &PeriodicTaskPublisher{
		log:             publisherConfig.Log,
		orgService:      publisherConfig.OrgService,
		tasksMetadata:   publisherConfig.TasksMetadata,
		channelManager:  publisherConfig.ChannelManager,
		organizations:   make(map[uuid.UUID]struct{}),
		orgSyncInterval: orgSyncInterval,
		taskBackoff:     publisherConfig.TaskBackoff,
		taskHeap:        NewTaskHeap(),
		wakeup:          make(chan struct{}, 1),
	}, nil
}

// Run spins up the organization sync and the scheduling loop goroutines.
// It blocks until the context is done.
func (p *PeriodicTaskPublisher) Run(ctx context.Context) {
	p.log.Info("Starting periodic task publisher")

	// Initialize system-wide tasks
	p.addSystemWideTasks()

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

	for {
		p.mu.Lock()
		nextTask := p.taskHeap.Peek()
		p.mu.Unlock()

		if nextTask == nil {
			if err := p.waitForWakeup(ctx); err != nil {
				return
			}
		} else if time.Until(nextTask.NextRun) <= 0 {
			// Task ready now, process immediately
			p.publishReadyTasks(ctx)
		} else {
			if err := p.waitForTaskReadyOrWakeup(ctx, nextTask); err != nil {
				return
			}
		}
	}
}

func (p *PeriodicTaskPublisher) waitForWakeup(ctx context.Context) error {
	select {
	case <-p.wakeup:
		p.publishReadyTasks(ctx)
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *PeriodicTaskPublisher) waitForTaskReadyOrWakeup(ctx context.Context, task *ScheduledTask) error {
	taskTimer := time.NewTimer(time.Until(task.NextRun))
	defer taskTimer.Stop()

	select {
	case <-taskTimer.C:
		p.publishReadyTasks(ctx)
	case <-p.wakeup:
		p.publishReadyTasks(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func (p *PeriodicTaskPublisher) publishReadyTasks(ctx context.Context) {
	for {
		task := p.getNextReadyTask()
		if task == nil {
			break
		}

		if !p.isOrgRegistered(task.OrgID, task.TaskType) {
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
}

func (p *PeriodicTaskPublisher) getNextReadyTask() *ScheduledTask {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.taskHeap.Len() == 0 {
		return nil
	}

	task := p.taskHeap.Peek()
	if task == nil || task.NextRun.After(time.Now()) {
		return nil
	}

	return heap.Pop(p.taskHeap).(*ScheduledTask)
}

func (p *PeriodicTaskPublisher) isOrgRegistered(orgID uuid.UUID, taskType PeriodicTaskType) bool {
	// Check if this is a system-wide task
	if metadata, exists := p.tasksMetadata[taskType]; exists && metadata.SystemWide {
		return true
	}

	// For organization-specific tasks, check if the org is registered
	p.mu.Lock()
	defer p.mu.Unlock()
	_, exists := p.organizations[orgID]
	return exists
}

func (p *PeriodicTaskPublisher) calculateBackoffDelay(retries int) time.Duration {
	return poll.CalculateBackoffDelay(p.taskBackoff, retries)
}

func (p *PeriodicTaskPublisher) rescheduleTask(task *ScheduledTask) {
	p.mu.Lock()
	defer p.mu.Unlock()

	task.NextRun = time.Now().Add(task.Interval)
	task.Retries = 0
	heap.Push(p.taskHeap, task)
}

func (p *PeriodicTaskPublisher) rescheduleTaskRetry(task *ScheduledTask) {
	p.mu.Lock()
	defer p.mu.Unlock()

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

	orgList, status := p.orgService.ListOrganizations(ctx, api.ListOrganizationsParams{})
	if status.Code < 200 || status.Code >= 300 {
		p.log.Errorf("Failed to list organizations: %v", status)
		return
	}

	currentOrgs := make(map[uuid.UUID]struct{})
	for _, org := range orgList.Items {
		orgID, err := uuid.Parse(lo.FromPtr(org.Metadata.Name))
		if err != nil {
			p.log.Errorf("Failed to parse organization ID %s: %v", lo.FromPtr(org.Metadata.Name), err)
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
		// Skip system-wide tasks when adding organization-specific tasks
		if metadata.SystemWide {
			continue
		}

		// Stagger task runs by a random amount up to 50% of the interval (with a floor)
		staggerRange := metadata.Interval / 2
		if staggerRange <= 0 {
			staggerRange = time.Millisecond
		}
		stagger := time.Duration(rand.Intn(int(staggerRange)))
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

	p.mu.Lock()
	for _, task := range tasks {
		heap.Push(p.taskHeap, task)
	}
	p.mu.Unlock()

	p.signalWakeup()
}

// addSystemWideTasks adds system-wide tasks that run once for the entire system
func (p *PeriodicTaskPublisher) addSystemWideTasks() {
	tasks := make([]*ScheduledTask, 0)
	now := time.Now()

	for taskType, metadata := range p.tasksMetadata {
		// Only add system-wide tasks
		if !metadata.SystemWide {
			continue
		}

		// Stagger task runs by a random amount up to 50% of the interval (with a floor)
		staggerRange := metadata.Interval / 2
		if staggerRange <= 0 {
			staggerRange = time.Millisecond
		}
		stagger := time.Duration(rand.Intn(int(staggerRange)))
		nextRun := now.Add(stagger)

		// System-wide tasks use a special "system" orgID (nil UUID)
		task := &ScheduledTask{
			NextRun:  nextRun,
			OrgID:    uuid.Nil, // Use nil UUID to indicate system-wide task
			TaskType: taskType,
			Interval: metadata.Interval,
			Retries:  0,
		}
		tasks = append(tasks, task)
	}

	if len(tasks) > 0 {
		p.mu.Lock()
		for _, task := range tasks {
			heap.Push(p.taskHeap, task)
		}
		p.mu.Unlock()

		p.signalWakeup()
	}
}

func (p *PeriodicTaskPublisher) diffOrganizations(currentOrgs map[uuid.UUID]struct{}) (toAdd []uuid.UUID, toRemove []uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()

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

	p.mu.Lock()
	defer p.mu.Unlock()

	newHeap := TaskHeap{}
	for _, task := range *p.taskHeap {
		if _, shouldRemove := removeSet[task.OrgID]; !shouldRemove {
			newHeap = append(newHeap, task)
		}
	}

	// Re-build the heap with remaining tasks
	heap.Init(&newHeap)
	*p.taskHeap = newHeap

	p.signalWakeup()
}

func (p *PeriodicTaskPublisher) signalWakeup() {
	select {
	case p.wakeup <- struct{}{}:
	default:
	}
}

func (p *PeriodicTaskPublisher) clearHeap() {
	p.mu.Lock()
	*p.taskHeap = TaskHeap{}
	p.mu.Unlock()
}
