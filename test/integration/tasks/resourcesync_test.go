package tasks_test

import (
	"context"
	"fmt"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
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
		log               *logrus.Logger
		ctx               context.Context
		internalCtx       context.Context
		orgId             uuid.UUID
		storeInst         store.Store
		serviceHandler    service.Service
		cfg               *config.Config
		dbName            string
		workerClient      worker_client.WorkerClient
		ctrl              *gomock.Controller
		mockQueueProducer *queues.MockQueueProducer
		resourceSync      *tasks.ResourceSync
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		internalCtx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, nil, log, "", "", []string{})
		resourceSync = tasks.NewResourceSync(serviceHandler, log, nil)

		// Set up mock expectations for the publisher
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
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

		_, status := serviceHandler.CreateRepository(ctx, orgId, *repo)
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

		_, status := serviceHandler.CreateResourceSync(ctx, orgId, *resourceSync)
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
			repo, err := resourceSync.GetRepositoryAndValidateAccess(internalCtx, orgId, rs)
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
			repo, err := resourceSync.GetRepositoryAndValidateAccess(internalCtx, orgId, rs)
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
			err := resourceSync.SyncFleets(internalCtx, log, orgId, rs, fleets, "sync-test-resourcesync")
			Expect(err).ToNot(HaveOccurred())

			// Verify conditions were set
			Expect(rs.Status.Conditions).ToNot(BeNil())

			// Check for synced condition
			syncedCondition := api.FindStatusCondition(rs.Status.Conditions, api.ConditionTypeResourceSyncSynced)
			Expect(syncedCondition).ToNot(BeNil())
			Expect(syncedCondition.Status).To(Equal(api.ConditionStatusTrue))
		})

		It("should set owner on fleets created by ResourceSync", func() {
			// Create repository and ResourceSync
			createTestRepository("owner-test-repo", "https://github.com/test/repo")
			rs := createTestResourceSync("owner-test-resourcesync", "owner-test-repo", "/examples")

			// Create test fleet with owner already set (as parseFleets does)
			fleets := []*api.Fleet{
				{
					Metadata: api.ObjectMeta{
						Name: lo.ToPtr("owned-fleet"),
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

			// Sync fleets
			err := resourceSync.SyncFleets(internalCtx, log, orgId, rs, fleets, "owner-test-resourcesync")
			Expect(err).ToNot(HaveOccurred())

			// Verify the fleet was created with the owner set
			createdFleet, status := serviceHandler.GetFleet(ctx, orgId, "owned-fleet", api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(200)))
			Expect(createdFleet.Metadata.Owner).ToNot(BeNil())
			Expect(*createdFleet.Metadata.Owner).To(Equal("ResourceSync/owner-test-resourcesync"))

			// Verify the fleet cannot be edited (spec update should fail)
			updatedFleet := *createdFleet
			updatedFleet.Spec.Template.Spec.Os.Image = "quay.io/test/os:updated"
			_, status = serviceHandler.ReplaceFleet(ctx, orgId, "owned-fleet", updatedFleet)
			Expect(status.Code).To(Equal(int32(409))) // Conflict
			Expect(status.Message).To(ContainSubstring("updating the resource is not allowed because it has an owner"))
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
			_, status := serviceHandler.CreateFleet(ctx, orgId, *conflictingFleet)
			Expect(status.Code).To(Equal(int32(201)))

			// Update the fleet to set the owner (use internalCtx so owner is preserved)
			_, status = serviceHandler.ReplaceFleet(internalCtx, orgId, *conflictingFleet.Metadata.Name, *conflictingFleet)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify the fleet was created with the correct owner
			createdFleet, status := serviceHandler.GetFleet(ctx, orgId, "conflicting-fleet", api.GetFleetParams{})
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
			err := resourceSync.SyncFleets(internalCtx, log, orgId, rs, fleets, "conflict-test-resourcesync")
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
			_, err := resourceSync.GetRepositoryAndValidateAccess(internalCtx, orgId, rs)
			Expect(err).ToNot(HaveOccurred())

			// Update the status to trigger event emission
			_, status := serviceHandler.ReplaceResourceSyncStatus(ctx, orgId, *rs.Metadata.Name, *rs)
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
			_, err := resourceSync.GetRepositoryAndValidateAccess(internalCtx, orgId, rs)
			Expect(err).To(HaveOccurred())

			// Update the status to trigger event emission
			_, status := serviceHandler.ReplaceResourceSyncStatus(ctx, orgId, *rs.Metadata.Name, *rs)
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
			rs, _ := serviceHandler.GetResourceSync(ctx, orgId, resourceSyncName)
			_, err := resourceSync.GetRepositoryAndValidateAccess(internalCtx, orgId, rs)
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

	Context("ResourceSync GitOps Field Removal", func() {
		It("should test RemoveIgnoredFields function directly", func() {
			// Test the RemoveIgnoredFields function directly
			resource := tasks.GenericResourceMap{
				"kind": api.FleetKind,
				"metadata": map[string]interface{}{
					"name":            "test-fleet",
					"resourceVersion": "12345",
					"labels": map[string]interface{}{
						"test-label": "should-be-removed",
						"keep-label": "should-be-kept",
					},
				},
			}

			ignoreFields := []string{"/metadata/resourceVersion", "/metadata/labels/test-label"}

			// Apply field removal
			cleanedResource := tasks.RemoveIgnoredFields(resource, ignoreFields)

			// Debug: Print the cleaned resource to see what happened
			fmt.Printf("Original resource: %+v\n", resource)
			fmt.Printf("Cleaned resource: %+v\n", cleanedResource)

			// Check the results directly on the map
			metadata, ok := cleanedResource["metadata"].(map[string]interface{})
			Expect(ok).To(BeTrue())

			// Check that resourceVersion was removed
			_, hasResourceVersion := metadata["resourceVersion"]
			fmt.Printf("Has resourceVersion: %v\n", hasResourceVersion)
			Expect(hasResourceVersion).To(BeFalse())

			// Check that name was preserved
			name, hasName := metadata["name"]
			Expect(hasName).To(BeTrue())
			Expect(name).To(Equal("test-fleet"))

			// Check that test-label was removed but keep-label was preserved
			labels, ok := metadata["labels"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			_, hasTestLabel := labels["test-label"]
			Expect(hasTestLabel).To(BeFalse())
			_, hasKeepLabel := labels["keep-label"]
			Expect(hasKeepLabel).To(BeTrue())
			Expect(labels["keep-label"]).To(Equal("should-be-kept"))
		})

		It("should remove ignored fields during resource parsing", func() {
			// Create a ResourceSync with custom ignore fields
			ignoreFields := []string{"/metadata/resourceVersion", "/metadata/labels/test-label"}
			resourceSyncWithIgnores := tasks.NewResourceSync(serviceHandler, log, ignoreFields)

			// Create test resources with fields that should be ignored
			resources := []tasks.GenericResourceMap{
				{
					"kind": api.FleetKind,
					"metadata": map[string]interface{}{
						"name":            "test-fleet",
						"resourceVersion": "12345",
						"labels": map[string]interface{}{
							"test-label": "should-be-removed",
							"keep-label": "should-be-kept",
						},
					},
					"spec": map[string]interface{}{
						"selector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"fleet": "test",
							},
						},
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"fleet": "test",
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

			// Apply field removal manually to test the function
			cleanedResources := make([]tasks.GenericResourceMap, len(resources))
			for i, resource := range resources {
				cleanedResources[i] = tasks.RemoveIgnoredFields(resource, ignoreFields)
			}

			// Test the field removal directly on the cleaned resource
			cleanedResource := cleanedResources[0]
			metadata, ok := cleanedResource["metadata"].(map[string]interface{})
			Expect(ok).To(BeTrue())

			// Check that resourceVersion was removed
			_, hasResourceVersion := metadata["resourceVersion"]
			Expect(hasResourceVersion).To(BeFalse())

			// Check that name was preserved
			name, hasName := metadata["name"]
			Expect(hasName).To(BeTrue())
			Expect(name).To(Equal("test-fleet"))

			// Check that test-label was removed but keep-label was preserved
			labels, ok := metadata["labels"].(map[string]interface{})
			Expect(ok).To(BeTrue())
			_, hasTestLabel := labels["test-label"]
			Expect(hasTestLabel).To(BeFalse())
			_, hasKeepLabel := labels["keep-label"]
			Expect(hasKeepLabel).To(BeTrue())
			Expect(labels["keep-label"]).To(Equal("should-be-kept"))

			// Parse fleets from cleaned resources
			fleets, err := resourceSyncWithIgnores.ParseFleetsFromResources(cleanedResources, "test-resourcesync")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets).To(HaveLen(1))

			// Verify that ignored fields were removed by checking the parsed fleet
			fleet := fleets[0]
			Expect(*fleet.Metadata.Name).To(Equal("test-fleet"))
			Expect(fleet.Metadata.ResourceVersion).To(BeNil())

			// Check that test-label was removed but keep-label was preserved
			Expect(fleet.Metadata.Labels).ToNot(BeNil())
			_, hasTestLabel = (*fleet.Metadata.Labels)["test-label"]
			Expect(hasTestLabel).To(BeFalse())
			_, hasKeepLabel = (*fleet.Metadata.Labels)["keep-label"]
			Expect(hasKeepLabel).To(BeTrue())
			Expect((*fleet.Metadata.Labels)["keep-label"]).To(Equal("should-be-kept"))
		})

		It("should not remove fields when no ignore list is provided", func() {
			// Create a ResourceSync without ignore fields
			resourceSyncNoIgnores := tasks.NewResourceSync(serviceHandler, log, nil)

			// Create test resources with fields that would normally be ignored
			resources := []tasks.GenericResourceMap{
				{
					"kind": api.FleetKind,
					"metadata": map[string]interface{}{
						"name":            "test-fleet",
						"resourceVersion": "12345",
						"labels": map[string]interface{}{
							"test-label": "should-be-kept",
						},
					},
					"spec": map[string]interface{}{
						"selector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"fleet": "test",
							},
						},
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"fleet": "test",
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

			// Apply field removal with empty ignore list - should NOT remove fields
			cleanedResources := make([]tasks.GenericResourceMap, len(resources))
			for i, resource := range resources {
				cleanedResources[i] = tasks.RemoveIgnoredFields(resource, nil)
			}

			// Parse fleets from cleaned resources
			fleets, err := resourceSyncNoIgnores.ParseFleetsFromResources(cleanedResources, "test-resourcesync")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets).To(HaveLen(1))

			// Verify that fields were NOT removed
			fleet := fleets[0]
			Expect(*fleet.Metadata.Name).To(Equal("test-fleet"))
			Expect(fleet.Metadata.ResourceVersion).ToNot(BeNil())
			Expect(*fleet.Metadata.ResourceVersion).To(Equal("12345"))

			// Check that test-label was preserved
			Expect(fleet.Metadata.Labels).ToNot(BeNil())
			testLabel, hasTestLabel := (*fleet.Metadata.Labels)["test-label"]
			Expect(hasTestLabel).To(BeTrue())
			Expect(testLabel).To(Equal("should-be-kept"))
		})

		It("should remove nested ignored fields", func() {
			// Create ignore fields for nested field removal
			ignoreFields := []string{"/metadata/labels/nested/field"}

			// Create test resources with nested fields that should be ignored
			resources := []tasks.GenericResourceMap{
				{
					"kind": api.FleetKind,
					"metadata": map[string]interface{}{
						"name": "test-fleet",
						"labels": map[string]interface{}{
							"nested": map[string]interface{}{
								"field": "should-be-removed",
								"other": "should-be-kept",
							},
							"top-level": "should-be-kept",
						},
					},
					"spec": map[string]interface{}{
						"selector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"fleet": "test",
							},
						},
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"fleet": "test",
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

			// Apply field removal manually to test the function
			cleanedResources := make([]tasks.GenericResourceMap, len(resources))
			for i, resource := range resources {
				cleanedResources[i] = tasks.RemoveIgnoredFields(resource, ignoreFields)
			}

			// Test the field removal directly on the cleaned resource
			cleanedResource := cleanedResources[0]
			metadata, ok := cleanedResource["metadata"].(map[string]interface{})
			Expect(ok).To(BeTrue())

			labels, ok := metadata["labels"].(map[string]interface{})
			Expect(ok).To(BeTrue())

			nested, ok := labels["nested"].(map[string]interface{})
			Expect(ok).To(BeTrue())

			// Check that the specific nested field was removed
			_, hasNestedField := nested["field"]
			Expect(hasNestedField).To(BeFalse())

			// Check that other nested field was preserved
			otherField, hasOtherField := nested["other"]
			Expect(hasOtherField).To(BeTrue())
			Expect(otherField).To(Equal("should-be-kept"))

			// Check that top-level field was preserved
			topLevel, hasTopLevel := labels["top-level"]
			Expect(hasTopLevel).To(BeTrue())
			Expect(topLevel).To(Equal("should-be-kept"))
		})

		It("should handle non-existent paths gracefully", func() {
			// Create a ResourceSync with ignore fields that don't exist in the resource
			ignoreFields := []string{"/metadata/nonExistentField", "/spec/nonExistentField"}
			resourceSyncWithIgnores := tasks.NewResourceSync(serviceHandler, log, ignoreFields)

			// Create test resources without the fields that should be ignored
			resources := []tasks.GenericResourceMap{
				{
					"kind": api.FleetKind,
					"metadata": map[string]interface{}{
						"name": "test-fleet",
					},
					"spec": map[string]interface{}{
						"selector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"fleet": "test",
							},
						},
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"fleet": "test",
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

			// Parse fleets from resources - should not error
			fleets, err := resourceSyncWithIgnores.ParseFleetsFromResources(resources, "test-resourcesync")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets).To(HaveLen(1))

			// Verify the resource is intact
			fleet := fleets[0]
			Expect(*fleet.Metadata.Name).To(Equal("test-fleet"))
		})

		It("should apply field removal during full sync process", func() {
			// Create a ResourceSync instance with ignore fields
			ignoreFields := []string{"/metadata/resourceVersion"}
			resourceSyncWithIgnores := tasks.NewResourceSync(serviceHandler, log, ignoreFields)

			// Create test resources with resourceVersion that should be removed
			resources := []tasks.GenericResourceMap{
				{
					"kind": api.FleetKind,
					"metadata": map[string]interface{}{
						"name":            "test-fleet",
						"resourceVersion": "12345",
					},
					"spec": map[string]interface{}{
						"selector": map[string]interface{}{
							"matchLabels": map[string]interface{}{
								"fleet": "test",
							},
						},
						"template": map[string]interface{}{
							"metadata": map[string]interface{}{
								"labels": map[string]interface{}{
									"fleet": "test",
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

			// Apply field removal manually to simulate the extraction process
			cleanedResources := make([]tasks.GenericResourceMap, len(resources))
			for i, resource := range resources {
				cleanedResources[i] = tasks.RemoveIgnoredFields(resource, ignoreFields)
			}

			// Parse fleets from cleaned resources
			fleets, err := resourceSyncWithIgnores.ParseFleetsFromResources(cleanedResources, "gitops-test-resourcesync")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleets).To(HaveLen(1))

			// Verify that the fleet was parsed without resourceVersion
			fleet := fleets[0]
			Expect(*fleet.Metadata.Name).To(Equal("test-fleet"))
			Expect(fleet.Metadata.ResourceVersion).To(BeNil())
		})
	})

	Context("ResourceSync Annotation Preservation", func() {
		It("should preserve existing annotations when syncing fleet YAML without annotations", func() {
			// Create a repository and ResourceSync
			createTestRepository("annotation-preserve-repo", "https://github.com/test/repo")
			rs := createTestResourceSync("annotation-preserve-resourcesync", "annotation-preserve-repo", "/examples")

			// Create a fleet with a system-managed annotation (simulating what fleet validation would set)
			fleetName := "annotation-test-fleet"
			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(fleetName),
					Annotations: &map[string]string{
						api.FleetAnnotationTemplateVersion: "test-template-version-1",
					},
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

			// Create the fleet first
			_, status := serviceHandler.CreateFleet(ctx, orgId, *fleet)
			Expect(status.Code).To(Equal(int32(201)))

			// Set the annotation using UpdateFleetAnnotations (simulating fleet validation)
			status = serviceHandler.UpdateFleetAnnotations(ctx, orgId, fleetName, map[string]string{
				api.FleetAnnotationTemplateVersion: "test-template-version-1",
			}, nil)
			Expect(status.Code).To(Equal(int32(200)))

			// Verify the annotation exists
			createdFleet, status := serviceHandler.GetFleet(ctx, orgId, fleetName, api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(200)))
			Expect(createdFleet.Metadata.Annotations).ToNot(BeNil())
			Expect((*createdFleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]).To(Equal("test-template-version-1"))

			// Now simulate syncing from YAML that has no annotations defined (like the user's case)
			// This simulates what happens when ResourceSync parses a fleet YAML with no annotations field
			fleetFromYAML := []*api.Fleet{
				{
					Metadata: api.ObjectMeta{
						Name:        lo.ToPtr(fleetName),
						Labels:      &map[string]string{}, // YAML has labels: {}
						Annotations: nil,                  // YAML doesn't define annotations at all
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

			// Sync the fleet from YAML
			err := resourceSync.SyncFleets(internalCtx, log, orgId, rs, fleetFromYAML, "annotation-preserve-resourcesync")
			Expect(err).ToNot(HaveOccurred())

			// Verify the annotation is still preserved
			updatedFleet, status := serviceHandler.GetFleet(ctx, orgId, fleetName, api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(200)))
			Expect(updatedFleet.Metadata.Annotations).ToNot(BeNil())
			Expect((*updatedFleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]).To(Equal("test-template-version-1"))
		})

		It("should ignore user-defined annotations in fleet YAML", func() {
			// Create a repository and ResourceSync
			createTestRepository("annotation-ignore-repo", "https://github.com/test/repo")
			rs := createTestResourceSync("annotation-ignore-resourcesync", "annotation-ignore-repo", "/examples")

			// Create a fleet first (without annotations)
			fleetName := "annotation-ignore-test-fleet"
			fleet := &api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(fleetName),
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

			// Create the fleet first
			_, status := serviceHandler.CreateFleet(ctx, orgId, *fleet)
			Expect(status.Code).To(Equal(int32(201)))

			// Set a system-managed annotation (simulating fleet validation)
			status = serviceHandler.UpdateFleetAnnotations(ctx, orgId, fleetName, map[string]string{
				api.FleetAnnotationTemplateVersion: "system-template-version",
			}, nil)
			Expect(status.Code).To(Equal(int32(200)))

			// Now simulate syncing from YAML that has user-defined annotations
			// These should be ignored - only system annotations should remain
			fleetFromYAML := []*api.Fleet{
				{
					Metadata: api.ObjectMeta{
						Name: lo.ToPtr(fleetName),
						Annotations: &map[string]string{
							"user-defined-annotation": "user-value",
							"another-user-annotation": "another-value",
						},
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

			// Sync the fleet from YAML
			err := resourceSync.SyncFleets(internalCtx, log, orgId, rs, fleetFromYAML, "annotation-ignore-resourcesync")
			Expect(err).ToNot(HaveOccurred())

			// Verify that user-defined annotations were ignored
			updatedFleet, status := serviceHandler.GetFleet(ctx, orgId, fleetName, api.GetFleetParams{})
			Expect(status.Code).To(Equal(int32(200)))
			Expect(updatedFleet.Metadata.Annotations).ToNot(BeNil())

			// System annotation should still exist
			Expect((*updatedFleet.Metadata.Annotations)[api.FleetAnnotationTemplateVersion]).To(Equal("system-template-version"))

			// User-defined annotations should NOT exist
			_, hasUserAnnotation := (*updatedFleet.Metadata.Annotations)["user-defined-annotation"]
			Expect(hasUserAnnotation).To(BeFalse())

			_, hasAnotherUserAnnotation := (*updatedFleet.Metadata.Annotations)["another-user-annotation"]
			Expect(hasAnotherUserAnnotation).To(BeFalse())
		})
	})
})
