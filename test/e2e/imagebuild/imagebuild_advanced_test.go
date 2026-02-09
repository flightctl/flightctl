package imagebuild_test

import (
	"fmt"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const maxConcurrent = 5

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild parallel builds and exports", func() {
		It("should run builds and exports in parallel and handle selective cancellation", Label("87341", "imagebuild", "slow"), func() {
			Expect(workerHarness).ToNot(BeNil())
			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()
			registryAddress := workerHarness.RegistryEndpoint()

			numBuilds := 10
			numExports := 10

			buildNames := make([]string, numBuilds)
			exportNames := make([]string, numExports)

			sourceRepoName := fmt.Sprintf("parallel-src-%s", testID)
			destRepoName := fmt.Sprintf("parallel-dest-%s", testID)

			// ============================================================
			// Step 1: Set maxConcurrentBuilds
			// ============================================================

			By(fmt.Sprintf("Step 1: Setting maxConcurrentBuilds to %d", maxConcurrent))
			err := workerHarness.SetMaxConcurrentBuilds(maxConcurrent)
			Expect(err).ToNot(HaveOccurred(), "Should set maxConcurrentBuilds")

			// ============================================================
			// Step 2: Create repositories
			// ============================================================

			By("Step 2: Creating repositories")
			_, err = resources.CreateOCIRepository(workerHarness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())
			_, err = resources.CreateOCIRepository(workerHarness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			// ============================================================
			// Step 3: Create ImageBuilds
			// ============================================================

			By(fmt.Sprintf("Step 3: Creating %d ImageBuilds", numBuilds))
			for i := 0; i < numBuilds; i++ {
				buildName := fmt.Sprintf("parallel-build-%d-%s", i, testID)
				buildNames[i] = buildName
				spec := e2e.NewImageBuildSpec(
					sourceRepoName,
					sourceImageName,
					sourceImageTag,
					destRepoName,
					fmt.Sprintf("parallel-image-%d", i),
					fmt.Sprintf("%s-%d", testID, i),
					imagebuilderapi.BindingTypeLate,
				)
				_, err := workerHarness.CreateImageBuild(buildName, spec)
				Expect(err).ToNot(HaveOccurred(), "Should create ImageBuild %s", buildName)
				GinkgoWriter.Printf("Created ImageBuild %d/%d: %s\n", i+1, numBuilds, buildName)
			}

			// ============================================================
			// Step 4: Wait for maxConcurrent-1 builds to be non-pending
			// ============================================================
			nonPendingRequired := maxConcurrent - 1
			By(fmt.Sprintf("Step 4: Waiting for %d ImageBuilds to move to non-pending state", nonPendingRequired))
			Eventually(func() int {
				return countNonPendingImageBuilds(workerHarness, buildNames)
			}, imageBuildTimeout, pollPeriodLong).Should(BeNumerically(">=", nonPendingRequired),
				"At least %d ImageBuilds should be non-pending", nonPendingRequired)

			// ============================================================
			// Step 5: Cancel all ImageBuilds except one
			// ============================================================
			By("Step 5: Canceling all ImageBuilds except one")
			buildToKeep := findBuildToKeep(workerHarness, buildNames)
			Expect(buildToKeep).ToNot(BeEmpty(), "Should have a build to keep")

			for _, name := range buildNames {
				if name == buildToKeep {
					continue
				}
				reason, _ := workerHarness.GetImageBuildConditionReason(name)
				if reason == string(imagebuilderapi.ImageBuildConditionReasonCompleted) ||
					reason == string(imagebuilderapi.ImageBuildConditionReasonFailed) ||
					reason == string(imagebuilderapi.ImageBuildConditionReasonCanceled) {
					continue
				}
				err := workerHarness.CancelImageBuild(name)
				Expect(err).ToNot(HaveOccurred(), "Should cancel ImageBuild %s", name)
				GinkgoWriter.Printf("Canceled ImageBuild: %s\n", name)
			}

			// ============================================================
			// Step 6: Verify canceled ImageBuilds reached Canceled
			// ============================================================
			By("Step 6: Verifying canceled ImageBuilds reached Canceled state")
			for _, name := range buildNames {
				if name == buildToKeep {
					continue
				}
				Eventually(func() string {
					return getImageBuildConditionReason(workerHarness, name)
				}, cancelTimeout, processingPollPeriod).Should(Equal(string(imagebuilderapi.ImageBuildConditionReasonCanceled)),
					"ImageBuild %s should be Canceled", name)
			}

			// ============================================================
			// Step 7: Wait for the remaining ImageBuild to complete
			// ============================================================
			By("Step 7: Waiting for the remaining ImageBuild to complete (streaming logs)")
			_, err = workerHarness.WaitForImageBuildWithLogs(buildToKeep, imageBuildTimeout)
			Expect(err).ToNot(HaveOccurred(), "ImageBuild %s should complete", buildToKeep)
			completedBuildName := buildToKeep

			// ============================================================
			// Step 8: Create ImageExports
			// ============================================================

			By(fmt.Sprintf("Step 8: Creating %d ImageExports based on completed build %s", numExports, completedBuildName))
			for i := 0; i < numExports; i++ {
				exportName := fmt.Sprintf("parallel-export-%d-%s", i, testID)
				exportNames[i] = exportName
				exportSpec := e2e.NewImageExportSpec(completedBuildName, imagebuilderapi.ExportFormatTypeQCOW2)
				_, err := workerHarness.CreateImageExport(exportName, exportSpec)
				Expect(err).ToNot(HaveOccurred(), "Should create ImageExport %s", exportName)
				GinkgoWriter.Printf("Created ImageExport %d/%d: %s\n", i+1, numExports, exportName)
			}

			// ============================================================
			// Step 9: Wait for maxConcurrent-1 exports to be non-pending
			// ============================================================
			exportNonPendingRequired := maxConcurrent - 1
			By(fmt.Sprintf("Step 9: Waiting for %d ImageExports to move to non-pending state", exportNonPendingRequired))
			Eventually(func() int {
				return countNonPendingImageExports(workerHarness, exportNames)
			}, imageExportTimeout, pollPeriodLong).Should(BeNumerically(">=", exportNonPendingRequired),
				"At least %d ImageExports should be non-pending", exportNonPendingRequired)

			// ============================================================
			// Step 10: Cancel all ImageExports
			// ============================================================
			By("Step 10: Canceling all ImageExports")
			for _, name := range exportNames {
				reason, _ := workerHarness.GetImageExportConditionReason(name)
				if reason == string(imagebuilderapi.ImageExportConditionReasonCompleted) ||
					reason == string(imagebuilderapi.ImageExportConditionReasonFailed) ||
					reason == string(imagebuilderapi.ImageExportConditionReasonCanceled) {
					continue
				}
				err := workerHarness.CancelImageExport(name)
				Expect(err).ToNot(HaveOccurred(), "Should cancel ImageExport %s", name)
				GinkgoWriter.Printf("Canceled ImageExport: %s\n", name)
			}

			// ============================================================
			// Step 11: Verify all ImageExports reached terminal state
			// ============================================================
			By("Step 11: Verifying all ImageExports reached terminal state (Canceled or Completed)")
			for _, name := range exportNames {
				Eventually(func() string {
					return getImageExportConditionReason(workerHarness, name)
				}, cancelTimeout, processingPollPeriod).Should(
					Or(
						Equal(string(imagebuilderapi.ImageExportConditionReasonCanceled)),
						Equal(string(imagebuilderapi.ImageExportConditionReasonCompleted)),
						Equal(string(imagebuilderapi.ImageExportConditionReasonFailed))),
					"ImageExport %s should reach terminal state", name)
			}

			GinkgoWriter.Printf("Parallel builds and exports test passed\n")
		})
	})
})

