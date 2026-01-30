package imagebuild_test

import (
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	// Test timeouts
	imageBuildTimeout    = 10 * time.Minute
	processingTimeout    = 2 * time.Minute
	processingPollPeriod = 5 * time.Second

	// Source image (centos-bootc from quay.io)
	sourceRegistry  = "quay.io"
	sourceImageName = "centos-bootc/centos-bootc"
	sourceImageTag  = "stream9"

	// Destination image
	destImageName = "centos-bootc-custom"
)

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild processing", Label("imagebuild", "slow"), func() {
		It("should build and push an image successfully", func() {
			// Get harness directly - no shared package-level variable
			harness := e2e.GetWorkerHarness()
			Expect(harness).ToNot(BeNil())
			Expect(harness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			// Get test ID for unique resource names
			testID := harness.GetTestIDFromContext()
			registryAddress := harness.RegistryEndpoint()

			// Create source OCI repository pointing to quay.io (read-only)
			sourceRepoName := fmt.Sprintf("source-repo-%s", testID)
			_, err := resources.CreateOCIRepository(harness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create destination OCI repository (read-write)
			// Use HTTPS with SkipServerVerification since the e2e registry has certs but may have SAN mismatch
			destRepoName := fmt.Sprintf("dest-repo-%s", testID)
			_, err = resources.CreateOCIRepository(harness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			// Create ImageBuild spec
			imageBuildName := fmt.Sprintf("test-build-%s", testID)
			spec := e2e.NewImageBuildSpec(
				sourceRepoName,  // source repository name
				sourceImageName, // source image name
				sourceImageTag,  // source image tag
				destRepoName,    // destination repository name
				destImageName,   // destination image name
				testID,          // destination image tag (unique per test)
				imagebuilderapi.BindingTypeLate,
			)

			// Create the ImageBuild
			imageBuild, err := harness.CreateImageBuild(imageBuildName, spec)
			Expect(err).ToNot(HaveOccurred())
			Expect(imageBuild).ToNot(BeNil())
			Expect(imageBuild.Metadata.Name).ToNot(BeNil())
			Expect(*imageBuild.Metadata.Name).To(Equal(imageBuildName))

			// Verify the ImageBuild was created
			Expect(harness.ImageBuildExists(imageBuildName)).To(BeTrue())

			// Wait for the build to start processing
			_, err = harness.WaitForImageBuildProcessing(imageBuildName, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred(), "ImageBuild should start processing")

			// Stream logs until build completes or times out
			finalBuild, err := harness.WaitForImageBuildWithLogs(imageBuildName, imageBuildTimeout)
			Expect(err).ToNot(HaveOccurred())
			Expect(finalBuild).ToNot(BeNil())

			// Assert the build completed successfully
			Expect(finalBuild.Status).ToNot(BeNil())
			Expect(finalBuild.Status.Conditions).ToNot(BeNil())

			reason, _ := harness.GetImageBuildConditionReason(imageBuildName)
			Expect(reason).To(Equal(string(imagebuilderapi.ImageBuildConditionReasonCompleted)),
				"Expected ImageBuild to complete successfully")
			Expect(finalBuild.Status.ImageReference).ToNot(BeNil(),
				"Expected built image reference to be set")

			// Verify the image actually exists in the registry
			desc, err := harness.ResolveImage(registryAddress, destImageName, testID)
			Expect(err).ToNot(HaveOccurred(), "Image should exist in registry")
			Expect(desc.Digest).ToNot(BeEmpty(), "Image should have a digest")

			// Cleanup is handled by AfterEach via CleanUpAllTestResources()
		})
	})
})
