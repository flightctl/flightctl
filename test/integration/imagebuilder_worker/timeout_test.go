package imagebuilder_worker_test

import (
	"context"
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
	"github.com/flightctl/flightctl/internal/kvstore"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/testutil"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// TestTimeoutSuite is kept for compatibility but RunSpecs is handled by the package-level suite
func TestTimeoutSuite(t *testing.T) {
	// RunSpecs is called in containerfile_test.go for the entire package
}

var _ = Describe("Timeout Check Integration Tests", func() {
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
		kvStoreInst         kvstore.KVStore
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

		// Create kvStore for Redis operations
		var kvErr error
		kvStoreInst, kvErr = kvstore.NewKVStore(ctx, log, "localhost", 6379, domain.SecureString("adminpass"))
		Expect(kvErr).ToNot(HaveOccurred())

		// Create config with defaults
		cfg = config.NewDefault()

		// Create imagebuilder service
		imageBuilderService = service.NewService(ctx, cfg, imageBuilderStore, mainStore, nil, kvStoreInst, log)

		// Create consumer
		consumer = tasks.NewConsumer(
			imageBuilderStore,
			mainStore,
			kvStoreInst,
			nil, // serviceHandler
			imageBuilderService,
			nil, // queueProducer
			cfg,
			log,
		)
	})

	AfterEach(func() {
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStore, dbName)
	})

	// Helper function to create an ImageBuild with specific status
	createImageBuildWithStatus := func(name string, reason apiimagebuilder.ImageBuildConditionReason, lastSeen time.Time) *apiimagebuilder.ImageBuild {
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
			LastSeen:   &lastSeen,
		}

		// Update status
		_, err := imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, toUpdate)
		Expect(err).ToNot(HaveOccurred())

		// Update lastSeen separately
		err = imageBuilderStore.ImageBuild().UpdateLastSeen(ctx, orgID, name, lastSeen)
		Expect(err).ToNot(HaveOccurred())

		// Get the final resource
		updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, name, false)
		Expect(status.Code).To(Equal(int32(200)))
		return updated
	}

	// Helper function to create an ImageExport with specific status
	createImageExportWithStatus := func(name string, reason apiimagebuilder.ImageExportConditionReason, lastSeen time.Time) *apiimagebuilder.ImageExport {
		// First create an ImageBuild that the ImageExport will reference
		imageBuildName := "build-for-" + name
		imageBuild := &apiimagebuilder.ImageBuild{
			ApiVersion: apiimagebuilder.ImageBuildAPIVersion,
			Kind:       string(apiimagebuilder.ResourceKindImageBuild),
			Metadata: v1beta1.ObjectMeta{
				Name: lo.ToPtr(imageBuildName),
			},
			Spec: apiimagebuilder.ImageBuildSpec{
				Source: apiimagebuilder.ImageBuildSource{
					Repository: sourceRepoName,
					ImageName:  "source-image",
					ImageTag:   "v1.0.0",
				},
				Destination: apiimagebuilder.ImageBuildDestination{
					Repository: outputRepoName,
					ImageName:  "output-image",
					ImageTag:   "v1.0.0",
				},
				Binding: apiimagebuilder.ImageBuildBinding{},
			},
		}
		_ = imageBuild.Spec.Binding.FromLateBinding(apiimagebuilder.LateBinding{
			Type: apiimagebuilder.Late,
		})
		_, createStatus := imageBuilderService.ImageBuild().Create(ctx, orgID, *imageBuild)
		Expect(createStatus.Code).To(Equal(int32(201)))

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
			LastSeen:   &lastSeen,
		}

		// Update status
		_, err = imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, toUpdate)
		Expect(err).ToNot(HaveOccurred())

		// Update lastSeen separately
		err = imageBuilderStore.ImageExport().UpdateLastSeen(ctx, orgID, name, lastSeen)
		Expect(err).ToNot(HaveOccurred())

		// Get the final resource
		updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, name)
		Expect(status.Code).To(Equal(int32(200)))
		return updated
	}

	Context("Timeout Check for ImageBuilds", func() {
		It("should mark timed-out Building ImageBuild as Canceling", func() {
			timeoutDuration := 5 * time.Minute
			oldLastSeen := time.Now().UTC().Add(-10 * time.Minute)

			// Create an ImageBuild in Building state with old lastSeen
			createImageBuildWithStatus("timeout-build-1", apiimagebuilder.ImageBuildConditionReasonBuilding, oldLastSeen)

			// Verify initial state
			build, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "timeout-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*build.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonBuilding)))

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(1))

			// Verify it was marked as Canceling (graceful cancellation initiated)
			// The worker will transition to Failed when it processes the error
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "timeout-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition = apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonCanceling)))
			Expect(readyCondition.Message).To(ContainSubstring("timed out"))
		})

		It("should not mark recent Building ImageBuild", func() {
			timeoutDuration := 5 * time.Minute
			recentLastSeen := time.Now().UTC().Add(-1 * time.Minute)

			// Create an ImageBuild in Building state with recent lastSeen
			createImageBuildWithStatus("recent-build-1", apiimagebuilder.ImageBuildConditionReasonBuilding, recentLastSeen)

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(0))

			// Verify it was NOT marked as Failed
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "recent-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonBuilding)))
		})

		It("should not mark Pending ImageBuild (excluded by field selector)", func() {
			timeoutDuration := 5 * time.Minute
			oldLastSeen := time.Now().UTC().Add(-10 * time.Minute)

			// Create an ImageBuild in Pending state with old lastSeen
			createImageBuildWithStatus("pending-build-1", apiimagebuilder.ImageBuildConditionReasonPending, oldLastSeen)

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(0))

			// Verify it was NOT marked as Failed (Pending is excluded)
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "pending-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonPending)))
		})

		It("should mark timed-out Pushing ImageBuild as Canceling", func() {
			timeoutDuration := 5 * time.Minute
			oldLastSeen := time.Now().UTC().Add(-10 * time.Minute)

			// Create an ImageBuild in Pushing state with old lastSeen
			createImageBuildWithStatus("pushing-build-1", apiimagebuilder.ImageBuildConditionReasonPushing, oldLastSeen)

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(1))

			// Verify it was marked as Canceling (graceful cancellation initiated)
			updated, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "pushing-build-1", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonCanceling)))
		})
	})

	Context("Timeout Check for ImageExports", func() {
		It("should mark timed-out Converting ImageExport as Canceling", func() {
			timeoutDuration := 5 * time.Minute
			oldLastSeen := time.Now().UTC().Add(-10 * time.Minute)

			// Create an ImageExport in Converting state with old lastSeen
			createImageExportWithStatus("timeout-export-1", apiimagebuilder.ImageExportConditionReasonConverting, oldLastSeen)

			// Verify initial state
			export, status := imageBuilderService.ImageExport().Get(ctx, orgID, "timeout-export-1")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageExportStatusCondition(*export.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonConverting)))

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(1))

			// Verify it was marked as Canceling (graceful cancellation initiated)
			// The worker will transition to Failed when it processes the error
			updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, "timeout-export-1")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition = apiimagebuilder.FindImageExportStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonCanceling)))
			Expect(readyCondition.Message).To(ContainSubstring("timed out"))
		})

		It("should not mark recent Converting ImageExport", func() {
			timeoutDuration := 5 * time.Minute
			recentLastSeen := time.Now().UTC().Add(-1 * time.Minute)

			// Create an ImageExport in Converting state with recent lastSeen
			createImageExportWithStatus("recent-export-1", apiimagebuilder.ImageExportConditionReasonConverting, recentLastSeen)

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(0))

			// Verify it was NOT marked as Failed
			updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, "recent-export-1")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageExportStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonConverting)))
		})

		It("should not mark Pending ImageExport (excluded by field selector)", func() {
			timeoutDuration := 5 * time.Minute
			oldLastSeen := time.Now().UTC().Add(-10 * time.Minute)

			// Create an ImageExport in Pending state with old lastSeen
			createImageExportWithStatus("pending-export-1", apiimagebuilder.ImageExportConditionReasonPending, oldLastSeen)

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(0))

			// Verify it was NOT marked as Failed (Pending is excluded)
			updated, status := imageBuilderService.ImageExport().Get(ctx, orgID, "pending-export-1")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageExportStatusCondition(*updated.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition).ToNot(BeNil())
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonPending)))
		})
	})

	Context("Timeout Check with Multiple Resources", func() {
		It("should mark multiple timed-out resources as Canceling", func() {
			timeoutDuration := 5 * time.Minute
			oldLastSeen := time.Now().UTC().Add(-10 * time.Minute)
			recentLastSeen := time.Now().UTC().Add(-1 * time.Minute)

			// Create multiple resources with different states
			createImageBuildWithStatus("timeout-build-2", apiimagebuilder.ImageBuildConditionReasonBuilding, oldLastSeen)
			createImageBuildWithStatus("recent-build-2", apiimagebuilder.ImageBuildConditionReasonBuilding, recentLastSeen)
			createImageBuildWithStatus("pending-build-2", apiimagebuilder.ImageBuildConditionReasonPending, oldLastSeen)
			createImageExportWithStatus("timeout-export-2", apiimagebuilder.ImageExportConditionReasonConverting, oldLastSeen)
			createImageExportWithStatus("recent-export-2", apiimagebuilder.ImageExportConditionReasonConverting, recentLastSeen)

			// Run timeout check
			failedCount, err := consumer.CheckAndMarkTimeoutsForOrg(ctx, orgID, timeoutDuration, log)
			Expect(err).ToNot(HaveOccurred())
			Expect(failedCount).To(Equal(2))

			// Verify timeout-build-2 was marked as Canceling (graceful cancellation initiated)
			build1, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "timeout-build-2", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition := apiimagebuilder.FindImageBuildStatusCondition(*build1.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonCanceling)))

			// Verify recent-build-2 was NOT marked as Canceling
			build2, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "recent-build-2", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition = apiimagebuilder.FindImageBuildStatusCondition(*build2.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonBuilding)))

			// Verify pending-build-2 was NOT marked as Canceling
			build3, status := imageBuilderService.ImageBuild().Get(ctx, orgID, "pending-build-2", false)
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition = apiimagebuilder.FindImageBuildStatusCondition(*build3.Status.Conditions, apiimagebuilder.ImageBuildConditionTypeReady)
			Expect(readyCondition.Reason).To(Equal(string(apiimagebuilder.ImageBuildConditionReasonPending)))

			// Verify timeout-export-2 was marked as Canceling (graceful cancellation initiated)
			export1, status := imageBuilderService.ImageExport().Get(ctx, orgID, "timeout-export-2")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition2 := apiimagebuilder.FindImageExportStatusCondition(*export1.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition2.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonCanceling)))

			// Verify recent-export-2 was NOT marked as Canceling
			export2, status := imageBuilderService.ImageExport().Get(ctx, orgID, "recent-export-2")
			Expect(status.Code).To(Equal(int32(200)))
			readyCondition2 = apiimagebuilder.FindImageExportStatusCondition(*export2.Status.Conditions, apiimagebuilder.ImageExportConditionTypeReady)
			Expect(readyCondition2.Reason).To(Equal(string(apiimagebuilder.ImageExportConditionReasonConverting)))
		})
	})
})
