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
	mu            sync.Mutex
	organizations *api.OrganizationList
	status        api.Status
	callCount     int
}

func (m *mockOrganizationService) ListOrganizations(ctx context.Context) (*api.OrganizationList, api.Status) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount++
	return m.organizations, m.status
}

func (m *mockOrganizationService) getCallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
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
	mu       sync.Mutex
	data     map[string][]byte
	getError error
	setError error
	getCalls []getCall
	setCalls []setCall
}

func (m *mockKVStore) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
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
	m.mu.Lock()
	defer m.mu.Unlock()
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

func (m *mockKVStore) getCallsCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.getCalls)
}

func (m *mockKVStore) getSetCallsCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.setCalls)
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
	mu                sync.Mutex
	publishedMessages [][]byte
	publishError      error
	closeCallCount    int
}

func (m *mockPublisher) Publish(ctx context.Context, payload []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.publishError != nil {
		return m.publishError
	}
	m.publishedMessages = append(m.publishedMessages, payload)
	return nil
}

func (m *mockPublisher) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeCallCount++
}

func (m *mockPublisher) getPublishedMessagesCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.publishedMessages)
}

func (m *mockPublisher) getPublishedMessages() [][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Return a copy to avoid races when the caller reads it
	messages := make([][]byte, len(m.publishedMessages))
	copy(messages, m.publishedMessages)
	return messages
}

func (m *mockPublisher) clearMessages() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishedMessages = nil
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

	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            log,
		KvStore:        kvStore,
		OrgService:     orgService,
		QueuesProvider: provider,
		TasksMetadata:  tasksMetadata,
	}
	publisher, err := NewPeriodicTaskPublisher(publisherConfig)

	require.NoError(t, err)
	require.NotNil(t, publisher)
	require.Equal(t, log, publisher.log)
	require.Equal(t, kvStore, publisher.kvStore)
	require.Equal(t, orgService, publisher.orgService)
	require.Equal(t, tasksMetadata, publisher.tasksMetadata)
	require.Equal(t, mockPub, publisher.publisher)
	require.Equal(t, DefaultTaskTickerInterval, publisher.taskTickerInterval)
	require.Equal(t, DefaultOrgTickerInterval, publisher.orgTickerInterval)
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

	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            log,
		KvStore:        kvStore,
		OrgService:     orgService,
		QueuesProvider: provider,
		TasksMetadata:  tasksMetadata,
	}
	publisher, err := NewPeriodicTaskPublisher(publisherConfig)

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

	_ = publisher.publishTask(ctx, taskType, orgID)

	require.Equal(t, 1, mockPub.getPublishedMessagesCount())

	// Verify the published message content
	var taskRef PeriodicTaskReference
	messages := mockPub.getPublishedMessages()
	err := json.Unmarshal(messages[0], &taskRef)
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

	_ = publisher.publishTask(ctx, taskType, orgID)

	require.Equal(t, 0, mockPub.getPublishedMessagesCount()) // No messages due to error
}

func TestPeriodicTaskPublisher_syncOrganizations_Success(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	orgs := createTestOrganizations(2)
	mockOrg.organizations = orgs
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.getCallCount())
	require.Equal(t, 2, publisher.getOrganizationCount())

	org1ID, _ := uuid.Parse(*orgs.Items[0].Metadata.Name)
	org2ID, _ := uuid.Parse(*orgs.Items[1].Metadata.Name)
	require.True(t, publisher.hasOrganization(org1ID))
	require.True(t, publisher.hasOrganization(org2ID))
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
	require.Equal(t, 2, publisher.getOrganizationCount())

	// Second sync with different organizations (1 removed, 1 new)
	orgs2 := createTestOrganizations(2)
	orgs2.Items[0] = orgs1.Items[0] // Keep first org
	// orgs2.Items[1] is new, orgs1.Items[1] will be removed

	mockOrg.organizations = orgs2
	publisher.syncOrganizations(ctx)

	require.Equal(t, 2, publisher.getOrganizationCount())
	require.Equal(t, 2, mockOrg.getCallCount())

	// Verify correct organizations are tracked
	org1ID, _ := uuid.Parse(*orgs2.Items[0].Metadata.Name)
	org2ID, _ := uuid.Parse(*orgs2.Items[1].Metadata.Name)
	require.True(t, publisher.hasOrganization(org1ID))
	require.True(t, publisher.hasOrganization(org2ID))
}

