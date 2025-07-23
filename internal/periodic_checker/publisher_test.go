package periodic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/queues"
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

type getCall struct {
	ctx context.Context
	key string
}

type setCall struct {
	ctx   context.Context
	key   string
	value []byte
}

type mockKVStore struct {
	data     map[string][]byte
	getError error
	setError error
	getCalls []getCall
	setCalls []setCall
}

func (m *mockKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	m.getCalls = append(m.getCalls, getCall{ctx: ctx, key: key})
	if m.getError != nil {
		return nil, m.getError
	}
	if data, exists := m.data[key]; exists {
		return data, nil
	}
	// When key doesn't exist, return nil bytes with no error (not an error)
	return nil, nil
}

func (m *mockKVStore) Set(ctx context.Context, key string, value []byte) error {
	m.setCalls = append(m.setCalls, setCall{ctx: ctx, key: key, value: value})
	if m.setError != nil {
		return m.setError
	}
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[key] = value
	return nil
}

func (m *mockKVStore) Close() {}
func (m *mockKVStore) SetNX(ctx context.Context, key string, value []byte) (bool, error) {
	return false, nil
}
func (m *mockKVStore) GetOrSetNX(ctx context.Context, key string, value []byte) ([]byte, error) {
	return nil, nil
}
func (m *mockKVStore) DeleteKeysForTemplateVersion(ctx context.Context, key string) error { return nil }
func (m *mockKVStore) DeleteAllKeys(ctx context.Context) error                            { return nil }
func (m *mockKVStore) PrintAllKeys(ctx context.Context)                                   {}

type mockPublisher struct {
	publishedMessages [][]byte
	publishError      error
	closeCallCount    int
}

func (m *mockPublisher) Publish(ctx context.Context, payload []byte) error {
	if m.publishError != nil {
		return m.publishError
	}
	m.publishedMessages = append(m.publishedMessages, payload)
	return nil
}

func (m *mockPublisher) Close() {
	m.closeCallCount++
}

type mockPublisherProvider struct {
	publisher         *mockPublisher
	newPublisherError error
}

func (m *mockPublisherProvider) NewPublisher(queueName string) (queues.Publisher, error) {
	if m.newPublisherError != nil {
		return nil, m.newPublisherError
	}
	return m.publisher, nil
}

func (m *mockPublisherProvider) NewConsumer(queueName string) (queues.Consumer, error) {
	return nil, errors.New("not implemented")
}

func (m *mockPublisherProvider) Stop() {}
func (m *mockPublisherProvider) Wait() {}

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

func createTestTaskMetadata() []PeriodicTaskMetadata {
	return []PeriodicTaskMetadata{
		{TaskType: PeriodicTaskTypeRepositoryTester, Interval: 1 * time.Minute},
		{TaskType: PeriodicTaskTypeResourceSync, Interval: 2 * time.Minute},
	}
}

func createTestPublisher() *PeriodicTaskPublisher {
	return &PeriodicTaskPublisher{
		publisher:          &mockPublisher{},
		log:                createPublisherTestLogger(),
		tasksMetadata:      createTestTaskMetadata(),
		orgService:         &mockOrganizationService{},
		kvStore:            &mockKVStore{data: make(map[string][]byte)},
		organizations:      make(map[uuid.UUID]bool),
		taskTickerInterval: 10 * time.Millisecond,
		orgTickerInterval:  20 * time.Millisecond,
	}
}

func TestNewPeriodicTaskPublisher_Success(t *testing.T) {
	log := createPublisherTestLogger()
	kvStore := &mockKVStore{data: make(map[string][]byte)}
	orgService := &mockOrganizationService{}
	tasksMetadata := createTestTaskMetadata()
	mockPub := &mockPublisher{}
	provider := &mockPublisherProvider{
		publisher: mockPub,
	}

	publisher, err := NewPeriodicTaskPublisher(log, kvStore, orgService, provider, tasksMetadata)

	require.NoError(t, err)
	require.NotNil(t, publisher)
	require.Equal(t, log, publisher.log)
	require.Equal(t, kvStore, publisher.kvStore)
	require.Equal(t, orgService, publisher.orgService)
	require.Equal(t, tasksMetadata, publisher.tasksMetadata)
	require.Equal(t, mockPub, publisher.publisher)
	require.Equal(t, 5*time.Second, publisher.taskTickerInterval)
	require.Equal(t, 5*time.Minute, publisher.orgTickerInterval)
}

