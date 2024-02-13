package tasks

import (
	"context"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks/repotester"
	"github.com/flightctl/flightctl/pkg/thread"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

type ResourceReference struct {
	OrgID uuid.UUID
	Kind  string
	Name  string
}

type TaskManager struct {
	log        logrus.FieldLogger
	ctx        context.Context
	cancelFunc context.CancelFunc
	channels   map[string]chan ResourceReference
	store      store.Store
	threads    []*thread.Thread
	once       *sync.Once
}

const (
	FleetTemplateRollout = "fleet-template-rollout"

	ChannelSize = 20
)

func Init(log logrus.FieldLogger, store store.Store) TaskManager {
	ctx := context.Background()
	ctxWithCancel, cancelFunc := context.WithCancel(ctx)

	channels := make(map[string](chan ResourceReference))
	channels[FleetTemplateRollout] = make(chan ResourceReference, ChannelSize)

	return TaskManager{
		log:        log,
		ctx:        ctxWithCancel,
		cancelFunc: cancelFunc,
		channels:   channels,
		store:      store,
		threads:    make([]*thread.Thread, 2),
		once:       new(sync.Once),
	}
}

func (t TaskManager) Start() {
	repoTester := repotester.NewRepoTester(t.log, t.store)
	repoTesterThread := thread.New(
		t.log.WithField("pkg", "repository-tester"), "Repository tester", threadIntervalMinute(2), repoTester.TestRepo)
	repoTesterThread.Start()

	go FleetRollouts(t)

	resourceSync := NewResourceSync(t)
	resourceSyncThread := thread.New(
		t.log.WithField("pkg", "resourcesync"), "ResourceSync", threadIntervalMinute(2), resourceSync.Poll)
	resourceSyncThread.Start()

	t.threads[0] = repoTesterThread
	t.threads[1] = resourceSyncThread
}

func (t TaskManager) Stop() {
	t.once.Do(func() {
		for _, thread := range t.threads {
			thread.Stop()
		}
		t.cancelFunc()
		for c := range t.channels {
			close(t.channels[c])
		}
	})
}

func (t TaskManager) SubmitTask(taskName string, resource ResourceReference) {
	t.channels[taskName] <- resource
}

func (t TaskManager) GetTask(taskName string) ResourceReference {
	return <-t.channels[taskName]
}

func (t TaskManager) FleetTemplateRolloutCallback(orgId uuid.UUID, name *string, templateUpdated bool) {
	t.SubmitTask(FleetTemplateRollout, ResourceReference{OrgID: orgId, Name: *name})
}

func threadIntervalMinute(min float64) time.Duration {
	return time.Duration(min * float64(time.Minute))
}
