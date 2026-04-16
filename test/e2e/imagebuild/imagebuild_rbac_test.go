package imagebuild_test

import (
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	flightctlOrgName     = "flightctl"
	defaultOrgName       = "Default"
	operatorUser         = "operator"
	operatorPassword     = "operator"
	viewerUser           = "viewer"
	viewerPassword       = "viewer"
	installerUser        = "installer"
	installerPassword    = "installer"
	rbacSharedNamePrefix = "rbac-shared"
	// imageBuildCancelTimeout is used when waiting for ImageBuild deletion after operator cancel/delete.
	imageBuildCancelTimeout = 2 * time.Minute
)

var _ = Describe("ImageBuild", Label("imagebuild", "slow"), func() {
	Context("ImageBuild RBAC", func() {
		var sourceRepoName, destRepoName string

		BeforeEach(func() {
			Expect(workerHarness).ToNot(BeNil())
			_, err := login.LoginToAPIWithToken(workerHarness)
			Expect(err).ToNot(HaveOccurred(), "Login as admin at start of test")

			orgID, err := workerHarness.GetOrganizationIDByDisplayName(flightctlOrgName)
			Expect(err).ToNot(HaveOccurred(), "Get organization %q", flightctlOrgName)
			err = workerHarness.SetCurrentOrganization(orgID)
			Expect(err).ToNot(HaveOccurred(), "Set organization to %q", flightctlOrgName)

			Expect(workerHarness.ImageBuilderClient).ToNot(BeNil(), "ImageBuilderClient must be available")

			testID := workerHarness.GetTestIDFromContext()
			registryAddress := auxSvcs.Registry.URL

			sourceRepoName = fmt.Sprintf("source-repo-%s", testID)
			destRepoName = fmt.Sprintf("dest-repo-%s", testID)

			By("Creating repositories for ImageBuild RBAC tests")
			_, err = resources.CreateOCIRepository(workerHarness, sourceRepoName, sourceRegistry,
				lo.ToPtr(api.Https), lo.ToPtr(api.Read), false, nil)
			Expect(err).ToNot(HaveOccurred())

			_, err = resources.CreateOCIRepository(workerHarness, destRepoName, registryAddress,
				lo.ToPtr(api.Https), lo.ToPtr(api.ReadWrite), true, nil)
			Expect(err).ToNot(HaveOccurred())
		})

		AfterEach(func() {
			if workerHarness == nil {
				return
			}
			By("Deleting ImageExports, ImageBuilds, and Repositories with this spec's test-id label")
			_, err := login.LoginToAPIWithToken(workerHarness)
			Expect(err).ToNot(HaveOccurred(), "Login as admin in AfterEach")
			orgID, err := workerHarness.GetOrganizationIDByDisplayName(flightctlOrgName)
			Expect(err).ToNot(HaveOccurred(), "Get organization for cleanup")
			Expect(workerHarness.SetCurrentOrganization(orgID)).To(Succeed(), "Set organization for cleanup")
			Expect(workerHarness.CleanUpTestResources(util.ImageExport, util.ImageBuild, util.Repository)).To(Succeed(),
				"delete imagebuilder resources and repos labeled with test-id from harness context")
			defaultOrgID, err := workerHarness.GetOrganizationIDByDisplayName(defaultOrgName)
			if err == nil {
				Expect(workerHarness.SetCurrentOrganization(defaultOrgID)).To(Succeed(), "restore default organization")
			}
		})

		It("roles should follow expected RBAC rules", Label("87344", "imagebuild", "slow"), func() {
			testID := workerHarness.GetTestIDFromContext()

			By("Admin: create shared ImageBuild and ImageExport and wait for export to complete (used for read/download by all roles)")
			sharedBuild, sharedExport, sharedImageBuildSpec, sharedExportSpec, err := workerHarness.AdminProvisionRBACImageBuildExport(
				sourceRepoName, destRepoName, rbacSharedNamePrefix, true,
				sourceImageName, sourceImageTag, destImageName)
			Expect(err).ToNot(HaveOccurred())

			p := setup.GetDefaultProviders()
			if p == nil {
				Skip("ImageBuild RBAC role tests: default providers not set (non-admin sessions unavailable)")
			}
			infra.SkipIfRBACNotSupported(p.RBAC)

			By("Viewer: read-only (including logs per Helm), forbidden mutations and download")
			Expect(login.SwitchToUser(workerHarness, viewerUser, viewerPassword, flightctlOrgName)).To(Succeed())
			Expect(workerHarness.ReadImageBuildExportAccess(sharedBuild, sharedExport, e2e.RbacReadOpts{
				RoleLabel:         "viewer",
				IncludeBuildLogs:  true,
				IncludeExportLogs: true,
			})).To(Succeed())
			Expect(workerHarness.RbacDeniedCancelCreateDelete(
				"viewer", "viewer", testID, sharedBuild, sharedExport, sharedImageBuildSpec, sharedExportSpec, e2e.RbacDownloadForbidden,
			)).To(Succeed())

			By("Installer: read ImageBuild/ImageExport resources; ImageBuild/ImageExport logs forbidden (403); download shared export; forbidden mutations")
			Expect(login.SwitchToUser(workerHarness, installerUser, installerPassword, flightctlOrgName)).To(Succeed())
			Expect(workerHarness.ReadImageBuildExportAccess(sharedBuild, sharedExport, e2e.RbacReadOpts{
				RoleLabel:         "installer",
				IncludeBuildLogs:  false,
				IncludeExportLogs: false,
			})).To(Succeed())
			Expect(workerHarness.RbacDeniedImageBuildExportLogs(sharedBuild, sharedExport, "installer")).To(Succeed())
			Expect(workerHarness.RbacDeniedCancelCreateDelete(
				"installer", "installer", testID, sharedBuild, sharedExport, sharedImageBuildSpec, sharedExportSpec, e2e.RbacDownloadSuccess,
			)).To(Succeed())

			By("Operator: own ImageBuild/ImageExport (cancel/delete without waiting for export completion); download admin shared ImageExport")
			Expect(runOperatorRBACWithSharedDownload(
				workerHarness, sourceRepoName, destRepoName, testID, sharedBuild, sharedExport,
			)).To(Succeed())
		})
	})
})

