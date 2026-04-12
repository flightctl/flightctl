package imagebuild_test

import (
	"errors"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"sigs.k8s.io/yaml"
)

// negAPIResourceCleanup tracks names for Context AfterEach cleanup.
type negAPIResourceCleanup struct {
	sourceRepo, destRepo, imageBuild, imageExport string
}

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
	Context("ImageBuild and ImageExport negative API behavior", func() {
		var negCleanup negAPIResourceCleanup

		AfterEach(func() {
			h := workerHarness
			if h == nil {
				return
			}
			if negCleanup.imageExport != "" {
				_ = h.DeleteImageExport(negCleanup.imageExport)
			}
			if negCleanup.imageBuild != "" {
				_ = h.DeleteImageBuild(negCleanup.imageBuild)
			}
			if negCleanup.destRepo != "" {
				_, _ = resources.Delete(h, resources.Repositories, negCleanup.destRepo)
			}
			if negCleanup.sourceRepo != "" {
				_, _ = resources.Delete(h, resources.Repositories, negCleanup.sourceRepo)
			}
			negCleanup = negAPIResourceCleanup{}
		})

		It("should fail ImageBuild after creation when destination push cannot authenticate", Label("88399", "imagebuild", "slow"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src := createQuaySourceRepository(workerHarness, testID, &negCleanup)
			dst := createAuthenticatedRegistryDestRepository(workerHarness, testID, &negCleanup, "wrong-password-"+testID)
			ib := negImageBuildName(testID)

			spec := e2e.NewImageBuildSpec(src, sourceImageName, sourceImageTag, dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			Eventually(func() string {
				return getImageBuildConditionReason(workerHarness, ib)
			}, imageBuildTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)),
				"ImageBuild should fail at push due to bad registry credentials")
		})

		It("should reject ImageExport create when imageBuildRef does not exist", Label("88400", "imagebuild"), func() {
			testID := workerHarness.GetTestIDFromContext()
			missingRef := fmt.Sprintf("missing-ib-%s", testID)
			spec := e2e.NewImageExportSpec(missingRef, imagebuilderapi.ExportFormatTypeQCOW2)
			_, err := workerHarness.CreateImageExport(negImageExportName(testID), spec)
			Expect(err).To(HaveOccurred())
			var apiErr *e2e.APIError
			Expect(errors.As(err, &apiErr)).To(BeTrue())
			Expect(apiErr.StatusCode).To(Equal(http.StatusBadRequest))
			Expect(apiErr.Status).ToNot(BeNil())
			Expect(apiErr.Status.Message).To(ContainSubstring("not found"))
		})

		It("should reject ImageExport download before export reaches Completed", Label("88401", "imagebuild"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src, dst := createStandardImageBuildRepositories(workerHarness, testID, &negCleanup)
			ib, ie := negImageBuildName(testID), negImageExportName(testID)

			spec := e2e.NewImageBuildSpec(src, sourceImageName, sourceImageTag, dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			exportSpec := e2e.NewImageExportSpec(ib, imagebuilderapi.ExportFormatTypeQCOW2)
			_, err = workerHarness.CreateImageExport(ie, exportSpec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageExport = ie

			buildReason, err := workerHarness.GetImageBuildConditionReason(ib)
			Expect(err).ToNot(HaveOccurred())
			Expect(buildReason).ToNot(Equal(string(imagebuilderapi.ImageBuildConditionReasonCompleted)),
				"ImageBuild must not be Completed yet so download is exercised before the export can finish")

			_, _, err = workerHarness.DownloadImageExport(ie)
			Expect(err).To(HaveOccurred())
			var apiErr *e2e.APIError
			Expect(errors.As(err, &apiErr)).To(BeTrue())
			Expect(apiErr.StatusCode).To(Equal(http.StatusBadRequest))
		})

		It("should reject cancel when ImageBuild is Failed", Label("88402", "imagebuild"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src, dst := createStandardImageBuildRepositories(workerHarness, testID, &negCleanup)
			ib := negImageBuildName(testID)

			spec := e2e.NewImageBuildSpec(src, "this-image-does-not-exist/invalid-image", "nonexistent-tag", dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			Eventually(func() string {
				return getImageBuildConditionReason(workerHarness, ib)
			}, imageBuildTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageBuildConditionReasonFailed)))

			expectCancelImageBuildConflict(workerHarness, ib)
		})

		It("should reject cancel when ImageBuild is Canceled", Label("imagebuild"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src, dst := createStandardImageBuildRepositories(workerHarness, testID, &negCleanup)
			ib := negImageBuildName(testID)

			spec := e2e.NewImageBuildSpec(src, sourceImageName, sourceImageTag, dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			Expect(workerHarness.CancelImageBuild(ib)).To(Succeed())

			Eventually(func() string {
				return getImageBuildConditionReason(workerHarness, ib)
			}, cancelTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageBuildConditionReasonCanceled)))

			expectCancelImageBuildConflict(workerHarness, ib)
		})

		It("should reject cancel when ImageBuild is Completed", Label("88404", "imagebuild", "slow"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src, dst := createStandardImageBuildRepositories(workerHarness, testID, &negCleanup)
			ib := negImageBuildName(testID)

			spec := e2e.NewImageBuildSpec(src, sourceImageName, sourceImageTag, dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			_, err = workerHarness.WaitForImageBuildProcessing(ib, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred())

			_, err = workerHarness.WaitForImageBuildWithLogs(ib, imageBuildTimeout)
			Expect(err).ToNot(HaveOccurred())

			reason, err := workerHarness.GetImageBuildConditionReason(ib)
			Expect(err).ToNot(HaveOccurred())
			Expect(reason).To(Equal(string(imagebuilderapi.ImageBuildConditionReasonCompleted)))

			expectCancelImageBuildConflict(workerHarness, ib)
		})

		It("should reject second cancel when ImageExport is already Canceled", Label("88403", "imagebuild"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src, dst := createStandardImageBuildRepositories(workerHarness, testID, &negCleanup)
			ib, ie := negImageBuildName(testID), negImageExportName(testID)

			spec := e2e.NewImageBuildSpec(src, sourceImageName, sourceImageTag, dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			exportSpec := e2e.NewImageExportSpec(ib, imagebuilderapi.ExportFormatTypeQCOW2)
			_, err = workerHarness.CreateImageExport(ie, exportSpec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageExport = ie

			Expect(workerHarness.CancelImageExport(ie)).To(Succeed())

			Eventually(func() string {
				return getImageExportConditionReason(workerHarness, ie)
			}, cancelTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageExportConditionReasonCanceled)))

			expectCancelImageExportConflict(workerHarness, ie)
		})

		It("should reject cancel when ImageExport is Failed", Label("88405", "imagebuild"), func() {
			testID := workerHarness.GetTestIDFromContext()
			src, dst := createStandardImageBuildRepositories(workerHarness, testID, &negCleanup)
			ib, ie := negImageBuildName(testID), negImageExportName(testID)

			spec := e2e.NewImageBuildSpec(src, sourceImageName, sourceImageTag, dst, destImageName, testID, imagebuilderapi.BindingTypeLate)
			_, err := workerHarness.CreateImageBuild(ib, spec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageBuild = ib

			_, err = workerHarness.WaitForImageBuildProcessing(ib, processingTimeout, processingPollPeriod)
			Expect(err).ToNot(HaveOccurred())

			_, err = workerHarness.WaitForImageBuildWithLogs(ib, imageBuildTimeout)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.Delete(workerHarness, resources.Repositories, dst)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.destRepo = ""

			exportSpec := e2e.NewImageExportSpec(ib, imagebuilderapi.ExportFormatTypeQCOW2)
			_, err = workerHarness.CreateImageExport(ie, exportSpec)
			Expect(err).ToNot(HaveOccurred())
			negCleanup.imageExport = ie

			Eventually(func() string {
				return getImageExportConditionReason(workerHarness, ie)
			}, imageExportTimeout, processingPollPeriod).Should(
				Equal(string(imagebuilderapi.ImageExportConditionReasonFailed)))

			expectCancelImageExportConflict(workerHarness, ie)
		})
	})
})

