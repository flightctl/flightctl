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
	"github.com/flightctl/flightctl/internal/domain"
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

// TestImageBuildUpdateSuite is kept for compatibility but RunSpecs is handled by the package-level suite
func TestImageBuildUpdateSuite(t *testing.T) {
	// RunSpecs is called in containerfile_test.go for the entire package
}

var _ = Describe("ImageBuild Update Integration Tests", func() {
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
		statusValue := v1beta1.ConditionStatusFalse
		if reason == apiimagebuilder.ImageBuildConditionReasonCompleted {
			statusValue = v1beta1.ConditionStatusTrue
		}
		conditions := []apiimagebuilder.ImageBuildCondition{
			{
				Type:               apiimagebuilder.ImageBuildConditionTypeReady,
				Status:             statusValue,
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

	// Helper function to create an ImageExport with specific status that references an ImageBuild
	createImageExportWithImageBuildRef := func(name string, imageBuildName string, reason apiimagebuilder.ImageExportConditionReason) *apiimagebuilder.ImageExport {
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
		_, status := imageBuilderService.ImageExport().Create(ctx, orgID, *imageExport)
		Expect(status.Code).To(Equal(int32(201)))

		// Get the created resource to update its status if needed
		toUpdate, status := imageBuilderService.ImageExport().Get(ctx, orgID, name)
		Expect(status.Code).To(Equal(int32(200)))

		// Set the desired condition if reason is provided
		if reason != "" {
			now := time.Now().UTC()
			statusValue := v1beta1.ConditionStatusFalse
			if reason == apiimagebuilder.ImageExportConditionReasonCompleted {
				statusValue = v1beta1.ConditionStatusTrue
			}
			conditions := []apiimagebuilder.ImageExportCondition{
				{
					Type:               apiimagebuilder.ImageExportConditionTypeReady,
					Status:             statusValue,
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
		}

		// Get the final resource
		updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, name)
		Expect(status.Code).To(Equal(int32(200)))
		return updated
	}

	// Helper function to create a ResourceUpdated event for an ImageBuild
	createImageBuildUpdateEvent := func(imageBuildName string) worker_client.EventWithOrgId {
		event := domain.GetBaseEvent(
			ctx,
			domain.ResourceKind(string(apiimagebuilder.ResourceKindImageBuild)),
			imageBuildName,
			domain.EventReasonResourceUpdated,
			"ImageBuild status was updated successfully.",
			nil,
		)
		return worker_client.EventWithOrgId{
			OrgId: orgID,
			Event: *event,
		}
	}

	Context("ImageBuild Update Handler", func() {
		It("should requeue Pending ImageExports when ImageBuild completes", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-1"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Create ImageExports that reference this ImageBuild
			createImageExportWithImageBuildRef("pending-export-1", imageBuildName, apiimagebuilder.ImageExportConditionReasonPending)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that the Pending ImageExport was requeued
			Expect(len(enqueuedEvents)).To(Equal(1))
			event := enqueuedEvents[0]
			Expect(event.OrgID).To(Equal(orgID))
			Expect(event.Event.InvolvedObject.Kind).To(Equal(string(apiimagebuilder.ResourceKindImageExport)))
			Expect(event.Event.InvolvedObject.Name).To(Equal("pending-export-1"))
			Expect(event.Event.Reason).To(Equal(domain.EventReasonResourceCreated))
		})

		It("should requeue ImageExports with no Ready condition when ImageBuild completes", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-2"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Create ImageExport with no status (no Ready condition)
			createImageExportWithImageBuildRef("no-status-export-1", imageBuildName, "")

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that the ImageExport was requeued
			Expect(len(enqueuedEvents)).To(Equal(1))
			event := enqueuedEvents[0]
			Expect(event.Event.InvolvedObject.Name).To(Equal("no-status-export-1"))
		})

		It("should not requeue Converting ImageExports when ImageBuild completes", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-3"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Create ImageExport that is already Converting
			createImageExportWithImageBuildRef("converting-export-1", imageBuildName, apiimagebuilder.ImageExportConditionReasonConverting)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))
		})

		It("should not requeue Completed ImageExports when ImageBuild completes", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-4"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Create ImageExport that is already Completed
			createImageExportWithImageBuildRef("completed-export-1", imageBuildName, apiimagebuilder.ImageExportConditionReasonCompleted)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))
		})

		It("should not requeue Failed ImageExports when ImageBuild completes", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-5"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Create ImageExport that is already Failed
			createImageExportWithImageBuildRef("failed-export-1", imageBuildName, apiimagebuilder.ImageExportConditionReasonFailed)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that nothing was requeued
			Expect(len(enqueuedEvents)).To(Equal(0))
		})

		It("should not requeue ImageExports when ImageBuild is not completed", func() {
			// Create a Building ImageBuild (not completed)
			imageBuildName := "building-build-1"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonBuilding)

			// Create ImageExport that references this ImageBuild
			createImageExportWithImageBuildRef("pending-export-2", imageBuildName, apiimagebuilder.ImageExportConditionReasonPending)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that nothing was requeued (ImageBuild is not completed)
			Expect(len(enqueuedEvents)).To(Equal(0))
		})

		It("should handle multiple ImageExports correctly", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-6"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Create multiple ImageExports with different states
			createImageExportWithImageBuildRef("pending-export-3", imageBuildName, apiimagebuilder.ImageExportConditionReasonPending)
			createImageExportWithImageBuildRef("no-status-export-2", imageBuildName, "")
			createImageExportWithImageBuildRef("converting-export-2", imageBuildName, apiimagebuilder.ImageExportConditionReasonConverting)
			createImageExportWithImageBuildRef("completed-export-2", imageBuildName, apiimagebuilder.ImageExportConditionReasonCompleted)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that only Pending and no-status ImageExports were requeued
			Expect(len(enqueuedEvents)).To(Equal(2))

			// Check that pending-export-3 and no-status-export-2 were requeued
			requeuedNames := make(map[string]bool)
			for _, event := range enqueuedEvents {
				requeuedNames[event.Event.InvolvedObject.Name] = true
			}
			Expect(requeuedNames["pending-export-3"]).To(BeTrue())
			Expect(requeuedNames["no-status-export-2"]).To(BeTrue())
			Expect(requeuedNames["converting-export-2"]).To(BeFalse())
			Expect(requeuedNames["completed-export-2"]).To(BeFalse())
		})

		It("should handle ImageBuild with no status gracefully", func() {
			// Create an ImageBuild with no status
			imageBuildName := "no-status-build-1"
			imageBuild := &apiimagebuilder.ImageBuild{
				ApiVersion: apiimagebuilder.ImageBuildAPIVersion,
				Kind:       string(apiimagebuilder.ResourceKindImageBuild),
				Metadata: v1beta1.ObjectMeta{
					Name: lo.ToPtr(imageBuildName),
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

			_, status := imageBuilderService.ImageBuild().Create(ctx, orgID, *imageBuild)
			Expect(status.Code).To(Equal(int32(201)))

			// Create ImageExport that references this ImageBuild
			createImageExportWithImageBuildRef("pending-export-4", imageBuildName, apiimagebuilder.ImageExportConditionReasonPending)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that nothing was requeued (ImageBuild has no status)
			Expect(len(enqueuedEvents)).To(Equal(0))
		})

		It("should handle ImageBuild with no ImageExports gracefully", func() {
			// Create a completed ImageBuild
			imageBuildName := "completed-build-7"
			createImageBuildWithStatus(imageBuildName, apiimagebuilder.ImageBuildConditionReasonCompleted)

			// Clear enqueued events
			enqueuedEvents = make([]EnqueuedEvent, 0)

			// Create and handle the update event
			eventWithOrgId := createImageBuildUpdateEvent(imageBuildName)
			err := consumer.HandleImageBuildUpdate(ctx, eventWithOrgId, log)
			Expect(err).ToNot(HaveOccurred())

			// Verify that nothing was requeued (no ImageExports reference this ImageBuild)
			Expect(len(enqueuedEvents)).To(Equal(0))
		})
	})
})