func TestNewPeriodicTaskPublisher_ProviderError(t *testing.T) {
	log := createPublisherTestLogger()
	kvStore := &mockKVStore{data: make(map[string][]byte)}
	orgService := &mockOrganizationService{}
	tasksMetadata := createTestTaskMetadata()
	expectedError := errors.New("provider failed")
	provider := &mockPublisherProvider{
		newPublisherError: expectedError,
	}

	publisher, err := NewPeriodicTaskPublisher(log, kvStore, orgService, provider, tasksMetadata)

	require.Error(t, err)
	require.Nil(t, publisher)
	require.Equal(t, expectedError, err)
}

func TestPeriodicTaskPublisher_publishTask_Success(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	ctx := context.Background()
	orgID := uuid.New()
	taskType := PeriodicTaskTypeRepositoryTester

	publisher.publishTask(ctx, taskType, orgID)

	require.Len(t, mockPub.publishedMessages, 1)

	// Verify the published message content
	var taskRef PeriodicTaskReference
	err := json.Unmarshal(mockPub.publishedMessages[0], &taskRef)
	require.NoError(t, err)
	require.Equal(t, taskType, taskRef.Type)
	require.Equal(t, orgID, taskRef.OrgID)
}

func TestPeriodicTaskPublisher_publishTask_PublishError(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	expectedError := errors.New("publish failed")
	mockPub.publishError = expectedError

	ctx := context.Background()
	orgID := uuid.New()
	taskType := PeriodicTaskTypeResourceSync

	publisher.publishTask(ctx, taskType, orgID)

	require.Len(t, mockPub.publishedMessages, 0) // No messages due to error
}

func TestPeriodicTaskPublisher_syncOrganizations_Success(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	orgs := createTestOrganizations(2)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.callCount)
	require.Len(t, publisher.organizations, 2)

	org1ID, _ := uuid.Parse(*orgs.Items[0].Metadata.Name)
	org2ID, _ := uuid.Parse(*orgs.Items[1].Metadata.Name)
	require.True(t, publisher.organizations[org1ID])
	require.True(t, publisher.organizations[org2ID])
}

func TestPeriodicTaskPublisher_syncOrganizations_AddAndRemoveOrgs(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	// First sync with 2 organizations
	orgs1 := createTestOrganizations(2)
	mockOrg.organizations = orgs1
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()
	publisher.syncOrganizations(ctx)
	require.Len(t, publisher.organizations, 2)

	// Second sync with different organizations (1 removed, 1 new)
	orgs2 := createTestOrganizations(2)
	orgs2.Items[0] = orgs1.Items[0] // Keep first org
	// orgs2.Items[1] is new, orgs1.Items[1] will be removed

	mockOrg.organizations = orgs2
	publisher.syncOrganizations(ctx)

	require.Len(t, publisher.organizations, 2)
	require.Equal(t, 2, mockOrg.callCount)

	// Verify correct organizations are tracked
	org1ID, _ := uuid.Parse(*orgs2.Items[0].Metadata.Name)
	org2ID, _ := uuid.Parse(*orgs2.Items[1].Metadata.Name)
	require.True(t, publisher.organizations[org1ID])
	require.True(t, publisher.organizations[org2ID])
}

func TestPeriodicTaskPublisher_syncOrganizations_APIError(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	mockOrg.status = api.Status{Code: 500, Message: "Internal Server Error"}

	ctx := context.Background()
	originalOrgCount := len(publisher.organizations)

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.callCount)
	require.Len(t, publisher.organizations, originalOrgCount) // Should not change
}

func TestPeriodicTaskPublisher_syncOrganizations_InvalidOrgID(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	// Create organization with invalid UUID
	invalidOrgName := "invalid-uuid"
	orgs := &api.OrganizationList{
		Items: []api.Organization{
			{
				Metadata: api.ObjectMeta{
					Name: &invalidOrgName,
				},
			},
		},
	}

	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.callCount)
	require.Len(t, publisher.organizations, 0) // Invalid org should be skipped
}

func TestPeriodicTaskPublisher_syncOrganizations_EmptyList(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	// Add some initial organizations
	publisher.organizations[uuid.New()] = true
	publisher.organizations[uuid.New()] = true
	require.Len(t, publisher.organizations, 2)

	// Sync with empty organization list
	mockOrg.organizations = &api.OrganizationList{Items: []api.Organization{}}
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.callCount)
	require.Len(t, publisher.organizations, 0) // All organizations should be removed
}

