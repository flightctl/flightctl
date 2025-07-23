package periodic

import (
	"context"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

type mockOrganizationService struct {
	organizations *api.OrganizationList
	status        api.Status
	callCount     int
}

func (m *mockOrganizationService) ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status) {
	m.callCount++
	return m.organizations, m.status
}

func (m *mockOrganizationService) getCallCount() int {
	return m.callCount
}

type mockChannelManager struct {
	publishedTasks []PeriodicTaskReference
}

func (m *mockChannelManager) PublishTask(ctx context.Context, taskRef PeriodicTaskReference) error {
	m.publishedTasks = append(m.publishedTasks, taskRef)
	return nil
}

func (m *mockChannelManager) getPublishedTaskCount() int {
	return len(m.publishedTasks)
}

func createPublisherTestLogger() logrus.FieldLogger {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)
	return logger
}

func createTestOrganizations(count int) *api.OrganizationList {
	organizations := &api.OrganizationList{
		Items: make([]api.Organization, count),
	}

	for i := 0; i < count; i++ {
		orgID := uuid.New()
		orgIDStr := orgID.String()
		organizations.Items[i] = api.Organization{
			Metadata: api.ObjectMeta{
				Name: &orgIDStr,
			},
		}
	}

	return organizations
}

func createTestTaskMetadata() map[PeriodicTaskType]PeriodicTaskMetadata {
	return map[PeriodicTaskType]PeriodicTaskMetadata{
		PeriodicTaskTypeRepositoryTester: {Interval: 1 * time.Minute},
		PeriodicTaskTypeResourceSync:     {Interval: 2 * time.Minute},
	}
}

func TestNewPeriodicTaskPublisher_Success(t *testing.T) {
	log := createPublisherTestLogger()
	orgService := &mockOrganizationService{}
	tasksMetadata := createTestTaskMetadata()
	channelManager := &mockChannelManager{}

	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            log,
		OrgService:     orgService,
		ChannelManager: channelManager,
		TasksMetadata:  tasksMetadata,
	}
	publisher, err := NewPeriodicTaskPublisher(publisherConfig)

	require.NoError(t, err)
	require.NotNil(t, publisher)
	require.Equal(t, log, publisher.log)
	require.Equal(t, orgService, publisher.orgService)
	require.Equal(t, tasksMetadata, publisher.tasksMetadata)
	require.NotNil(t, publisher.orgTasksMetadata)
	require.Equal(t, DefaultTaskTickerInterval, publisher.taskTickerInterval)
	require.Equal(t, DefaultOrgTickerInterval, publisher.orgTickerInterval)
}

func TestPeriodicTaskPublisher_syncOrganizations_Success(t *testing.T) {
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}
	mockOrg := publisher.orgService.(*mockOrganizationService)

	orgs := createTestOrganizations(2)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.getCallCount())
	require.Equal(t, 2, len(publisher.orgTasksMetadata))

	org1ID, _ := uuid.Parse(*orgs.Items[0].Metadata.Name)
	org2ID, _ := uuid.Parse(*orgs.Items[1].Metadata.Name)
	require.True(t, publisher.orgTasksMetadata[org1ID] != nil)
	require.True(t, publisher.orgTasksMetadata[org2ID] != nil)
}

func TestPeriodicTaskPublisher_syncOrganizations_Failure(t *testing.T) {
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}
	mockOrg := publisher.orgService.(*mockOrganizationService)

	orgs := createTestOrganizations(2)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 500}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.getCallCount())
	require.Equal(t, 0, len(publisher.orgTasksMetadata))
}

func TestPeriodicTaskPublisher_syncOrganizations_AddAndRemoveOrgs(t *testing.T) {
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}
	mockOrg := publisher.orgService.(*mockOrganizationService)

	// First sync with 2 organizations
	orgs1 := createTestOrganizations(2)
	mockOrg.organizations = orgs1
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()
	publisher.syncOrganizations(ctx)
	require.Equal(t, 2, len(publisher.orgTasksMetadata))

	// Second sync with different organizations (1 removed, 1 new)
	orgs2 := createTestOrganizations(2)
	orgs2.Items[0] = orgs1.Items[0] // Keep first org
	// orgs2.Items[1] is new, orgs1.Items[1] will be removed

	mockOrg.organizations = orgs2
	publisher.syncOrganizations(ctx)

	require.Equal(t, 2, len(publisher.orgTasksMetadata))
	require.Equal(t, 2, mockOrg.getCallCount())

	// Verify correct organizations are tracked
	org1ID, _ := uuid.Parse(*orgs2.Items[0].Metadata.Name)
	org2ID, _ := uuid.Parse(*orgs2.Items[1].Metadata.Name)
	require.True(t, publisher.orgTasksMetadata[org1ID] != nil)
	require.True(t, publisher.orgTasksMetadata[org2ID] != nil)
}

func TestPeriodicTaskPublisher_publishTasks_Success(t *testing.T) {
	channelManager := &mockChannelManager{}
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		tasksMetadata:    createTestTaskMetadata(),
		channelManager:   channelManager,
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}

	orgID := uuid.New()
	publisher.orgTasksMetadata[orgID] = &OrgTaskMetadata{
		TaskLastPublish: map[PeriodicTaskType]time.Time{
			PeriodicTaskTypeRepositoryTester: time.Unix(0, 0),
			PeriodicTaskTypeResourceSync:     time.Unix(0, 0),
		},
	}

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish 2 tasks (one for each task type) for the organization
	require.Equal(t, 2, channelManager.getPublishedTaskCount())
}

func TestPeriodicTaskPublisher_publishTasks_NoOrganizations(t *testing.T) {
	channelManager := &mockChannelManager{}
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		tasksMetadata:    createTestTaskMetadata(),
		channelManager:   channelManager,
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should not publish any tasks
	require.Equal(t, 0, channelManager.getPublishedTaskCount())
}

func TestPeriodicTaskPublisher_shouldPublishTask(t *testing.T) {
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		tasksMetadata:    createTestTaskMetadata(),
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}

	interval := 1 * time.Minute
	snapshot := time.Now()
	require.True(t, publisher.shouldPublishTask(time.Unix(0, 0), interval))
	require.False(t, publisher.shouldPublishTask(snapshot, interval))
	require.True(t, publisher.shouldPublishTask(snapshot.Add(-interval), interval))
}

func TestPeriodicTaskPublisher_clearOrganizations(t *testing.T) {
	publisher := &PeriodicTaskPublisher{
		log:              createPublisherTestLogger(),
		orgService:       &mockOrganizationService{},
		orgTasksMetadata: make(map[uuid.UUID]*OrgTaskMetadata),
	}

	// Add some test organizations
	org1 := uuid.New()
	org2 := uuid.New()
	publisher.orgTasksMetadata[org1] = &OrgTaskMetadata{
		TaskLastPublish: map[PeriodicTaskType]time.Time{
			PeriodicTaskTypeRepositoryTester: time.Unix(0, 0),
			PeriodicTaskTypeResourceSync:     time.Unix(0, 0),
		},
	}
	publisher.orgTasksMetadata[org2] = &OrgTaskMetadata{
		TaskLastPublish: map[PeriodicTaskType]time.Time{
			PeriodicTaskTypeRepositoryTester: time.Unix(0, 0),
			PeriodicTaskTypeResourceSync:     time.Unix(0, 0),
		},
	}
	require.Equal(t, 2, len(publisher.orgTasksMetadata))

	publisher.clearOrganizations()

	// Organizations should be cleared
	require.Equal(t, 0, len(publisher.orgTasksMetadata))
}