func TestPeriodicTaskPublisher_syncOrganizations_APIError(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	mockOrg.status = api.Status{Code: 500, Message: "Internal Server Error"}

	ctx := context.Background()
	originalOrgCount := publisher.getOrganizationCount()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.getCallCount())
	require.Equal(t, originalOrgCount, publisher.getOrganizationCount()) // Should not change
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

	require.Equal(t, 1, mockOrg.getCallCount())
	require.Equal(t, 0, publisher.getOrganizationCount()) // Invalid org should be skipped
}

func TestPeriodicTaskPublisher_syncOrganizations_EmptyList(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)

	// Add some initial organizations
	publisher.addOrganization(uuid.New())
	publisher.addOrganization(uuid.New())
	require.Equal(t, 2, publisher.getOrganizationCount())

	// Sync with empty organization list
	mockOrg.organizations = &api.OrganizationList{Items: []api.Organization{}}
	mockOrg.status = api.Status{Code: 200}

	ctx := context.Background()

	publisher.syncOrganizations(ctx)

	require.Equal(t, 1, mockOrg.getCallCount())
	require.Equal(t, 0, publisher.getOrganizationCount()) // All organizations should be removed
}

func TestPeriodicTaskPublisher_publishTasks_Success(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.addOrganization(orgID)

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish 2 tasks (one for each task type) for the organization
	require.Equal(t, 2, mockPub.getPublishedMessagesCount())
	require.Equal(t, 2, mockKV.getCallsCount())
	require.Equal(t, 2, mockKV.getSetCallsCount())

	// Verify task references in published messages
	var taskRefs []PeriodicTaskReference
	messages := mockPub.getPublishedMessages()
	for _, msg := range messages {
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

	require.Equal(t, 0, mockPub.getPublishedMessagesCount())
	require.Equal(t, 0, mockKV.getCallsCount())
	require.Equal(t, 0, mockKV.getSetCallsCount())
}

func TestPeriodicTaskPublisher_publishTasks_WithExistingLastPublish(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.addOrganization(orgID)

	// Setup existing last publish data (within interval, should not publish)
	recentTime := time.Now()
	lastPublish := PeriodicTaskLastPublish{LastPublish: recentTime}
	lastPublishJSON, _ := json.Marshal(lastPublish)

	// Set up KV store with recent last publish for first task
	taskKey1 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastPublish, PeriodicTaskTypeRepositoryTester, orgID.String())
	mockKV.data[taskKey1] = lastPublishJSON

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should only publish 1 task (ResourceSync) since RepositoryTester published recently
	require.Equal(t, 1, mockPub.getPublishedMessagesCount())

	// Verify the published task is ResourceSync
	var taskRef PeriodicTaskReference
	messages := mockPub.getPublishedMessages()
	err := json.Unmarshal(messages[0], &taskRef)
	require.NoError(t, err)
	require.Equal(t, PeriodicTaskTypeResourceSync, taskRef.Type)
	require.Equal(t, orgID, taskRef.OrgID)
}

func TestPeriodicTaskPublisher_publishTasks_WithOldLastPublish(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.addOrganization(orgID)

	// Setup old last publish data (should trigger new publish)
	oldTime := time.Now().Add(-1 * time.Hour)
	lastPublish := PeriodicTaskLastPublish{LastPublish: oldTime}
	lastPublishJSON, _ := json.Marshal(lastPublish)

	// Set up KV store with old last publish for both tasks
	taskKey1 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastPublish, PeriodicTaskTypeRepositoryTester, orgID.String())
	taskKey2 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastPublish, PeriodicTaskTypeResourceSync, orgID.String())
	mockKV.data[taskKey1] = lastPublishJSON
	mockKV.data[taskKey2] = lastPublishJSON

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish both tasks since they're both old enough
	require.Equal(t, 2, mockPub.getPublishedMessagesCount())
}