func TestPeriodicTaskPublisher_publishTasks_Success(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.organizations[orgID] = true

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish 2 tasks (one for each task type) for the organization
	require.Len(t, mockPub.publishedMessages, 2)

	// Verify KV store interactions (Get calls for checking last run)
	require.Len(t, mockKV.getCalls, 2)

	// Verify Set calls for updating last run
	require.Len(t, mockKV.setCalls, 2)

	// Verify task references in published messages
	var taskRefs []PeriodicTaskReference
	for _, msg := range mockPub.publishedMessages {
		var taskRef PeriodicTaskReference
		err := json.Unmarshal(msg, &taskRef)
		require.NoError(t, err)
		taskRefs = append(taskRefs, taskRef)
		require.Equal(t, orgID, taskRef.OrgID)
	}

	// Should have both task types
	taskTypes := []PeriodicTaskType{taskRefs[0].Type, taskRefs[1].Type}
	require.Contains(t, taskTypes, PeriodicTaskTypeRepositoryTester)
	require.Contains(t, taskTypes, PeriodicTaskTypeResourceSync)
}

func TestPeriodicTaskPublisher_publishTasks_NoOrganizations(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	ctx := context.Background()

	publisher.publishTasks(ctx)

	require.Len(t, mockPub.publishedMessages, 0)
	require.Len(t, mockKV.getCalls, 0)
	require.Len(t, mockKV.setCalls, 0)
}

func TestPeriodicTaskPublisher_publishTasks_WithExistingLastRun(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.organizations[orgID] = true

	// Setup existing last run data (recent run, should not publish)
	recentTime := time.Now()
	lastRun := PeriodicTaskLastRun{LastRun: recentTime}
	lastRunJSON, _ := json.Marshal(lastRun)

	// Set up KV store with recent last run for first task
	taskKey1 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastRun, PeriodicTaskTypeRepositoryTester, orgID.String())
	mockKV.data[taskKey1] = lastRunJSON

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should only publish 1 task (ResourceSync) since RepositoryTester ran recently
	require.Len(t, mockPub.publishedMessages, 1)

	// Verify the published task is ResourceSync
	var taskRef PeriodicTaskReference
	err := json.Unmarshal(mockPub.publishedMessages[0], &taskRef)
	require.NoError(t, err)
	require.Equal(t, PeriodicTaskTypeResourceSync, taskRef.Type)
	require.Equal(t, orgID, taskRef.OrgID)
}

func TestPeriodicTaskPublisher_publishTasks_WithOldLastRun(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.organizations[orgID] = true

	// Setup old last run data (should trigger new publish)
	oldTime := time.Now().Add(-5 * time.Minute) // Older than all intervals
	lastRun := PeriodicTaskLastRun{LastRun: oldTime}
	lastRunJSON, _ := json.Marshal(lastRun)

	// Set up KV store with old last run for both tasks
	taskKey1 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastRun, PeriodicTaskTypeRepositoryTester, orgID.String())
	taskKey2 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastRun, PeriodicTaskTypeResourceSync, orgID.String())
	mockKV.data[taskKey1] = lastRunJSON
	mockKV.data[taskKey2] = lastRunJSON

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish both tasks since they're both old enough
	require.Len(t, mockPub.publishedMessages, 2)
}

func TestPeriodicTaskPublisher_publishTasks_KVGetError(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.organizations[orgID] = true

	mockKV.getError = errors.New("kv get failed")

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should skip tasks when there are actual KV errors
	require.Len(t, mockPub.publishedMessages, 0)
	require.Len(t, mockKV.getCalls, 2) // Should still try to get
}

func TestPeriodicTaskPublisher_publishTasks_KVSetError(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.organizations[orgID] = true

	mockKV.setError = errors.New("kv set failed")

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish tasks but fail to update last run times
	require.Len(t, mockPub.publishedMessages, 2)
	require.Len(t, mockKV.setCalls, 2) // Should still try to set
}

func TestPeriodicTaskPublisher_publishTasks_InvalidLastRunJSON(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.organizations[orgID] = true

	taskKey1 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastRun, PeriodicTaskTypeRepositoryTester, orgID.String())
	mockKV.data[taskKey1] = []byte("invalid json")

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should continue and publish tasks despite invalid JSON
	require.Len(t, mockPub.publishedMessages, 2)
}