// --- Helper functions (no Expect calls) ---

func countNonPendingImageBuilds(h *e2e.Harness, buildNames []string) int {
	if h == nil || len(buildNames) == 0 {
		return 0
	}
	count := 0
	for _, name := range buildNames {
		reason, err := h.GetImageBuildConditionReason(name)
		if err != nil {
			GinkgoWriter.Printf("countNonPendingImageBuilds GetImageBuildConditionReason(%s): %v\n", name, err)
			continue
		}
		if reason != string(imagebuilderapi.ImageBuildConditionReasonPending) {
			count++
		}
	}
	return count
}

func countNonPendingImageExports(h *e2e.Harness, exportNames []string) int {
	if h == nil || len(exportNames) == 0 {
		return 0
	}
	count := 0
	for _, name := range exportNames {
		reason, err := h.GetImageExportConditionReason(name)
		if err != nil {
			GinkgoWriter.Printf("countNonPendingImageExports GetImageExportConditionReason(%s): %v\n", name, err)
			continue
		}
		if reason != string(imagebuilderapi.ImageExportConditionReasonPending) {
			count++
		}
	}
	return count
}

func findBuildToKeep(h *e2e.Harness, buildNames []string) string {
	if h == nil || len(buildNames) == 0 {
		GinkgoWriter.Printf("findBuildToKeep: harness nil or empty buildNames\n")
		return ""
	}
	for _, name := range buildNames {
		reason, err := h.GetImageBuildConditionReason(name)
		if err != nil {
			GinkgoWriter.Printf("findBuildToKeep GetImageBuildConditionReason(%s): %v\n", name, err)
			continue
		}
		if reason == string(imagebuilderapi.ImageBuildConditionReasonBuilding) ||
			reason == string(imagebuilderapi.ImageBuildConditionReasonCompleted) ||
			reason == string(imagebuilderapi.ImageBuildConditionReasonPushing) {
			GinkgoWriter.Printf("Keeping in-progress ImageBuild: %s\n", name)
			return name
		}
	}
	return ""
}

func getImageBuildConditionReason(h *e2e.Harness, name string) string {
	if h == nil || name == "" {
		GinkgoWriter.Printf("getImageBuildConditionReason: harness nil or empty name\n")
		return ""
	}
	reason, err := h.GetImageBuildConditionReason(name)
	if err != nil {
		GinkgoWriter.Printf("getImageBuildConditionReason(%s): %v\n", name, err)
		return ""
	}
	return reason
}

func getImageExportConditionReason(h *e2e.Harness, name string) string {
	if h == nil || name == "" {
		GinkgoWriter.Printf("getImageExportConditionReason: harness nil or empty name\n")
		return ""
	}
	reason, err := h.GetImageExportConditionReason(name)
	if err != nil {
		GinkgoWriter.Printf("getImageExportConditionReason(%s): %v\n", name, err)
		return ""
	}
	return reason
}
