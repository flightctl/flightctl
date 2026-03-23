package imagebuilder_worker_test

import (
	"context"
	"fmt"
	"os"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/tasks"
	"github.com/flightctl/flightctl/internal/kvstore"
	flightctlstore "github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/testutil"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutilpkg "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("Status Updater Integration Tests", func() {
	var (
		log                 *logrus.Logger
		ctx                 context.Context
		orgID               uuid.UUID
		mainStore           flightctlstore.Store
		imageBuilderStore   imagebuilderstore.Store
		imageBuilderService service.Service
		kvStoreInst         kvstore.KVStore
		cfg                 *config.Config
		dbName              string
		db                  *gorm.DB
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

		// Create required repositories
		_, err = createOCIRepository(ctx, mainStore.Repository(), orgID, testRepoName, "quay.io", nil)
		Expect(err).ToNot(HaveOccurred())
		_, err = createOCIRepository(ctx, mainStore.Repository(), orgID, sourceRepoName, "quay.io", nil)
		Expect(err).ToNot(HaveOccurred())
		outputRepo, err := createOCIRepository(ctx, mainStore.Repository(), orgID, outputRepoName, "quay.io", nil)
		Expect(err).ToNot(HaveOccurred())
		ociSpec, err := outputRepo.Spec.AsOciRepoSpec()
		Expect(err).ToNot(HaveOccurred())
		ociSpec.AccessMode = lo.ToPtr(v1beta1.ReadWrite)
		err = outputRepo.Spec.FromOciRepoSpec(ociSpec)
		Expect(err).ToNot(HaveOccurred())
		_, err = mainStore.Repository().Update(ctx, orgID, outputRepo, flightctlstore.EventCallback(func(context.Context, v1beta1.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {
		}))
		Expect(err).ToNot(HaveOccurred())

		// Setup Redis KVStore (skip test if Redis not available)
		var kvErr error
		kvStoreInst, kvErr = kvstore.NewKVStore(ctx, log, "localhost", 6379, domain.SecureString("adminpass"))
		if kvErr != nil {
			Skip(fmt.Sprintf("Redis not available, skipping test: %v", kvErr))
		}

		// Create imagebuilder service with kvStore
		imageBuilderService = service.NewService(ctx, cfg, imageBuilderStore, mainStore, nil, kvStoreInst, log)
	})

	AfterEach(func() {
		if kvStoreInst != nil {
			// Clean up Redis keys
			_ = kvStoreInst.DeleteAllKeys(ctx)
			kvStoreInst.Close()
		}
		flightctlstore.DeleteTestDB(ctx, log, cfg, mainStore, dbName)
	})

	Context("Log persistence to Redis and DB", func() {
		It("should write logs to Redis during active build", func() {
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			imageBuild := newTestImageBuild(imageBuildName, "late")
			imageBuild.Spec.Source.Repository = sourceRepoName
			imageBuild.Spec.Destination.Repository = outputRepoName

			// Create ImageBuild
			created, status := imageBuilderService.ImageBuild().Create(ctx, orgID, imageBuild)
			Expect(service.IsStatusOK(status)).To(BeTrue())
			Expect(created).ToNot(BeNil())

			// Start status updater
			updater, cleanup := tasks.StartStatusUpdater(
				ctx,
				func() {}, // cancelBuild - no-op for tests
				imageBuilderService.ImageBuild(),
				orgID,
				imageBuildName,
				kvStoreInst,
				&config.Config{
					ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
				},
				log.WithField("test", "status-updater"),
			)
			defer cleanup()

			// Send some log output
			testLogs := []string{
				"Building image...\n",
				"Step 1/5: Pulling base image\n",
				"Step 2/5: Installing packages\n",
			}

			for _, logLine := range testLogs {
				updater.ReportOutput([]byte(logLine))
			}

			expectedKey := fmt.Sprintf("imagebuild:logs:%s:%s", orgID.String(), imageBuildName)
			var entries []kvstore.StreamEntry
			Eventually(func(g Gomega) {
				var err error
				entries, err = kvStoreInst.StreamRange(ctx, expectedKey, "-", "+")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(len(entries)).To(BeNumerically(">=", 3))
			}).WithTimeout(5 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())

			// Verify log content
			allLogs := ""
			for _, entry := range entries {
				allLogs += string(entry.Value)
			}
			Expect(allLogs).To(ContainSubstring("Building image"))
			Expect(allLogs).To(ContainSubstring("Pulling base image"))
			Expect(allLogs).To(ContainSubstring("Installing packages"))
		})

		It("should persist logs to DB when build completes", func() {
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			imageBuild := newTestImageBuild(imageBuildName, "late")
			imageBuild.Spec.Source.Repository = sourceRepoName
			imageBuild.Spec.Destination.Repository = outputRepoName

			// Create ImageBuild
			created, status := imageBuilderService.ImageBuild().Create(ctx, orgID, imageBuild)
			Expect(service.IsStatusOK(status)).To(BeTrue())
			Expect(created).ToNot(BeNil())

			// Start status updater
			updater, cleanup := tasks.StartStatusUpdater(
				ctx,
				func() {}, // cancelBuild - no-op for tests
				imageBuilderService.ImageBuild(),
				orgID,
				imageBuildName,
				kvStoreInst,
				&config.Config{
					ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
				},
				log.WithField("test", "status-updater"),
			)
			defer cleanup()

			// Send log output
			for i := 0; i < 10; i++ {
				updater.ReportOutput([]byte(fmt.Sprintf("Log line %d\n", i)))
			}

			// Completed triggers persistLogsToDB in the same run-loop turn as the update;
			// wait until all chunks are flushed to Redis so the in-memory buffer is full first.
			logStreamKey := fmt.Sprintf("imagebuild:logs:%s:%s", orgID.String(), imageBuildName)
			Eventually(func(g Gomega) {
				entries, err := kvStoreInst.StreamRange(ctx, logStreamKey, "-", "+")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(entries).To(HaveLen(10))
			}).WithTimeout(30 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())

			completedCondition := api.ImageBuildCondition{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageBuildConditionReasonCompleted),
				Message:            "Build completed",
				LastTransitionTime: time.Now().UTC(),
			}
			updater.UpdateCondition(completedCondition)

			var logsStr string
			Eventually(func(g Gomega) {
				var status domain.Status
				_, logsStr, status = imageBuilderService.ImageBuild().GetLogs(ctx, orgID, imageBuildName, false)
				g.Expect(service.IsStatusOK(status)).To(BeTrue())
				g.Expect(logsStr).NotTo(BeEmpty())
				g.Expect(logsStr).To(ContainSubstring("Log line"))
			}).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())
		})

		It("should persist logs periodically during build", func() {
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			imageBuild := newTestImageBuild(imageBuildName, "late")
			imageBuild.Spec.Source.Repository = sourceRepoName
			imageBuild.Spec.Destination.Repository = outputRepoName

			// Create ImageBuild
			created, status := imageBuilderService.ImageBuild().Create(ctx, orgID, imageBuild)
			Expect(service.IsStatusOK(status)).To(BeTrue())
			Expect(created).ToNot(BeNil())

			// Create config with short update interval for testing
			cfg := config.NewDefaultImageBuilderWorkerConfig()
			cfg.LastSeenUpdateInterval = util.Duration(200 * time.Millisecond)

			// Start status updater
			updater, cleanup := tasks.StartStatusUpdater(
				ctx,
				func() {}, // cancelBuild - no-op for tests
				imageBuilderService.ImageBuild(),
				orgID,
				imageBuildName,
				kvStoreInst,
				&config.Config{
					ImageBuilderWorker: cfg,
				},
				log.WithField("test", "status-updater"),
			)
			defer cleanup()

			// Send log output
			updater.ReportOutput([]byte("Periodic test log\n"))

			Eventually(func(g Gomega) {
				_, logsStr, status := imageBuilderService.ImageBuild().GetLogs(ctx, orgID, imageBuildName, false)
				g.Expect(service.IsStatusOK(status)).To(BeTrue())
				g.Expect(logsStr).To(ContainSubstring("Periodic test log"))
			}).WithTimeout(5 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())
		})

		It("should keep only last 500 lines in buffer", func() {
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			imageBuild := newTestImageBuild(imageBuildName, "late")
			imageBuild.Spec.Source.Repository = sourceRepoName
			imageBuild.Spec.Destination.Repository = outputRepoName

			// Create ImageBuild
			created, status := imageBuilderService.ImageBuild().Create(ctx, orgID, imageBuild)
			Expect(service.IsStatusOK(status)).To(BeTrue())
			Expect(created).ToNot(BeNil())

			// Start status updater
			updater, cleanup := tasks.StartStatusUpdater(
				ctx,
				func() {}, // cancelBuild - no-op for tests
				imageBuilderService.ImageBuild(),
				orgID,
				imageBuildName,
				kvStoreInst,
				&config.Config{
					ImageBuilderWorker: config.NewDefaultImageBuilderWorkerConfig(),
				},
				log.WithField("test", "status-updater"),
			)
			defer cleanup()

			// Send 600 log lines
			for i := 0; i < 600; i++ {
				updater.ReportOutput([]byte(fmt.Sprintf("Line %d\n", i)))
			}

			// Terminal status persists logs immediately; the run loop may still be draining
			// outputChan. Wait until every chunk is written to Redis (one stream entry each)
			// before Completed, or persistLogsToDB can run on a partial buffer.
			logStreamKey := fmt.Sprintf("imagebuild:logs:%s:%s", orgID.String(), imageBuildName)
			Eventually(func(g Gomega) {
				entries, err := kvStoreInst.StreamRange(ctx, logStreamKey, "-", "+")
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(entries).To(HaveLen(600))
			}).WithTimeout(30 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())

			completedCondition := api.ImageBuildCondition{
				Type:               api.ImageBuildConditionTypeReady,
				Status:             v1beta1.ConditionStatusTrue,
				Reason:             string(api.ImageBuildConditionReasonCompleted),
				Message:            "Build completed",
				LastTransitionTime: time.Now().UTC(),
			}
			updater.UpdateCondition(completedCondition)

			var logsStr string
			Eventually(func(g Gomega) {
				var status domain.Status
				_, logsStr, status = imageBuilderService.ImageBuild().GetLogs(ctx, orgID, imageBuildName, false)
				g.Expect(service.IsStatusOK(status)).To(BeTrue())
				g.Expect(logsStr).To(ContainSubstring("Line 100"))
				g.Expect(logsStr).To(ContainSubstring("Line 599"))
				g.Expect(logsStr).NotTo(ContainSubstring("Line 99"))
			}).WithTimeout(10 * time.Second).WithPolling(50 * time.Millisecond).Should(Succeed())
		})
	})
})
