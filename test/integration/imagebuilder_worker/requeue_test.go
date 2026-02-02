package imagebuilder_worker_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiimagebuilder "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/tasks"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/testutil"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

// TestRequeueSuite is kept for compatibility but RunSpecs is handled by the package-level suite
func TestRequeueSuite(t *testing.T) {
	// RunSpecs is called in containerfile_test.go for the entire package
}

var _ = Describe("Requeue Integration Tests", func() {
	var (
		log                 *logrus.Logger
		ctx                 context.Context
		orgID               uuid.UUID
		mainStore           flightctlstore.Store
		imageBuilderStore   imagebuilderstore.Store
		imageBuilderService service.Service
		consumer            *tasks.Consumer
		cfg                 *config.Config
		dbName              string
		db                  *gorm.DB
		ctrl                *gomock.Controller
		mockQueueProducer   *queues.MockQueueProducer
		enqueuedEvents      []EnqueuedEvent
		testID              string
		testRepoName        string
		sourceRepoName      string
		outputRepoName      string
	)

	BeforeEach(func() {
		ctx = testutilpkg.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()

		// Extract test ID from context
		testID = ctx.Value(testutilpkg.TestIDKey).(string)
		testRepoName = fmt.Sprintf("test-repo-%s", testID)
		sourceRepoName = fmt.Sprintf("source-repo-%s", testID)
		outputRepoName = fmt.Sprintf("output-repo-%s", testID)

		// Use main store's PrepareDBForUnitTests which includes organizations table
		mainStore, cfg, dbName, db = flightctlstore.PrepareDBForUnitTests(ctx, log)

		// Create imagebuilder store on the same db connection
		imageBuilderStore = imagebuilderstore.NewStore(db, log.WithField("pkg", "imagebuilder-store"))

		// Run imagebuilder-specific migrations only for local strategy
		strategy := os.Getenv("FLIGHTCTL_TEST_DB_STRATEGY")
		if strategy != testutil.StrategyTemplate {
			err := imageBuilderStore.RunMigrations(ctx)
			Expect(err).ToNot(HaveOccurred())
		}

		// Create test organization (required for foreign key constraint)
		orgID = uuid.New()
		err := testutilpkg.CreateTestOrganization(ctx, mainStore, orgID)
		Expect(err).ToNot(HaveOccurred())

		// Create required repositories for ImageBuild/ImageExport tests with unique test-id-based names
		_, err = createOCIRepository(ctx, mainStore.Repository(), orgID, testRepoName, "quay.io", nil)
		Expect(err).ToNot(HaveOccurred())
		_, err = createOCIRepository(ctx, mainStore.Repository(), orgID, sourceRepoName, "quay.io", nil)
		Expect(err).ToNot(HaveOccurred())
		// Create output-repo with ReadWrite access mode (required for ImageExport destination)
		outputRepo, err := createOCIRepository(ctx, mainStore.Repository(), orgID, outputRepoName, "quay.io", nil)
		Expect(err).ToNot(HaveOccurred())
		// Update output-repo to have ReadWrite access mode
		ociSpec, err := outputRepo.Spec.AsOciRepoSpec()
		Expect(err).ToNot(HaveOccurred())
		ociSpec.AccessMode = lo.ToPtr(v1beta1.ReadWrite)
		err = outputRepo.Spec.FromOciRepoSpec(ociSpec)
		Expect(err).ToNot(HaveOccurred())
		_, err = mainStore.Repository().Update(ctx, orgID, outputRepo, flightctlstore.EventCallback(func(context.Context, v1beta1.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
		}))
		Expect(err).ToNot(HaveOccurred())

		// Create imagebuilder service
		imageBuilderService = service.NewService(ctx, cfg, imageBuilderStore, mainStore, nil, nil, log)

		// Setup mock queue producer to capture enqueued events
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		enqueuedEvents = make([]EnqueuedEvent, 0)

		// Capture enqueued events
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, payload []byte, timestamp int64) error {
				var eventWithOrgId worker_client.EventWithOrgId
				if err := json.Unmarshal(payload, &eventWithOrgId); err == nil {
					enqueuedEvents = append(enqueuedEvents, EnqueuedEvent{
						OrgID:   eventWithOrgId.OrgId,
						Event:   eventWithOrgId.Event,
						Payload: payload,
					})
				}
				return nil
			}).
			AnyTimes()

		// Create consumer
		consumer = tasks.NewConsumer(
			imageBuilderStore,
			mainStore,
			nil, // kvStore
			nil, // serviceHandler
			imageBuilderService,
			mockQueueProducer,
			&config.Config{},
			log,
		)
	})

	AfterEach(func() {
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStore, dbName)
		ctrl.Finish()
	})

	// Helper function to create an ImageBuild with specific status
	createImageBuildWithStatus := func(name string, reason apiimagebuilder.ImageBuildConditionReason) *apiimagebuilder.ImageBuild {
		imageBuild := &apiimagebuilder.ImageBuild{
			ApiVersion: apiimagebuilder.ImageBuildAPIVersion,
			Kind:       string(apiimagebuilder.ResourceKindImageBuild),
			Metadata: v1beta1.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: apiimagebuilder.ImageBuildSpec{
				Source: apiimagebuilder.ImageBuildSource{
					Repository: testRepoName,
					ImageName:  "test-image",
					ImageTag:   "v1.0.0",
				},
				Destination: apiimagebuilder.ImageBuildDestination{
					Repository: outputRepoName,
					ImageName:  "output-image",
					ImageTag:   "v1.0.0",
				},
			},
		}

		// Create the resource (status will be set to Pending by default)
		_, status := imageBuilderService.ImageBuild().Create(ctx, orgID, *imageBuild)
		Expect(status.Code).To(Equal(int32(201)))

		// Get the created resource to update its status
		toUpdate, status := imageBuilderService.ImageBuild().Get(ctx, orgID, name, false)
		Expect(status.Code).To(Equal(int32(200)))

		// Set the desired condition
		now := time.Now().UTC()
		conditions := []apiimagebuilder.ImageBuildCondition{
			{
				Type:               apiimagebuilder.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(reason),
				Message:            "test",
				LastTransitionTime: now,
			},
		}
		toUpdate.Status = &apiimagebuilder.ImageBuildStatus{
			Conditions: &conditions,
		}

		// Update status
		_, err := imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, toUpdate)
		Expect(err).ToNot(HaveOccurred())

		// Get the final resource
		updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, name, false)
		Expect(status.Code).To(Equal(int32(200)))
		return updated
	}

	// Helper function to create an ImageExport with specific status
	createImageExportWithStatus := func(name string, reason apiimagebuilder.ImageExportConditionReason) *apiimagebuilder.ImageExport {
		// First create an ImageBuild that the ImageExport will reference
		// Set it to Completed so it doesn't get requeued during requeue tests
		imageBuildName := "build-for-" + name
		_ = createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

		source := apiimagebuilder.ImageExportSource{}
		err := source.FromImageBuildRefSource(apiimagebuilder.ImageBuildRefSource{
			Type:          apiimagebuilder.ImageBuildRefSourceTypeImageBuild,
			ImageBuildRef: imageBuildName,
		})
		Expect(err).ToNot(HaveOccurred())

		imageExport := &apiimagebuilder.ImageExport{
			ApiVersion: apiimagebuilder.ImageExportAPIVersion,
			Kind:       string(apiimagebuilder.ResourceKindImageExport),
			Metadata: v1beta1.ObjectMeta{
				Name: lo.ToPtr(name),
			},
			Spec: apiimagebuilder.ImageExportSpec{
				Source: source,
				Format: apiimagebuilder.ExportFormatTypeQCOW2,
			},
		}

		// Create the resource (status will be set to Pending by default)
		_, exportStatus := imageBuilderService.ImageExport().Create(ctx, orgID, *imageExport)
		Expect(exportStatus.Code).To(Equal(int32(201)))

		// Get the created resource to update its status
		toUpdate, status := imageBuilderService.ImageExport().Get(ctx, orgID, name)
		Expect(status.Code).To(Equal(int32(200)))

		// Set the desired condition
		now := time.Now().UTC()
		conditions := []apiimagebuilder.ImageExportCondition{
			{
				Type:               apiimagebuilder.ImageExportConditionTypeReady,
				Status:             v1beta1.ConditionStatusFalse,
				Reason:             string(reason),
				Message:            "test",
				LastTransitionTime: now,
			},
		}
		toUpdate.Status = &apiimagebuilder.ImageExportStatus{
			Conditions: &conditions,
		}

		// Update status
		_, err = imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, toUpdate)
		Expect(err).ToNot(HaveOccurred())

		// Get the final resource
		updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, name)
		Expect(status.Code).To(Equal(int32(200)))
		return updated
	}

	Context("Requeue on Startup", func() {
		It("should requeue Pending ImageBuilds", func() {
			// Create a Pending ImageBuild
			createImageBuildWithStatus("pending-build-1", apiimagebuilder.ImageBuildConditionReasonPending)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that the ImageBuild was requeued
			Expect(len(enqueuedEvents)).To(Equal(1))
			event := enqueuedEvents[0]
			Expect(event.OrgID).To(Equal(orgID))
			Expect(event.Event.InvolvedObject.Kind).To(Equal(string(apiimagebuilder.ResourceKindImageBuild)))
			Expect(event.Event.InvolvedObject.Name).To(Equal("pending-build-1"))
			Expect(event.Event.Reason).To(Equal(v1beta1.EventReasonResourceCreated))
		})

		It("should mark non-Pending, non-terminal ImageBuilds as Failed", func() {
			// Create a Building ImageBuild (started processing)
			createImageBuildWithStatus("building-build-1", apiimagebuilder.ImageBuildConditionReasonBuilding)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))

			// Verify that the ImageBuild was marked as Failed
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "building-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonFailed)))
			Expect(readyCondition.Message).To(ContainSubstring("was in progress on startup"))
		})

		It("should not requeue Completed ImageBuilds", func() {
			// Create a Completed ImageBuild
			createImageBuildWithStatus("completed-build-1", apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))

			// Verify that the ImageBuild status is unchanged
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "completed-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonCompleted)))
		})

		It("should not requeue Failed ImageBuilds", func() {
			// Create a Failed ImageBuild
			createImageBuildWithStatus("failed-build-1", apiimagebuilder.ImageBuildConditionReasonFailed)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))

			// Verify that the ImageBuild status is unchanged
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "failed-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonFailed)))
		})

		It("should requeue Pending ImageExports", func() {
			// Create a Pending ImageExport
			createImageExportWithStatus("pending-export-1", apiimagebuilder.ImageExportConditionReasonPending)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that the ImageExport was requeued
			Expect(len(enqueuedEvents)).To(Equal(1))
			event := enqueuedEvents[0]
			Expect(event.OrgID).To(Equal(orgID))
			Expect(event.Event.InvolvedObject.Kind).To(Equal(string(apiimagebuilder.ResourceKindImageExport)))
			Expect(event.Event.InvolvedObject.Name).To(Equal("pending-export-1"))
			Expect(event.Event.Reason).To(Equal(v1beta1.EventReasonResourceCreated))
		})

		It("should mark non-Pending, non-terminal ImageExports as Failed", func() {
			// Create a Converting ImageExport (started processing)
			createImageExportWithStatus("converting-export-1", apiimagebuilder.ImageExportConditionReasonConverting)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))

			// Verify that the ImageExport was marked as Failed
			updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, "converting-export-1")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageExportStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonFailed)))
			Expect(readyCondition.Message).To(ContainSubstring("was in progress on startup"))
		})

		It("should handle mix of resources correctly", func() {
			// Create resources with different states
			createImageBuildWithStatus("pending-build-2", apiimagebuilder.ImageBuildConditionReasonPending)
			createImageBuildWithStatus("building-build-2", apiimagebuilder.ImageBuildConditionReasonBuilding)
			createImageBuildWithStatus("completed-build-2", apiimagebuilder.ImageBuildConditionReasonCompleted)
			createImageExportWithStatus("pending-export-2", apiimagebuilder.ImageExportConditionReasonPending)
			createImageExportWithStatus("converting-export-2", apiimagebuilder.ImageExportConditionReasonConverting)
			createImageExportWithStatus("completed-export-2", apiimagebuilder.ImageExportConditionReasonCompleted)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that only Pending resources were requeued
			Expect(len(enqueuedEvents)).To(Equal(2))

			// Check that pending-build-2 and pending-export-2 were requeued
			requeuedNames := make(map[string]bool)
			for _, event := range enqueuedEvents {
				requeuedNames[event.Event.InvolvedObject.Name] = true
			}
			Expect(requeuedNames["pending-build-2"]).To(BeTrue())
			Expect(requeuedNames["pending-export-2"]).To(BeTrue())

			// Verify building-build-2 was marked as Failed
			build, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "building-build-2", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*build.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonFailed)))

			// Verify completed-build-2 was not changed
			build, status = imageBuilderService.ImageBuild().Get(ctx, orgID, "completed-build-2", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition = apiimagebuilder.FindImageBuildStatusCondition(*build.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonCompleted)))

			// Verify converting-export-2 was marked as Failed
			export, status := imageBuilderService.ImageExport().Get(ctx, orgID, "converting-export-2")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition2 := apiimagebuilder.FindImageExportStatusCondition(*export.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition2.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonFailed)))

			// Verify completed-export-2 was not changed
			export, status = imageBuilderService.ImageExport().Get(ctx, orgID, "completed-export-2")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition2 = apiimagebuilder.FindImageExportStatusCondition(*export.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition2.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonCompleted)))
		})

		It("should mark Pushing ImageBuild as Failed", func() {
			// Create a Pushing ImageBuild (started processing)
			createImageBuildWithStatus("pushing-build-1", apiimagebuilder.ImageBuildConditionReasonPushing)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))

			// Verify that the ImageBuild was marked as Failed
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "pushing-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonFailed)))
		})

		It("should mark Pushing ImageExport as Failed", func() {
			// Create a Pushing ImageExport (started processing)
			createImageExportWithStatus("pushing-export-1", apiimagebuilder.ImageExportConditionReasonPushing)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Run requeue task
			consumer.RunRequeueOnStartup(ctx)

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))

			// Verify that the ImageExport was marked as Failed
			updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, "pushing-export-1")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageExportStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonFailed)))
		})
	})
})

// EnqueuedEvent represents an event that was enqueued during testing
type EnqueuedEvent struct {
	OrgID   uuid.UUID
	Event   v1beta1.Event
	Payload []byte
}