func negImageBuildName(testID string) string {
	return fmt.Sprintf("ib-%s", testID)
}

func negImageExportName(testID string) string {
	return fmt.Sprintf("ie-%s", testID)
}

func createStandardImageBuildRepositories(h *e2e.Harness, testID string, c *negAPIResourceCleanup) (sourceRepoName, destRepoName string) {
	sourceRepoName = createQuaySourceRepository(h, testID, c)
	destRepoName = fmt.Sprintf("repo-dst-%s", testID)
	registryAddress := auxSvcs.Registry.Host + ":" + auxSvcs.Registry.Port
	_, err := resources.CreateOCIRepository(h, destRepoName, registryAddress,
		lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
	Expect(err).ToNot(HaveOccurred())
	c.destRepo = destRepoName
	return sourceRepoName, destRepoName
}

func createQuaySourceRepository(h *e2e.Harness, testID string, c *negAPIResourceCleanup) string {
	name := fmt.Sprintf("repo-src-%s", testID)
	_, err := resources.CreateOCIRepository(h, name, sourceRegistry,
		lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
	Expect(err).ToNot(HaveOccurred())
	c.sourceRepo = name
	return name
}

func createAuthenticatedRegistryDestRepository(h *e2e.Harness, testID string, c *negAPIResourceCleanup, password string) string {
	name := fmt.Sprintf("repo-dst-auth-%s", testID)
	err := applyOCIRepositoryWithDockerAuth(
		h,
		name,
		auxSvcs.Registry.Authenticated.HostPort,
		lo.ToPtr(api.Https),
		lo.ToPtr(api.ReadWrite),
		true,
		auxSvcs.Registry.Authenticated.Username,
		password,
	)
	Expect(err).ToNot(HaveOccurred())
	c.destRepo = name
	return name
}

func applyOCIRepositoryWithDockerAuth(
	h *e2e.Harness,
	name, registry string,
	scheme *api.OciRepoSpecScheme,
	accessMode *api.OciRepoSpecAccessMode,
	skipTLSVerify bool,
	username, password string,
) error {
	ociSpec := api.OciRepoSpec{
		Registry:   registry,
		Type:       api.OciRepoSpecTypeOci,
		Scheme:     scheme,
		AccessMode: accessMode,
	}
	if skipTLSVerify {
		ociSpec.SkipServerVerification = lo.ToPtr(true)
	}
	if username != "" || password != "" {
		ociAuth := api.OciAuth{}
		if err := ociAuth.FromDockerAuth(api.DockerAuth{
			AuthType: api.Docker,
			Username: username,
			Password: password,
		}); err != nil {
			return fmt.Errorf("docker auth: %w", err)
		}
		ociSpec.OciAuth = &ociAuth
	}
	spec := api.RepositorySpec{}
	if err := spec.FromOciRepoSpec(ociSpec); err != nil {
		return err
	}
	repository := &api.Repository{
		ApiVersion: api.RepositoryAPIVersion,
		Kind:       api.RepositoryKind,
		Metadata:   api.ObjectMeta{Name: &name},
		Spec:       spec,
	}
	h.SetLabelsForRepositoryMetadata(&repository.Metadata, map[string]string{})
	yamlStr, err := yaml.Marshal(repository)
	if err != nil {
		return fmt.Errorf("marshal repository: %w", err)
	}
	_, err = h.CLIWithStdin(string(yamlStr), "apply", "-f", "-")
	return err
}

func expectCancelImageBuildConflict(h *e2e.Harness, name string) {
	err := h.CancelImageBuild(name)
	Expect(err).To(HaveOccurred(), "cancel should fail for non-cancelable ImageBuild %q", name)
	var apiErr *e2e.APIError
	Expect(errors.As(err, &apiErr)).To(BeTrue(), "error should be APIError")
	Expect(apiErr.StatusCode).To(Equal(http.StatusConflict))
	Expect(apiErr.Status).ToNot(BeNil())
	Expect(apiErr.Status.Message).To(ContainSubstring("not in a cancelable state"))
}

func expectCancelImageExportConflict(h *e2e.Harness, name string) {
	err := h.CancelImageExport(name)
	Expect(err).To(HaveOccurred(), "cancel should fail for non-cancelable ImageExport %q", name)
	var apiErr *e2e.APIError
	Expect(errors.As(err, &apiErr)).To(BeTrue(), "error should be APIError")
	Expect(apiErr.StatusCode).To(Equal(http.StatusConflict))
	Expect(apiErr.Status).ToNot(BeNil())
	Expect(apiErr.Status.Message).To(ContainSubstring("not in a cancelable state"))
}