func TestPeriodicTaskPublisher_Start_TickerBehavior(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)
	mockPub := publisher.publisher.(*mockPublisher)

	orgs := createTestOrganizations(1)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Run Start in a goroutine
	done := make(chan struct{})
	go func() {
		publisher.Start(ctx)
		close(done)
	}()

	// Wait for context cancellation
	<-done

	// Should have called syncOrganizations at least once
	require.GreaterOrEqual(t, mockOrg.callCount, 1)

	// Should have published tasks from task ticker
	require.Greater(t, len(mockPub.publishedMessages), 0)

	// Verify organizations are cleared after stop
	require.Len(t, publisher.organizations, 0)
}

func TestPeriodicTaskPublisher_Start_ContextCancellation(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	orgs := createTestOrganizations(1)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	ctx, cancel := context.WithCancel(context.Background())

	// Cancel immediately to test quick exit
	cancel()

	// Should not block
	done := make(chan struct{})
	go func() {
		publisher.Start(ctx)
		close(done)
	}()

	select {
	case <-done:
		// Should exit quickly
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Start() did not exit quickly after context cancellation")
	}

	// Should have called initial sync
	require.Equal(t, 1, mockOrg.callCount)

	// Organizations should be cleared
	require.Len(t, publisher.organizations, 0)
}

func TestPeriodicTaskPublisher_Start_MultipleTickerEvents(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)
	mockPub := publisher.publisher.(*mockPublisher)

	orgs := createTestOrganizations(1)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	// Set intervals to ensure multiple ticks
	publisher.taskTickerInterval = 5 * time.Millisecond
	publisher.orgTickerInterval = 100 * time.Millisecond // Longer so we can count task ticks

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	// Run Start
	publisher.Start(ctx)

	// Should have had at least one task ticker event
	// Since we have 1 org and 2 task types, first tick should publish 2 messages
	// Subsequent ticks won't publish due to recent last run time
	require.GreaterOrEqual(t, len(mockPub.publishedMessages), 2)

	// Should have called sync at least once (initial sync)
	require.GreaterOrEqual(t, mockOrg.callCount, 1)
}

func TestPeriodicTaskPublisher_stopAll(t *testing.T) {
	publisher := createTestPublisher()

	// Add some test organizations
	org1 := uuid.New()
	org2 := uuid.New()
	publisher.organizations[org1] = true
	publisher.organizations[org2] = true

	require.Len(t, publisher.organizations, 2)

	publisher.stopAll()

	// Organizations should be cleared
	require.Len(t, publisher.organizations, 0)
}

func TestPeriodicTaskPublisher_FullWorkflow(t *testing.T) {
	log := createPublisherTestLogger()
	mockKV := &mockKVStore{data: make(map[string][]byte)}
	mockOrg := &mockOrganizationService{
		organizations: createTestOrganizations(2),
		status:        api.Status{Code: 200},
	}
	tasksMetadata := createTestTaskMetadata()
	mockPub := &mockPublisher{}
	provider := &mockPublisherProvider{publisher: mockPub}

	publisher, err := NewPeriodicTaskPublisher(log, mockKV, mockOrg, provider, tasksMetadata)
	require.NoError(t, err)

	// Set shorter intervals for testing
	publisher.taskTickerInterval = 10 * time.Millisecond
	publisher.orgTickerInterval = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Use WaitGroup to properly wait for publisher to finish
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		publisher.Start(ctx)
	}()

	// Wait for the publisher to complete
	wg.Wait()

	// Verify organizations were synced
	require.GreaterOrEqual(t, mockOrg.callCount, 1)
	// Organizations should be cleared after context cancellation due to stopAll()
	require.Len(t, publisher.organizations, 0)

	// Verify tasks were published
	require.Greater(t, len(mockPub.publishedMessages), 0)

	// Verify KV store interactions happened
	require.Greater(t, len(mockKV.getCalls), 0)
	require.Greater(t, len(mockKV.setCalls), 0)

	// Verify task references are valid
	for _, msg := range mockPub.publishedMessages {
		var taskRef PeriodicTaskReference
		err := json.Unmarshal(msg, &taskRef)
		require.NoError(t, err)
		require.NotEmpty(t, taskRef.Type)
		require.NotEqual(t, uuid.Nil, taskRef.OrgID)
	}
}

