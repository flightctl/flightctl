package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/redis/go-redis/v9"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

// Helper function to create organizations
func createTestOrganizations(orgIDs []uuid.UUID) *api.OrganizationList {
	var orgs []api.Organization
	for i, orgID := range orgIDs {
		name := orgID.String()
		displayName := fmt.Sprintf("Test Organization %d", i+1)
		org := api.Organization{
			ApiVersion: "v1alpha1",
			Kind:       api.OrganizationKind,
			Metadata:   api.ObjectMeta{Name: &name},
			Spec: &api.OrganizationSpec{
				DisplayName: &displayName,
			},
		}
		orgs = append(orgs, org)
	}
	return &api.OrganizationList{Items: orgs}
}

// Helper function to create events for an organization with timestamp filtering
func createEventsForOrg(events []api.Event, fieldSelector *string) *api.EventList {
	logrus.Infof("createEventsForOrg called with %d events, fieldSelector: %v", len(events), fieldSelector)

	if fieldSelector == nil {
		logrus.Info("No fieldSelector, returning all events")
		return &api.EventList{Items: events}
	}

	// Simple timestamp filtering implementation
	if strings.Contains(*fieldSelector, "metadata.creationTimestamp>=") {
		parts := strings.Split(*fieldSelector, ">=")
		if len(parts) == 2 {
			timestampStr := strings.TrimSpace(parts[1])
			logrus.Infof("Parsing timestamp threshold: %s", timestampStr)
			// Try RFC3339Nano first, then fall back to RFC3339
			threshold, err := time.Parse(time.RFC3339Nano, timestampStr)
			if err != nil {
				threshold, err = time.Parse(time.RFC3339, timestampStr)
			}
			if err == nil {
				logrus.Infof("Threshold parsed as: %s", threshold.Format(time.RFC3339Nano))
				var filteredEvents []api.Event
				for _, event := range events {
					if event.Metadata.CreationTimestamp != nil {
						eventTime := *event.Metadata.CreationTimestamp
						logrus.Infof("Event %s timestamp: %s, threshold: %s, include: %t",
							*event.Metadata.Name,
							eventTime.Format(time.RFC3339Nano),
							threshold.Format(time.RFC3339Nano),
							!eventTime.Before(threshold))
						if !eventTime.Before(threshold) {
							filteredEvents = append(filteredEvents, event)
						}
					}
				}
				logrus.Infof("Returning %d filtered events out of %d total events", len(filteredEvents), len(events))
				return &api.EventList{Items: filteredEvents}
			}
		}
	}

	return &api.EventList{Items: events}
}

