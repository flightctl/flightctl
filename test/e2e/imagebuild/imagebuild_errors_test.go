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

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild wrong configurations", Label("87338", "imagebuild"), func() {
		It("should fail with non-existing repository", func() {
			harness := e2e.GetWorkerHarness()
			Expect(harness).ToNot(BeNil())
			Expect(harness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := harness.GetTestIDFromContext()

			By("Step 1: Attempting to create ImageBuild with non-existing source repository")

			imageBuildName := fmt.Sprintf("wrong-config-nonexist-repo-%s", testID)

			// Use non-existing repository names
			spec := e2e.NewImageBuildSpec(
				"nonexistent-source-repo", // non-existing source repository
				sourceImageName,
				sourceImageTag,
				"nonexistent-dest-repo", // non-existing destination repository
				destImageName,
				testID,
				imagebuilderapi.BindingTypeLate,
			)

			// Attempt to create the ImageBuild - should fail validation
			_, err := harness.CreateImageBuild(imageBuildName, spec)

			// The API should reject this with a validation error (400) since repos don't exist
			// OR it will be created but fail during processing
			if err != nil {
				// Good - validation rejected it
				GinkgoWriter.Printf("ImageBuild creation correctly rejected: %v\n", err)
				Expect(err.Error()).To(Or(
					ContainSubstring("400"),
					ContainSubstring("not found"),
					ContainSubstring("repository"),
				), "Error should indicate repository validation failure")
			} else {
				// ImageBuild was created - it should fail during processing
				defer func() {
					_ = harness.DeleteImageBuild(imageBuildName)
				}()

				// Wait for it to fail
				Eventually(func() string {
					reason, _ := harness.GetImageBuildConditionReason(imageBuildName)
					return reason
				}, failureTimeout, processingPollPeriod).Should(
					Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)),
					"ImageBuild should fail due to non-existing repository")

				_, message, _ := harness.GetImageBuildConditionReasonAndMessage(imageBuildName)
				GinkgoWriter.Printf("ImageBuild failed as expected with message: %s\n", message)
				Expect(message).To(ContainSubstring("repository"),
					"Error message should mention repository issue")

				// Cleanup
				err = harness.DeleteImageBuild(imageBuildName)
				Expect(err).ToNot(HaveOccurred())
			}

			GinkgoWriter.Printf("Non-existing repository test passed\n")
		})

		It("should fail with wrong image name/tag for source", func() {
			harness := e2e.GetWorkerHarness()
			Expect(harness).ToNot(BeNil())
			Expect(harness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := harness.GetTestIDFromContext()
			registryAddress := harness.RegistryEndpoint()

			// Create valid repositories first
			sourceRepoName := fmt.Sprintf("wrong-src-repo-%s", testID)
			destRepoName := fmt.Sprintf("wrong-dest-repo-%s", testID)

			defer func() {
				_, _ = resources.Delete(harness, resources.Repositories, destRepoName)
				_, _ = resources.Delete(harness, resources.Repositories, sourceRepoName)
			}()

			_, err := resources.CreateOCIRepository(harness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(harness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Step 2: Creating ImageBuild with non-existent source image name")

			imageBuildName := fmt.Sprintf("wrong-config-bad-image-%s", testID)

			// Use wrong/non-existent image name
			spec := e2e.NewImageBuildSpec(
				sourceRepoName,
				"this-image-does-not-exist/invalid-image", // non-existent image name
				"nonexistent-tag",                         // non-existent tag
				destRepoName,
				destImageName,
				testID,
				imagebuilderapi.BindingTypeLate,
			)

			imageBuild, err := harness.CreateImageBuild(imageBuildName, spec)
			Expect(err).ToNot(HaveOccurred(), "ImageBuild should be created (validation happens at build time)")
			Expect(imageBuild).ToNot(BeNil())

			defer func() {
				_ = harness.DeleteImageBuild(imageBuildName)
			}()

			By("Waiting for ImageBuild to fail due to wrong image name/tag")

			// Wait for it to fail - the build will fail when trying to pull the non-existent image
			Eventually(func() string {
				reason, _ := harness.GetImageBuildConditionReason(imageBuildName)
				GinkgoWriter.Printf("ImageBuild %s state: %s\n", imageBuildName, reason)
				return reason
			}, imageBuildTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)),
				"ImageBuild should fail due to wrong image name/tag")

			// Verify the error message indicates the image issue
			_, message, err := harness.GetImageBuildConditionReasonAndMessage(imageBuildName)
			Expect(err).ToNot(HaveOccurred())
			GinkgoWriter.Printf("ImageBuild failed with message: %s\n", message)

			// The error should indicate something about the image not being found
			// (manifest not found, image pull error, etc.)
			Expect(message).To(Or(
				ContainSubstring("manifest"),
				ContainSubstring("not found"),
				ContainSubstring("pull"),
				ContainSubstring("image"),
			), "Error message should indicate image-related failure")

			GinkgoWriter.Printf("Wrong image name/tag test passed\n")
		})

		It("should fail validation with invalid image reference format", func() {
			harness := e2e.GetWorkerHarness()
			Expect(harness).ToNot(BeNil())
			Expect(harness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := harness.GetTestIDFromContext()
			registryAddress := harness.RegistryEndpoint()

			// Create valid repositories first
			sourceRepoName := fmt.Sprintf("invalid-ref-src-repo-%s", testID)
			destRepoName := fmt.Sprintf("invalid-ref-dest-repo-%s", testID)

			defer func() {
				_, _ = resources.Delete(harness, resources.Repositories, destRepoName)
				_, _ = resources.Delete(harness, resources.Repositories, sourceRepoName)
			}()

			_, err := resources.CreateOCIRepository(harness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(harness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())

			By("Step 3: Attempting to create ImageBuild with invalid image reference format")

			imageBuildName := fmt.Sprintf("wrong-config-invalid-ref-%s", testID)

			// Use invalid characters in image name/tag (should fail validation)
			spec := e2e.NewImageBuildSpec(
				sourceRepoName,
				sourceImageName,
				sourceImageTag,
				destRepoName,
				"INVALID_IMAGE_NAME_WITH_UPPERCASE", // Invalid - image names should be lowercase
				"invalid:tag:format",                // Invalid tag format (contains colon)
				imagebuilderapi.BindingTypeLate,
			)

			// Attempt to create - should fail API validation
			_, err = harness.CreateImageBuild(imageBuildName, spec)

			if err != nil {
				// Good - validation rejected it at API level
				GinkgoWriter.Printf("ImageBuild creation correctly rejected: %v\n", err)
				Expect(err.Error()).To(Or(
					ContainSubstring("400"),
					ContainSubstring("invalid"),
					ContainSubstring("validation"),
				), "Error should indicate validation failure")
			} else {
				// If it was created, it should fail during processing
				defer func() {
					_ = harness.DeleteImageBuild(imageBuildName)
				}()

				Eventually(func() string {
					reason, _ := harness.GetImageBuildConditionReason(imageBuildName)
					return reason
				}, failureTimeout, processingPollPeriod).Should(
					Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)),
					"ImageBuild should fail due to invalid reference format")

				GinkgoWriter.Printf("ImageBuild failed as expected due to invalid format\n")
			}

			GinkgoWriter.Printf("Invalid image reference format test passed\n")
		})

		It("should fail with empty required fields", func() {
			harness := e2e.GetWorkerHarness()
			Expect(harness).ToNot(BeNil())
			Expect(harness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := harness.GetTestIDFromContext()

			By("Step 4: Attempting to create ImageBuild with empty required fields")

			imageBuildName := fmt.Sprintf("wrong-config-empty-fields-%s", testID)

			// Use empty strings for required fields
			spec := e2e.NewImageBuildSpec(
				"", // empty source repository
				"", // empty source image name
				"", // empty source tag
				"", // empty destination repository
				"", // empty destination image name
				"", // empty destination tag
				imagebuilderapi.BindingTypeLate,
			)

			// Attempt to create - should fail API validation
			_, err := harness.CreateImageBuild(imageBuildName, spec)

			// This should definitely fail validation at API level
			Expect(err).To(HaveOccurred(), "ImageBuild creation should fail with empty required fields")
			GinkgoWriter.Printf("ImageBuild creation correctly rejected: %v\n", err)
			Expect(err.Error()).To(Or(
				ContainSubstring("400"),
				ContainSubstring("required"),
				ContainSubstring("empty"),
				ContainSubstring("validation"),
			), "Error should indicate validation failure for empty fields")

			GinkgoWriter.Printf("Empty required fields test passed\n")
		})
	})
})