// runOperatorRBACWithSharedDownload logs in as operator, creates a short-lived build/export (canceled/deleted without
// waiting for export completion), downloads the admin shared export, then restores admin token.
func runOperatorRBACWithSharedDownload(
	h *e2e.Harness,
	sourceRepoName, destRepoName, testID, adminSharedBuild, adminSharedExport string,
) error {
	if err := login.SwitchToUser(h, operatorUser, operatorPassword, flightctlOrgName); err != nil {
		return err
	}
	defer func() {
		_, err := login.LoginToAPIWithToken(h)
		Expect(err).ToNot(HaveOccurred(), "restore admin token after operator RBAC flow")
		orgID, err := h.GetOrganizationIDByDisplayName(flightctlOrgName)
		Expect(err).ToNot(HaveOccurred(), "get org after operator RBAC flow")
		Expect(h.SetCurrentOrganization(orgID)).To(Succeed(), "set org after operator RBAC flow")
	}()

	imageBuildName := fmt.Sprintf("test-build-operator-%s", testID)
	imageExportName := fmt.Sprintf("test-export-operator-%s", testID)
	imageBuildSpec := e2e.NewRBACImageBuildSpec(sourceRepoName, destRepoName, testID, sourceImageName, sourceImageTag, destImageName)

	By("Operator: create/cancel/delete ImageBuild")
	if _, err := h.CreateImageBuild(imageBuildName, imageBuildSpec); err != nil {
		return fmt.Errorf("operator create image build: %w", err)
	}
	if err := h.WaitImageBuilderResourcePhase(e2e.ImageBuilderResourceKindBuild, imageBuildName, e2e.ImageBuilderWaitProcessing, "",
		processingTimeout, processingPollPeriod, ""); err != nil {
		return fmt.Errorf("operator wait for image build processing: %w", err)
	}
	if err := h.CancelImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("operator cancel image build: %w", err)
	}
	wantCanceled := string(imagebuilderapi.ImageBuildConditionReasonCanceled)
	if err := h.WaitImageBuilderResourcePhase(e2e.ImageBuilderResourceKindBuild, imageBuildName, e2e.ImageBuilderWaitConditionReason, wantCanceled,
		cancelTimeout, processingPollPeriod, "Operator: wait for ImageBuild Canceled (first build)"); err != nil {
		return fmt.Errorf("operator image build %s after cancel: %w", imageBuildName, err)
	}

	if err := h.DeleteImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("operator delete image build: %w", err)
	}
	if err := h.WaitImageBuilderResourcePhase(e2e.ImageBuilderResourceKindBuild, imageBuildName, e2e.ImageBuilderWaitDeleted, "",
		imageBuildCancelTimeout, processingPollPeriod, "Operator: wait for ImageBuild deletion (first build)"); err != nil {
		return fmt.Errorf("operator image build %s after delete: %w", imageBuildName, err)
	}

	By("Operator: create ImageBuild (second time)")
	if _, err := h.CreateImageBuild(imageBuildName, imageBuildSpec); err != nil {
		return fmt.Errorf("operator create image build: %w", err)
	}

	By("Operator: Create an image export for the second ImageBuild")
	opExportSpec := e2e.NewImageExportSpec(imageBuildName, imagebuilderapi.ExportFormatTypeQCOW2)

	By("Operator: create ImageExport and cancel immediately (do not wait for worker processing)")
	if _, err := h.CreateImageExport(imageExportName, opExportSpec); err != nil {
		return fmt.Errorf("operator create image export: %w", err)
	}

	if err := h.CancelImageExport(imageExportName); err != nil {
		return fmt.Errorf("operator cancel image export: %w", err)
	}

	if err := h.DeleteImageExport(imageExportName); err != nil {
		return fmt.Errorf("operator delete image export: %w", err)
	}

	if err := h.DeleteImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("operator delete image build: %w", err)
	}
	if err := h.WaitImageBuilderResourcePhase(e2e.ImageBuilderResourceKindBuild, imageBuildName, e2e.ImageBuilderWaitDeleted, "",
		imageBuildCancelTimeout, processingPollPeriod, "Operator: wait for ImageBuild deletion (second build)"); err != nil {
		return fmt.Errorf("operator image build %s after second delete: %w", imageBuildName, err)
	}

	By("Operator: read shared admin ImageBuild/ImageExport")
	if err := h.ReadImageBuildExportAccess(adminSharedBuild, adminSharedExport, e2e.RbacReadOpts{
		RoleLabel:         "operator",
		IncludeBuildLogs:  true,
		IncludeExportLogs: true,
	}); err != nil {
		return err
	}

	By("Operator: download admin shared ImageExport (should succeed)")
	body, contentLength, err := h.DownloadImageExport(adminSharedExport)
	if err != nil {
		return fmt.Errorf("operator download shared image export: %w", err)
	}
	if body == nil {
		return fmt.Errorf("operator download shared image export: nil body")
	}
	defer body.Close()
	if contentLength <= 0 {
		return fmt.Errorf("operator download shared image export: expected positive Content-Length, got %d", contentLength)
	}
	return nil
}
