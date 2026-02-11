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

// Error substrings used in wrong-configuration tests
const (
	errSubstrBadRequest = "400"
	errSubstrNotFound   = "not found"
	errSubstrRepository = "repository"
	errSubstrValidation = "validation"
	errSubstrInvalid    = "invalid"
	errSubstrRequired   = "required"
	errSubstrEmpty      = "empty"
	errSubstrManifest   = "manifest"
	errSubstrPull       = "pull"
	errSubstrImage      = "image"
)

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild wrong configurations", func() {
		It("should fail with non-existing repository", Label("87338", "imagebuild"), func() {
			Expect(workerHarness).ToNot(BeNil())
			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()

			By("Step 1: Attempting to create ImageBuild with non-existing source repository")

			imageBuildName := fmt.Sprintf("wrong-config-nonexist-repo-%s", testID)

			spec := e2e.NewImageBuildSpec(
				"nonexistent-source-repo",
				sourceImageName,
				sourceImageTag,
				"nonexistent-dest-repo",
				destImageName,
				testID,
				imagebuilderapi.BindingTypeLate,
			)

			_, err := workerHarness.CreateImageBuild(imageBuildName, spec)

			Expect(err).To(HaveOccurred(), "ImageBuild creation should fail immediately with 400 for non-existing repository")
			GinkgoWriter.Printf("ImageBuild creation correctly rejected: %v\n", err)
			Expect(err.Error()).To(Or(
				ContainSubstring(errSubstrBadRequest),
				ContainSubstring(errSubstrNotFound),
				ContainSubstring(errSubstrRepository),
			), "Error should indicate repository validation failure (400)")

			GinkgoWriter.Printf("Non-existing repository test passed\n")
		})

		It("should fail with wrong image name/tag for source", Label("87706", "imagebuild"), func() {
			Expect(workerHarness).ToNot(BeNil())
			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()
			registryAddress := workerHarness.RegistryEndpoint()

			sourceRepoName := fmt.Sprintf("wrong-src-repo-%s", testID)
			destRepoName := fmt.Sprintf("wrong-dest-repo-%s", testID)

			defer func() {
				_, _ = resources.Delete(workerHarness, resources.Repositories, destRepoName)
				_, _ = resources.Delete(workerHarness, resources.Repositories, sourceRepoName)
			}()

			_, err := resources.CreateOCIRepository(workerHarness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(workerHarness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Step 2: Creating ImageBuild with non-existent source image name")

			imageBuildName := fmt.Sprintf("wrong-config-bad-image-%s", testID)

			spec := e2e.NewImageBuildSpec(
				sourceRepoName,
				"this-image-does-not-exist/invalid-image",
				"nonexistent-tag",
				destRepoName,
				destImageName,
				testID,
				imagebuilderapi.BindingTypeLate,
			)

			imageBuild, err := workerHarness.CreateImageBuild(imageBuildName, spec)
			Expect(err).ToNot(HaveOccurred(), "ImageBuild should be created (validation happens at build time)")
			Expect(imageBuild).ToNot(BeNil())
			Expect(imageBuild.Metadata).ToNot(BeNil())
			Expect(imageBuild.Metadata.Name).ToNot(BeNil())
			Expect(*imageBuild.Metadata.Name).To(Equal(imageBuildName))

			defer func() {
				_ = workerHarness.DeleteImageBuild(imageBuildName)
			}()

			By("Waiting for ImageBuild to fail due to wrong image name/tag")

			Eventually(func() string {
				return getImageBuildConditionReason(workerHarness, imageBuildName)
			}, imageBuildTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)),
				"ImageBuild should fail due to wrong image name/tag")

			_, message, err := workerHarness.GetImageBuildConditionReasonAndMessage(imageBuildName)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("ImageBuild failed with message: %s\n", message)

			Expect(message).To(Or(
				ContainSubstring(errSubstrManifest),
				ContainSubstring(errSubstrNotFound),
				ContainSubstring(errSubstrPull),
				ContainSubstring(errSubstrImage),
			), "Error message should indicate image-related failure")

			GinkgoWriter.Printf("Wrong image name/tag test passed\n")
		})

		It("should fail validation with invalid image reference format", Label("87705", "imagebuild"), func() {
			Expect(workerHarness).ToNot(BeNil())
			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()
			registryAddress := workerHarness.RegistryEndpoint()

			sourceRepoName := fmt.Sprintf("invalid-ref-src-repo-%s", testID)
			destRepoName := fmt.Sprintf("invalid-ref-dest-repo-%s", testID)

			defer func() {
				_, _ = resources.Delete(workerHarness, resources.Repositories, destRepoName)
				_, _ = resources.Delete(workerHarness, resources.Repositories, sourceRepoName)
			}()

			_, err := resources.CreateOCIRepository(workerHarness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(workerHarness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Step 3: Attempting to create ImageBuild with invalid image reference format")

			imageBuildName := fmt.Sprintf("wrong-config-invalid-ref-%s", testID)

			spec := e2e.NewImageBuildSpec(
				sourceRepoName,
				sourceImageName,
				sourceImageTag,
				destRepoName,
				"INVALID_IMAGE_NAME_WITH_UPPERCASE",
				"invalid:tag:format",
				imagebuilderapi.BindingTypeLate,
			)

			_, err = workerHarness.CreateImageBuild(imageBuildName, spec)

			if err != nil {
				GinkgoWriter.Printf("ImageBuild creation correctly rejected: %v\n", err)
				Expect(err.Error()).To(Or(
					ContainSubstring(errSubstrBadRequest),
					ContainSubstring(errSubstrInvalid),
					ContainSubstring(errSubstrValidation),
				), "Error should indicate validation failure")
			} else {
				defer func() {
					_ = workerHarness.DeleteImageBuild(imageBuildName)
				}()

				Eventually(func() string {
					return getImageBuildConditionReason(workerHarness, imageBuildName)
				}, failureTimeout, processingPollPeriod).Should(
					Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)),
					"ImageBuild should fail due to invalid reference format")

				GinkgoWriter.Printf("ImageBuild failed as expected due to invalid format\n")
			}

			GinkgoWriter.Printf("Invalid image reference format test passed\n")
		})

		It("should fail with empty required fields", Label("87708", "imagebuild"), func() {
			Expect(workerHarness).ToNot(BeNil())
			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()

			emptyFieldCases := []struct {
				desc                               string
				sourceRepo, sourceImage, sourceTag string
				destRepo, destImage, destTag       string
			}{
				{"all fields empty", "", "", "", "", "", ""},
				{"empty source repository", "", sourceImageName, sourceImageTag, "dest-repo", destImageName, testID},
				{"empty source image name", "src-repo", "", sourceImageTag, "dest-repo", destImageName, testID},
				{"empty source tag", "src-repo", sourceImageName, "", "dest-repo", destImageName, testID},
				{"empty destination repository", "src-repo", sourceImageName, sourceImageTag, "", destImageName, testID},
				{"empty destination image name", "src-repo", sourceImageName, sourceImageTag, "dest-repo", "", testID},
				{"empty destination tag", "src-repo", sourceImageName, sourceImageTag, "dest-repo", destImageName, ""},
			}
			for i, tc := range emptyFieldCases {
				By(tc.desc)
				imageBuildName := fmt.Sprintf("wrong-config-empty-fields-%d-%s", i, testID)
				spec := e2e.NewImageBuildSpec(
					tc.sourceRepo, tc.sourceImage, tc.sourceTag,
					tc.destRepo, tc.destImage, tc.destTag,
					imagebuilderapi.BindingTypeLate,
				)
				_, err := workerHarness.CreateImageBuild(imageBuildName, spec)
				Expect(err).To(HaveOccurred(), "ImageBuild creation should fail for %s", tc.desc)
				Expect(err.Error()).To(Or(
					ContainSubstring(errSubstrBadRequest),
					ContainSubstring(errSubstrRequired),
					ContainSubstring(errSubstrEmpty),
					ContainSubstring(errSubstrValidation),
				), "Error should indicate validation failure for %s", tc.desc)
			}
		})
	})
})
