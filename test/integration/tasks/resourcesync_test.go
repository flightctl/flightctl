package tasks_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/tasks_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var _ = Describe("ResourceSync Task Integration Tests", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		storeInst       store.Store
		serviceHandler  service.Service
		cfg             *config.Config
		dbName          string
		callbackManager tasks_client.CallbackManager
		ctrl            *gomock.Controller
		mockPublisher   *queues.MockPublisher
		resourceSync    *tasks.ResourceSync
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ctrl = gomock.NewController(GinkgoT())
		mockPublisher = queues.NewMockPublisher(ctrl)
		callbackManager = tasks_client.NewCallbackManager(mockPublisher, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")
		resourceSync = tasks.NewResourceSync(callbackManager, serviceHandler, log)

		// Set up mock expectations for the publisher
		mockPublisher.EXPECT().Publish(gomock.Any(), gomock.Any()).AnyTimes()
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
		ctrl.Finish()
	})

	// Helper function to get events for a specific ResourceSync
	getEventsForResourceSync := func(resourceSyncName string) []api.Event {
		listParams := store.ListParams{
			Limit:       100,
			SortColumns: []store.SortColumn{store.SortByCreatedAt, store.SortByName},
			SortOrder:   lo.ToPtr(store.SortDesc),
		}
		eventList, err := storeInst.Event().List(ctx, orgId, listParams)
		Expect(err).ToNot(HaveOccurred())

		var matchingEvents []api.Event
		for _, event := range eventList.Items {
			if event.InvolvedObject.Kind == api.ResourceSyncKind && event.InvolvedObject.Name == resourceSyncName {
				matchingEvents = append(matchingEvents, event)
			}
		}
		return matchingEvents
	}

	// Helper function to check for specific event reason
	findEventByReason := func(events []api.Event, reason api.EventReason) *api.Event {
		for _, event := range events {
			if event.Reason == reason {
				return &event
			}
		}
		return nil
	}

	// Helper function to create a test repository
	createTestRepository := func(name string, url string) *api.Repository {
		spec := api.RepositorySpec{}
		err := spec.FromGenericRepoSpec(api.GenericRepoSpec{
			Url:  url,
			Type: "git",
		})
		Expect(err).ToNot(HaveOccurred())

		repo := &api.Repository{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: spec,
		}

		_, status := serviceHandler.CreateRepository(ctx, *repo)
		Expect(status.Code).To(Equal(int32(201)))
		return repo
	}

	// Helper function to create a test ResourceSync
	createTestResourceSync := func(name string, repoName string, path string) *api.ResourceSync {
		resourceSync := &api.ResourceSync{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: api.ResourceSyncSpec{
				Repository:     repoName,
				Path:           path,
				TargetRevision: "main",
			},
		}

		_, status := serviceHandler.CreateResourceSync(ctx, *resourceSync)
		Expect(status.Code).To(Equal(int32(201)))
		return resourceSync
	}

	Context("ResourceSync Helper Methods", func() {
		It("should validate repository access and set conditions", func() {
			// Create a repository
			createTestRepository("test-repo", "https://github.com/test/repo")

			// Create a ResourceSync
			resourceSyncName := "test-resourcesync"
			rs := createTestResourceSync(resourceSyncName, "test-repo", "/examples")

			// Test the helper method
			repo, err := resourceSync.GetRepositoryAndValidateAccess(ctx, rs)
			Expect(err).ToNot(HaveOccurred())
			Expect(repo).ToNot(BeNil())

			// Verify conditions were set
			Expect(rs.Status.Conditions).ToNot(BeNil())
			Expect(len(rs.Status.Conditions)).To(BeNumerically(">", 0))

			// Check for accessible condition
			accessibleCondition := api.FindStatusCondition(rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible)
			Expect(accessibleCondition).ToNot(BeNil())
			Expect(accessibleCondition.Status).To(Equal(api.ConditionStatusTrue))
		})

		It("should handle missing repository and set error condition", func() {
			// Create a ResourceSync that references a non-existent repository
			resourceSyncName := "invalid-repo-resourcesync"
			rs := createTestResourceSync(resourceSyncName, "non-existent-repo", "/examples")

			// Test the helper method
			repo, err := resourceSync.GetRepositoryAndValidateAccess(ctx, rs)
			Expect(err).To(HaveOccurred())
			Expect(repo).To(BeNil())

			// Verify conditions were set
			Expect(rs.Status.Conditions).ToNot(BeNil())
			Expect(len(rs.Status.Conditions)).To(BeNumerically(">", 0))

			// Check for inaccessible condition
			accessibleCondition := api.FindStatusCondition(rs.Status.Conditions, api.ConditionTypeResourceSyncAccessible)
			Expect(accessibleCondition).ToNot(BeNil())
			Expect(accessibleCondition.Status).To(Equal(api.ConditionStatusFalse))
		})

		It("should parse fleets from resources", func() {
			// Create mock resources
			resources := []tasks.GenericResourceMap{
				{
					"kind": api.FleetKind,
					"metadata": map[string]interface{}{
						"name": "test-fleet",
					},
					"spec": map[string]interface{}{
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"environment": "test",
								},
							},
							"spec": map[string]interface{}{
								"os": map[string]interface{}{
									"image": "quay.io/test/os:latest",
								},
							},
						},
					},
				},
			}

			// Test the helper method
			fleets, err := resourceSync.ParseFleetsFromResources(resources, "test-resourcesync")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets).ToNot(BeNil())
			Expect(len(fleets)).To(Equal(1))

			// Verify fleet properties
			fleet := fleets[0]
			Expect(*fleet.Metadata.Name).To(Equal("test-fleet"))
			Expect((*fleet.Spec.Template.Metadata.Labels)["environment"]).To(Equal("test"))
		})

		It("should handle invalid resource format", func() {
			// Create invalid resources
			resources := []tasks.GenericResourceMap{
				{
					"kind": "InvalidKind",
					"metadata": map[string]interface{}{
						"name": "test-fleet",
					},
				},
			}

			// Test the helper method
			fleets, err := resourceSync.ParseFleetsFromResources(resources, "test-resourcesync")
			Expect(err).To(HaveOccurred())
			Expect(fleets).To(BeNil())
		})

		It("should sync fleets successfully", func() {
			// Create a repository and ResourceSync
			createTestRepository("sync-test-repo", "https://github.com/test/repo")
			rs := createTestResourceSync("sync-test-resourcesync", "sync-test-repo", "/examples")

			// Create test fleets
			fleets := []*api.Fleet{
				{
					Metadata: api.ObjectMeta{
						Name: lo.ToPtr("test-fleet-1"),
					},
					Spec: api.FleetSpec{
						Template: struct {
							Metadata *api.ObjectMeta `json:"metadata,omitempty"`
							Spec     api.DeviceSpec  `json:"spec"`
						}{
							Metadata: &api.ObjectMeta{
								Labels: &map[string]string{
									"environment": "test",
								},
							},
							Spec: api.DeviceSpec{
								Os: &api.DeviceOsSpec{
									Image: "quay.io/test/os:latest",
								},
							},
						},
					},
				},
			}

			// Test the helper method
			err := resourceSync.SyncFleets(ctx, log, rs, fleets, "sync-test-resourcesync")
			Expect(err).ToNot(HaveOccurred())

			// Verify conditions were set
			Expect(rs.Status.Conditions).ToNot(BeNil())

			// Check for synced condition
			syncedCondition := api.FindStatusCondition(rs.Status.Conditions, api.ConditionTypeResourceSyncSynced)
			Expect(syncedCondition).ToNot(BeNil())
			Expect(syncedCondition.Status).To(Equal(api.ConditionStatusTrue))
		})

		It("should handle fleet name conflicts", func() {
			// Create a repository and ResourceSync
			createTestRepository("conflict-test-repo", "https://github.com/test/repo")
			rs := createTestResourceSync("conflict-test-resourcesync", "conflict-test-repo", "/examples")

			// Create a fleet with the same name that's owned by a different ResourceSync
			conflictingFleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name:  lo.ToPtr("conflicting-fleet"),
					Owner: lo.ToPtr("ResourceSync/different-owner"),
				},
				Spec: api.FleetSpec{
					Template: struct {
						Metadata *api.ObjectMeta `json:"metadata,omitempty"`
						Spec     api.DeviceSpec  `json:"spec"`
					}{
						Spec: api.DeviceSpec{
							Os: &api.DeviceOsSpec{
								Image: "quay.io/test/os:latest",
							},
						},
					},
				},
			}

			// Create the conflicting fleet first
			_, status := serviceHandler.CreateFleet(ctx, *conflictingFleet)
			Expect(status.Code).To(Equal(int32(201)))

			// Update the fleet to set the owner
			_, status = serviceHandler.ReplaceFleet(ctx, *conflictingFleet.Metadata.Name, *conflictingFleet)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify the fleet was created with the correct owner
			createdFleet, status := serviceHandler.GetFleet(ctx, "conflicting-fleet", api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(200)))
			Expect(createdFleet.Metadata.Owner).ToNot(BeNil())
			Expect(*createdFleet.Metadata.Owner).To(Equal("ResourceSync/different-owner"))

			// Try to sync a fleet with the same name
			fleets := []*api.Fleet{
				{
					Metadata: api.ObjectMeta{
						Name: lo.ToPtr("conflicting-fleet"),
					},
					Spec: api.FleetSpec{
						Template: struct {
							Metadata *api.ObjectMeta `json:"metadata,omitempty"`
							Spec     api.DeviceSpec  `json:"spec"`
						}{
							Spec: api.DeviceSpec{
								Os: &api.DeviceOsSpec{
									Image: "quay.io/test/os:latest",
								},
							},
						},
					},
				},
			}

			// Test the helper method
			err := resourceSync.SyncFleets(ctx, log, rs, fleets, "conflict-test-resourcesync")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fleet name(s)"))
		})
	})

	Context("ResourceSync Event Emission", func() {
		It("should emit events when repository access is validated", func() {
			// Create a repository
			createTestRepository("event-test-repo", "https://github.com/test/repo")

			// Create a ResourceSync
			resourceSyncName := "event-test-resourcesync"
			rs := createTestResourceSync(resourceSyncName, "event-test-repo", "/examples")

			// Call the helper method
			_, err := resourceSync.GetRepositoryAndValidateAccess(ctx, rs)
			Expect(err).ToNot(HaveOccurred())

			// Update the status to trigger event emission
			_, status := serviceHandler.ReplaceResourceSyncStatus(ctx, *rs.Metadata.Name, *rs)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify events were emitted
			events := getEventsForResourceSync(resourceSyncName)
			Expect(len(events)).To(BeNumerically(">", 0))

			// Check for accessible event
			accessibleEvent := findEventByReason(events, api.EventReasonResourceSyncAccessible)
			Expect(accessibleEvent).ToNot(BeNil())
			Expect(accessibleEvent.Type).To(Equal(api.Normal))
		})

		It("should emit warning events when repository access fails", func() {
			// Create a ResourceSync that references a non-existent repository
			resourceSyncName := "warning-event-test-resourcesync"
			rs := createTestResourceSync(resourceSyncName, "non-existent-repo", "/examples")

			// Call the helper method
			_, err := resourceSync.GetRepositoryAndValidateAccess(ctx, rs)
			Expect(err).To(HaveOccurred())

			// Update the status to trigger event emission
			_, status := serviceHandler.ReplaceResourceSyncStatus(ctx, *rs.Metadata.Name, *rs)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify events were emitted
			events := getEventsForResourceSync(resourceSyncName)
			Expect(len(events)).To(BeNumerically(">", 0))

			// Check for inaccessible event
			inaccessibleEvent := findEventByReason(events, api.EventReasonResourceSyncInaccessible)
			Expect(inaccessibleEvent).ToNot(BeNil())
			Expect(inaccessibleEvent.Type).To(Equal(api.Warning))
			Expect(inaccessibleEvent.Message).To(ContainSubstring("Repository is inaccessible"))
		})

		It("should emit events with correct involved object references", func() {
			createTestRepository("ref-test-repo", "https://github.com/test/repo")
			resourceSyncName := "ref-test-resourcesync"
			createTestResourceSync(resourceSyncName, "ref-test-repo", "/examples")

			// Call the helper method
			rs, _ := serviceHandler.GetResourceSync(ctx, resourceSyncName)
			_, err := resourceSync.GetRepositoryAndValidateAccess(ctx, rs)
			Expect(err).ToNot(HaveOccurred())

			events := getEventsForResourceSync(resourceSyncName)
			Expect(len(events)).To(BeNumerically(">", 0))

			// Verify all events have correct involved object references
			for _, event := range events {
				Expect(event.InvolvedObject.Kind).To(Equal(api.ResourceSyncKind))
				Expect(event.InvolvedObject.Name).To(Equal(resourceSyncName))
				Expect(event.Metadata.Name).ToNot(BeNil())
			}
		})
	})
})