func TestPeriodicTaskPublisher_FullWorkflow_OrganizationChanges(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)
	mockPub := publisher.publisher.(*mockPublisher)

	// Start with 1 organization
	orgs1 := createTestOrganizations(1)
	mockOrg.organizations = orgs1
	mockOrg.status = api.Status{Code: 200}

	// First sync
	ctx := context.Background()
	publisher.syncOrganizations(ctx)
	require.Len(t, publisher.organizations, 1)

	// Publish tasks for the organization
	publisher.publishTasks(ctx)
	initialMessageCount := len(mockPub.publishedMessages)
	require.Equal(t, 2, initialMessageCount) // 1 org * 2 task types

	// Change to 3 organizations
	orgs2 := createTestOrganizations(3)
	mockOrg.organizations = orgs2
	publisher.syncOrganizations(ctx)
	require.Len(t, publisher.organizations, 3)

	// Clear previous messages and publish again
	mockPub.publishedMessages = nil
	publisher.publishTasks(ctx)
	newMessageCount := len(mockPub.publishedMessages)
	require.Equal(t, 6, newMessageCount) // 3 orgs * 2 task types

	// Verify all messages have different organization IDs
	orgIDs := make(map[uuid.UUID]bool)
	for _, msg := range mockPub.publishedMessages {
		var taskRef PeriodicTaskReference
		err := json.Unmarshal(msg, &taskRef)
		require.NoError(t, err)
		orgIDs[taskRef.OrgID] = true
	}
	require.Len(t, orgIDs, 3) // Should have 3 different org IDs
}

func TestPeriodicTaskPublisher_FullWorkflow_TimingBehavior(t *testing.T) {
	publisher := createTestPublisher()
	mockKV := publisher.kvStore.(*mockKVStore)
	mockPub := publisher.publisher.(*mockPublisher)

	// Setup organization
	orgID := uuid.New()
	publisher.organizations[orgID] = true

	ctx := context.Background()

	// First publish - should publish all tasks
	publisher.publishTasks(ctx)
	require.Len(t, mockPub.publishedMessages, 2)

	// Immediately try to publish again - should not publish due to recent last run
	mockPub.publishedMessages = nil
	publisher.publishTasks(ctx)
	require.Len(t, mockPub.publishedMessages, 0)

	// Manually update one task's last run to be old
	oldTime := time.Now().Add(-5 * time.Minute)
	lastRun := PeriodicTaskLastRun{LastRun: oldTime}
	lastRunJSON, _ := json.Marshal(lastRun)
	taskKey := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastRun, PeriodicTaskTypeRepositoryTester, orgID.String())
	mockKV.data[taskKey] = lastRunJSON

	// Now should publish only the one with old last run
	mockPub.publishedMessages = nil
	publisher.publishTasks(ctx)
	require.Len(t, mockPub.publishedMessages, 1)

	// Verify it's the correct task type
	var taskRef PeriodicTaskReference
	err := json.Unmarshal(mockPub.publishedMessages[0], &taskRef)
	require.NoError(t, err)
	require.Equal(t, PeriodicTaskTypeRepositoryTester, taskRef.Type)
}

func TestPeriodicTaskPublisher_FullWorkflow_ErrorRecovery(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	// Setup organization
	orgID := uuid.New()
	publisher.organizations[orgID] = true

	ctx := context.Background()

	// Simulate KV error temporarily
	mockKV.getError = errors.New("temporary error")
	publisher.publishTasks(ctx)
	require.Len(t, mockPub.publishedMessages, 0) // No tasks published due to KV error

	// Recover from KV error
	mockKV.getError = nil
	publisher.publishTasks(ctx)
	require.Len(t, mockPub.publishedMessages, 2) // Tasks published after recovery

	// Simulate org service error
	mockOrg.status = api.Status{Code: 500}
	originalOrgCount := len(publisher.organizations)
	publisher.syncOrganizations(ctx)
	require.Len(t, publisher.organizations, originalOrgCount) // Organizations unchanged

	// Recover from org service error
	mockOrg.status = api.Status{Code: 200}
	mockOrg.organizations = createTestOrganizations(1)
	publisher.syncOrganizations(ctx)
	require.Len(t, publisher.organizations, 1) // Organizations updated after recovery
}