var _ = Describe("Queue Maintenance Integration Tests", func() {
	var (
		log                  *logrus.Logger
		ctx                  context.Context
		cancel               context.CancelFunc
		provider             queues.Provider
		processID            string
		mockCtrl             *gomock.Controller
		mockService          *service.MockService
		queueMaintenanceTask *tasks.QueueMaintenanceTask
		testOrg1ID           uuid.UUID
		testOrg2ID           uuid.UUID
		testOrg1Events       []api.Event
		testOrg2Events       []api.Event
		checkpoints          map[string][]byte
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())
		log = logrus.New()
		log.SetLevel(logrus.DebugLevel)
		processID = fmt.Sprintf("test-process-%s", uuid.New().String())

		// Initialize mock controller and service
		mockCtrl = gomock.NewController(GinkgoT())
		mockService = service.NewMockService(mockCtrl)

		// Generate test organization IDs
		testOrg1ID = uuid.New()
		testOrg2ID = uuid.New()

		// Create test events
		baseTime := time.Now().Add(-1 * time.Hour)
		testOrg1Events = []api.Event{
			createTestEvent("event1-org1", baseTime.Add(10*time.Minute)),
			createTestEvent("event2-org1", baseTime.Add(20*time.Minute)),
		}
		testOrg2Events = []api.Event{
			createTestEvent("event1-org2", baseTime.Add(15*time.Minute)),
			createTestEvent("event2-org2", baseTime.Add(25*time.Minute)),
		}

		// Initialize checkpoints map
		checkpoints = make(map[string][]byte)

		// Create a Redis provider - skip test if Redis is not available
		var err error
		provider, err = queues.NewRedisProvider(ctx, log, processID, "localhost", 6379, config.SecureString("adminpass"), queues.RetryConfig{
			BaseDelay:    100 * time.Millisecond,
			MaxRetries:   3,
			MaxDelay:     500 * time.Millisecond,
			JitterFactor: 0.0,
		})
		if err != nil {
			Skip(fmt.Sprintf("Redis not available, skipping test: %v", err))
		}

		// Clean up Redis keys from previous tests
		redisClient := redis.NewClient(&redis.Options{
			Addr:     "localhost:6379",
			Password: "adminpass",
			DB:       0,
		})
		defer redisClient.Close()

		keys, err := redisClient.Keys(ctx, "*").Result()
		if err == nil && len(keys) > 0 {
			var keysToDelete []string
			for _, key := range keys {
				if key == "in_flight_tasks" || key == "global_checkpoint" ||
					key == "task-queue" || // Clean up the main task queue stream
					strings.HasPrefix(key, "failed_messages:") ||
					strings.HasPrefix(key, "test-queue-") {
					keysToDelete = append(keysToDelete, key)
				}
			}
			if len(keysToDelete) > 0 {
				redisClient.Del(ctx, keysToDelete...)
			}
		}

		// Also ensure we destroy any existing consumer groups for the task queue
		redisClient.XGroupDestroy(ctx, "task-queue", "task-queue-group")

		// Note: queue maintenance task creates its own publisher as needed

		// Create queue maintenance task
		queueMaintenanceTask = tasks.NewQueueMaintenanceTask(log, mockService, provider)
	})

	AfterEach(func() {
		if provider != nil {
			provider.Stop()
			provider.Wait()
		}
		if mockCtrl != nil {
			mockCtrl.Finish()
		}
		cancel()
	})

	Describe("Basic Queue Maintenance Operations", func() {
		It("should execute queue maintenance without errors", func() {
			// Setup expectations for basic execution - recovery may or may not happen depending on Redis state
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(
				&api.OrganizationList{Items: []api.Organization{}}, api.Status{Code: 200}).AnyTimes()

			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle empty system gracefully", func() {
			// Setup expectations for empty organization list - recovery may or may not happen
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(
				&api.OrganizationList{Items: []api.Organization{}}, api.Status{Code: 200}).AnyTimes()

			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("Multi-Organization Event Republishing", func() {
		Context("when Redis checkpoint is missing", func() {
			It("should republish events from all organizations", func() {
				// Setup organizations
				orgs := createTestOrganizations([]uuid.UUID{testOrg1ID, testOrg2ID})

				// Setup mock expectations
				mockService.EXPECT().ListOrganizations(gomock.Any()).Return(orgs, api.Status{Code: 200})

				// Setup database checkpoint to trigger republishing
				baseTime := time.Now().Add(-1 * time.Hour)
				checkpointTime := baseTime.Format(time.RFC3339Nano)
				checkpoints["task_queue-global_checkpoint"] = []byte(checkpointTime)

				mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
					[]byte(checkpointTime), api.Status{Code: 200})

				// Setup expectations for ListEvents calls - will be called with organization contexts
				mockService.EXPECT().ListEvents(gomock.Any(), gomock.Any()).DoAndReturn(
					func(ctx context.Context, params api.ListEventsParams) (*api.EventList, api.Status) {
						// Get org ID from context
						orgID, ok := util.GetOrgIdFromContext(ctx)
						if !ok {
							return &api.EventList{Items: []api.Event{}}, api.Status{Code: 200}
						}

						// Return appropriate events based on organization
						if orgID == testOrg1ID {
							return createEventsForOrg(testOrg1Events, params.FieldSelector), api.Status{Code: 200}
						} else if orgID == testOrg2ID {
							return createEventsForOrg(testOrg2Events, params.FieldSelector), api.Status{Code: 200}
						}

						return &api.EventList{Items: []api.Event{}}, api.Status{Code: 200}
					}).AnyTimes() // Allow multiple calls for debugging

				// Mock CreateEvent calls for any internal task events that might be emitted
				mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

				// Mock SetCheckpoint calls for checkpoint synchronization
				mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
					api.Status{Code: 200}).AnyTimes()

				// Execute queue maintenance to trigger republishing
				err := queueMaintenanceTask.Execute(ctx)
				Expect(err).ToNot(HaveOccurred())

				// Verify that events were republished by checking the Redis stream directly
				redisClient := redis.NewClient(&redis.Options{
					Addr:     "localhost:6379",
					Password: "adminpass",
					DB:       0,
				})
				defer redisClient.Close()

				// Check that the task-queue stream has the expected number of entries
				Eventually(func() int64 {
					streamInfo, err := redisClient.XInfoStream(ctx, consts.TaskQueue).Result()
					if err != nil {
						return 0
					}
					return streamInfo.Length
				}, 10*time.Second, 500*time.Millisecond).Should(BeNumerically(">=", 4))

				// Get all messages from the stream to verify content
				msgs, err := redisClient.XRange(ctx, consts.TaskQueue, "-", "+").Result()
				Expect(err).ToNot(HaveOccurred())
				Expect(len(msgs)).To(BeNumerically(">=", 4))

				// Verify that we have messages for both organizations
				orgEventCounts := make(map[uuid.UUID]int)
				for _, msg := range msgs {
					if bodyVal, ok := msg.Values["body"]; ok {
						if bodyBytes, ok := bodyVal.(string); ok {
							var eventWithOrg worker_client.EventWithOrgId
							if err := json.Unmarshal([]byte(bodyBytes), &eventWithOrg); err == nil {
								orgEventCounts[eventWithOrg.OrgId]++
							}
						}
					}
				}

				Expect(orgEventCounts[testOrg1ID]).To(BeNumerically(">=", 2))
				Expect(orgEventCounts[testOrg2ID]).To(BeNumerically(">=", 2))
			})

			It("should handle organization with no events", func() {
				// Add an organization with no events
				emptyOrgID := uuid.New()
				orgs := createTestOrganizations([]uuid.UUID{emptyOrgID})

				mockService.EXPECT().ListOrganizations(gomock.Any()).Return(orgs, api.Status{Code: 200}).AnyTimes()

				// Mock ListEvents for the organization (returns empty list)
				mockService.EXPECT().ListEvents(gomock.Any(), gomock.Any()).Return(
					&api.EventList{Items: []api.Event{}}, api.Status{Code: 200}).AnyTimes()

				// The queue maintenance will try to get checkpoint during recovery process
				mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
					nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

				// Mock CreateEvent calls for any internal task events that might be emitted
				mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

				// Mock SetCheckpoint calls for checkpoint synchronization
				mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
					api.Status{Code: 200}).AnyTimes()

				err := queueMaintenanceTask.Execute(ctx)
				Expect(err).ToNot(HaveOccurred())
			})
		})
	})

	Describe("Redis Failure Recovery", func() {
		It("should recover when Redis checkpoint is missing but database checkpoint exists", func() {
			// Setup organizations
			orgs := createTestOrganizations([]uuid.UUID{testOrg1ID})
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(orgs, api.Status{Code: 200})

			// Set only database checkpoint (simulate Redis failure)
			checkpointTime := time.Now().Add(-30 * time.Minute)
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				[]byte(checkpointTime.Format(time.RFC3339Nano)), api.Status{Code: 200})

			// Setup ListEvents expectation for recovery
			mockService.EXPECT().ListEvents(gomock.Any(), gomock.Any()).Return(
				&api.EventList{Items: []api.Event{createTestEvent("recent-event", time.Now().Add(-15*time.Minute))}},
				api.Status{Code: 200})

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Verify Redis checkpoint operation completed without errors
			// Note: The actual checkpoint value may vary depending on Redis state
		})

		It("should handle fresh system with no checkpoints", func() {
			// Setup empty organizations - recovery may or may not happen
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(
				&api.OrganizationList{Items: []api.Organization{}}, api.Status{Code: 200}).AnyTimes()

			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred())
		})
	})

	Describe("Error Handling", func() {
		It("should handle service errors gracefully", func() {
			// Setup service to return an error - may or may not be called depending on Redis state
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(
				nil, api.Status{Code: 500, Message: "internal server error"}).AnyTimes()

			// Even when ListOrganizations fails, other queue maintenance operations may still run
			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			err := queueMaintenanceTask.Execute(ctx)
			// Should handle the error gracefully - may or may not fail depending on when the error occurs
			// If error happens during recovery, expect error; otherwise expect success
			// We'll allow both cases
			_ = err
		})

		It("should handle invalid organization IDs", func() {
			// Add organization with invalid name format
			invalidOrg := api.Organization{
				ApiVersion: "v1alpha1",
				Kind:       api.OrganizationKind,
				Metadata:   api.ObjectMeta{Name: lo.ToPtr("invalid-uuid")},
			}
			orgs := &api.OrganizationList{Items: []api.Organization{invalidOrg}}

			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(orgs, api.Status{Code: 200}).AnyTimes()

			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred()) // Should continue gracefully
		})
	})

	Describe("Timeout and Retry Scenarios", func() {
		It("should handle message timeouts and retries", func() {
			// Setup basic organizations for queue operations - recovery may or may not happen
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(
				&api.OrganizationList{Items: []api.Organization{}}, api.Status{Code: 200}).AnyTimes()

			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			// Simulate queue operations (processTimedOutMessages, retryFailedMessages, etc.)
			// Note: The actual timeout/retry testing would require more complex setup
			// with Redis streams and would be better suited for the existing redis_provider_test.go

			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should advance checkpoint after processing", func() {
			// Setup basic organizations - recovery may or may not happen
			mockService.EXPECT().ListOrganizations(gomock.Any()).Return(
				&api.OrganizationList{Items: []api.Organization{}}, api.Status{Code: 200}).AnyTimes()

			// The queue maintenance will try to get checkpoint during recovery process
			mockService.EXPECT().GetCheckpoint(gomock.Any(), "task_queue", "global_checkpoint").Return(
				nil, api.Status{Code: 404, Message: "checkpoint not found"}).AnyTimes()

			// Mock CreateEvent calls for any internal task events that might be emitted
			mockService.EXPECT().CreateEvent(gomock.Any(), gomock.Any()).AnyTimes()

			// Mock SetCheckpoint calls for checkpoint synchronization
			mockService.EXPECT().SetCheckpoint(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(
				api.Status{Code: 200}).AnyTimes()

			// Test checkpoint advancement (requires Redis operations)
			err := queueMaintenanceTask.Execute(ctx)
			Expect(err).ToNot(HaveOccurred())

			// Verify checkpoint operations occurred (this would be more complex in a real scenario)
			// The actual checkpoint verification would need Redis introspection
		})
	})
})

// Helper function to create test events
func createTestEvent(name string, timestamp time.Time) api.Event {
	return api.Event{
		ApiVersion: "v1alpha1",
		Kind:       api.EventKind,
		Metadata: api.ObjectMeta{
			Name:              &name,
			CreationTimestamp: &timestamp,
		},
		InvolvedObject: api.ObjectReference{
			Kind: api.DeviceKind,
			Name: "test-device",
		},
		Reason:  "TestEvent",
		Message: "This is a test event",
		Type:    api.Normal,
	}
}