func TestPeriodicTaskPublisher_publishTasks_KVGetError(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.addOrganization(orgID)

	mockKV.getError = errors.New("kv get failed")

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should skip tasks when there are actual KV errors
	require.Equal(t, 0, mockPub.getPublishedMessagesCount())
	require.Equal(t, 2, mockKV.getCallsCount()) // Should still try to get
}

func TestPeriodicTaskPublisher_publishTasks_KVSetError(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.addOrganization(orgID)

	mockKV.setError = errors.New("kv set failed")

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should publish tasks but fail to update last publish times
	require.Equal(t, 2, mockPub.getPublishedMessagesCount())
	require.Equal(t, 2, mockKV.getSetCallsCount()) // Should still try to set
}

func TestPeriodicTaskPublisher_publishTasks_InvalidLastPublishJSON(t *testing.T) {
	publisher := createTestPublisher()
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	orgID := uuid.New()
	publisher.addOrganization(orgID)

	taskKey1 := fmt.Sprintf("%s%s:%s", RedisKeyPeriodicTaskLastPublish, PeriodicTaskTypeRepositoryTester, orgID.String())
	mockKV.data[taskKey1] = []byte("invalid json")

	ctx := context.Background()

	publisher.publishTasks(ctx)

	// Should continue and publish tasks despite invalid JSON
	require.Equal(t, 2, mockPub.getPublishedMessagesCount())
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

	// Start publisher (non-blocking)
	publisher.Start(ctx)

	// Wait for context timeout to allow processing
	<-ctx.Done()

	// Wait for organization sync to complete
	require.Eventually(t, func() bool {
		return mockOrg.getCallCount() >= 1
	}, 100*time.Millisecond, 1*time.Millisecond, "Should have called syncOrganizations at least once")

	// Wait for tasks to be published
	require.Eventually(t, func() bool {
		return mockPub.getPublishedMessagesCount() > 0
	}, 100*time.Millisecond, 1*time.Millisecond, "Should have published tasks from task ticker")
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

	// Start publisher (non-blocking) with already cancelled context
	publisher.Start(ctx)

	// Wait for initial sync to complete
	require.Eventually(t, func() bool {
		return mockOrg.getCallCount() == 1
	}, 100*time.Millisecond, 1*time.Millisecond, "Should have called initial sync")
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

	// Start publisher (non-blocking)
	publisher.Start(ctx)

	// Wait for context timeout to allow processing
	<-ctx.Done()

	// Wait for task ticker events to occur
	// Since we have 1 org and 2 task types, first tick should publish 2 messages
	// Subsequent ticks won't publish due to recent last publish time
	require.Eventually(t, func() bool {
		return mockPub.getPublishedMessagesCount() >= 2
	}, 100*time.Millisecond, 1*time.Millisecond, "Should have had at least one task ticker event")

	// Wait for organization sync to complete
	require.Eventually(t, func() bool {
		return mockOrg.getCallCount() >= 1
	}, 100*time.Millisecond, 1*time.Millisecond, "Should have called sync at least once (initial sync)")
}

func TestPeriodicTaskPublisher_clearOrganizations(t *testing.T) {
	publisher := createTestPublisher()

	// Add some test organizations
	org1 := uuid.New()
	org2 := uuid.New()
	publisher.addOrganization(org1)
	publisher.addOrganization(org2)

	require.Equal(t, 2, publisher.getOrganizationCount())

	publisher.clearOrganizations()

	// Organizations should be cleared
	require.Equal(t, 0, publisher.getOrganizationCount())
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

	publisherConfig := PeriodicTaskPublisherConfig{
		Log:            log,
		KvStore:        mockKV,
		OrgService:     mockOrg,
		QueuesProvider: provider,
		TasksMetadata:  tasksMetadata,
	}
	publisher, err := NewPeriodicTaskPublisher(publisherConfig)
	require.NoError(t, err)

	// Set shorter intervals for testing
	publisher.taskTickerInterval = 10 * time.Millisecond
	publisher.orgTickerInterval = 20 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Start publisher (non-blocking)
	publisher.Start(ctx)

	// Wait for context timeout to allow processing
	<-ctx.Done()

	// Wait for organizations to be synced
	require.Eventually(t, func() bool {
		return mockOrg.getCallCount() >= 1
	}, 100*time.Millisecond, 1*time.Millisecond, "Organizations should be synced")

	// Wait for tasks to be published
	require.Eventually(t, func() bool {
		return mockPub.getPublishedMessagesCount() > 0
	}, 100*time.Millisecond, 1*time.Millisecond, "Tasks should be published")

	// Wait for KV store interactions to happen
	require.Eventually(t, func() bool {
		return mockKV.getCallsCount() > 0 && mockKV.getSetCallsCount() > 0
	}, 100*time.Millisecond, 1*time.Millisecond, "KV store interactions should happen")

	// Verify task references are valid
	messages := mockPub.getPublishedMessages()
	for _, msg := range messages {
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
	require.Equal(t, 1, publisher.getOrganizationCount())

	// Publish tasks for the organization
	publisher.publishTasks(ctx)
	initialMessageCount := mockPub.getPublishedMessagesCount()
	require.Equal(t, 2, initialMessageCount) // 1 org * 2 task types

	// Change to 3 organizations
	orgs2 := createTestOrganizations(3)
	mockOrg.organizations = orgs2
	publisher.syncOrganizations(ctx)
	require.Equal(t, 3, publisher.getOrganizationCount())

	// Clear previous messages and publish again
	mockPub.clearMessages()
	publisher.publishTasks(ctx)
	newMessageCount := mockPub.getPublishedMessagesCount()
	require.Equal(t, 6, newMessageCount) // 3 orgs * 2 task types

	// Verify all messages have different organization IDs
	orgIDs := make(map[uuid.UUID]bool)
	messages := mockPub.getPublishedMessages()
	for _, msg := range messages {
		var taskRef PeriodicTaskReference
		err := json.Unmarshal(msg, &taskRef)
		require.NoError(t, err)
		orgIDs[taskRef.OrgID] = true
	}
	require.Len(t, orgIDs, 3) // Should have 3 different org IDs
}

func TestPeriodicTaskPublisher_FullWorkflow_ErrorRecovery(t *testing.T) {
	publisher := createTestPublisher()
	mockOrg := publisher.orgService.(*mockOrganizationService)
	mockPub := publisher.publisher.(*mockPublisher)
	mockKV := publisher.kvStore.(*mockKVStore)

	// Setup organization
	orgID := uuid.New()
	publisher.addOrganization(orgID)

	ctx := context.Background()

	// Simulate KV error temporarily
	mockKV.getError = errors.New("temporary error")
	publisher.publishTasks(ctx)
	require.Equal(t, 0, mockPub.getPublishedMessagesCount()) // No tasks published due to KV error

	// Recover from KV error
	mockKV.getError = nil
	publisher.publishTasks(ctx)
	require.Equal(t, 2, mockPub.getPublishedMessagesCount()) // Tasks published after recovery

	// Simulate org service error
	mockOrg.status = api.Status{Code: 500}
	originalOrgCount := publisher.getOrganizationCount()
	publisher.syncOrganizations(ctx)
	require.Equal(t, originalOrgCount, publisher.getOrganizationCount()) // Organizations unchanged

	// Recover from org service error
	mockOrg.status = api.Status{Code: 200}
	mockOrg.organizations = createTestOrganizations(1)
	publisher.syncOrganizations(ctx)
	require.Equal(t, 1, publisher.getOrganizationCount()) // Organizations updated after recovery
}
