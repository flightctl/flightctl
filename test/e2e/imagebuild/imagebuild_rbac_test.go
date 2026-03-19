package imagebuild_test

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	imagebuilderapi "github.com/flightctl/flightctl/api/imagebuilder/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/login"
	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	flightctlOrgName  = "flightctl"
	operatorUser      = "operator"
	operatorPassword  = "operator"
	viewerUser        = "viewer"
	viewerPassword    = "viewer"
	installerUser     = "installer"
	installerPassword = "installer"
)

// rbacReadOpts configures optional log endpoints for read-only RBAC helpers (access only; no content checks).
type rbacReadOpts struct {
	IncludeBuildLogs  bool
	IncludeExportLogs bool
	roleLabel         string
}

type rbacDownloadExpectation int

const (
	rbacDownloadForbidden rbacDownloadExpectation = iota
	rbacDownloadSuccess
)

var _ = Describe("ImageBuild", Label("imagebuild"), func() {
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
			registryAddress := auxSvcs.RegistryHost + ":" + auxSvcs.RegistryPort

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
			_, err := login.LoginToAPIWithToken(workerHarness)
			Expect(err).ToNot(HaveOccurred(), "Login as admin in AfterEach")
			By("Deleting ImageBuild RBAC repositories")
			_ = workerHarness.DeleteRepository(sourceRepoName)
			_ = workerHarness.DeleteRepository(destRepoName)
		})

		It("should allow admin all actions", Label("TBD-1", "imagebuild", "slow"), func() {
			Expect(runImageBuildRBACFlow(workerHarness, sourceRepoName, destRepoName)).To(Succeed())
		})

		It("should allow operator same actions as admin", Label("TBD-2", "imagebuild", "slow"), func() {
			err := login.Login(workerHarness, operatorUser, operatorPassword)
			Expect(err).ToNot(HaveOccurred(), "Login as operator user %q", operatorUser)
			Expect(runImageBuildRBACFlow(workerHarness, sourceRepoName, destRepoName)).To(Succeed())
		})

		It("viewer can get and list but not mutate imagebuild and imageexport", Label("TBD-3", "imagebuild", "slow"), func() {
			testID := workerHarness.GetTestIDFromContext()
			imageBuildName, imageExportName, imageBuildSpec, exportSpec, err := adminProvisionRBACImageBuildExport(
				workerHarness, sourceRepoName, destRepoName, "viewer", false)
			Expect(err).ToNot(HaveOccurred())

			err = switchToRBACUser(workerHarness, viewerUser, viewerPassword)
			Expect(err).ToNot(HaveOccurred(), "Login as viewer user %q", viewerUser)

			Expect(readImageBuildExportAccess(workerHarness, imageBuildName, imageExportName, rbacReadOpts{roleLabel: "viewer"})).To(Succeed())

			Expect(rbacDeniedCancelCreateDelete(
				workerHarness, "viewer", "viewer", testID, imageBuildName, imageExportName, imageBuildSpec, exportSpec, rbacDownloadForbidden,
			)).To(Succeed())
		})

		It("installer can get, list, show logs, and download completed exports but not cancel, create, or delete imagebuild and imageexport", Label("TBD-4", "imagebuild", "slow"), func() {
			testID := workerHarness.GetTestIDFromContext()
			imageBuildName, imageExportName, imageBuildSpec, exportSpec, err := adminProvisionRBACImageBuildExport(
				workerHarness, sourceRepoName, destRepoName, "installer", true)
			Expect(err).ToNot(HaveOccurred())

			err = switchToRBACUser(workerHarness, installerUser, installerPassword)
			Expect(err).ToNot(HaveOccurred(), "Login as installer user %q", installerUser)

			Expect(readImageBuildExportAccess(workerHarness, imageBuildName, imageExportName, rbacReadOpts{
				roleLabel:         "installer",
				IncludeBuildLogs:  true,
				IncludeExportLogs: true,
			})).To(Succeed())

			Expect(rbacDeniedCancelCreateDelete(
				workerHarness, "installer", "installer", testID, imageBuildName, imageExportName, imageBuildSpec, exportSpec, rbacDownloadSuccess,
			)).To(Succeed())
		})
	})
})

func newRBACImageBuildSpec(sourceRepoName, destRepoName, testID string) imagebuilderapi.ImageBuildSpec {
	return e2e.NewImageBuildSpec(
		sourceRepoName,
		sourceImageName,
		sourceImageTag,
		destRepoName,
		destImageName,
		testID,
		imagebuilderapi.BindingTypeEarly,
	)
}

// adminProvisionRBACImageBuildExport creates one ImageBuild and one ImageExport as admin to be used by other roles
// that are not allowed to create or delete imagebuild and imageexport.
func adminProvisionRBACImageBuildExport(h *e2e.Harness, sourceRepoName, destRepoName, namePrefix string, waitExportCompleted bool) (
	imageBuildName, imageExportName string,
	imageBuildSpec imagebuilderapi.ImageBuildSpec,
	exportSpec imagebuilderapi.ImageExportSpec,
	err error,
) {
	testID := h.GetTestIDFromContext()
	imageBuildName = fmt.Sprintf("test-build-%s-%s", namePrefix, testID)
	imageExportName = fmt.Sprintf("test-export-%s-%s", namePrefix, testID)
	imageBuildSpec = newRBACImageBuildSpec(sourceRepoName, destRepoName, testID)

	By("Creating ImageBuild as admin")
	if _, err = h.CreateImageBuild(imageBuildName, imageBuildSpec); err != nil {
		return "", "", imageBuildSpec, exportSpec, fmt.Errorf("create image build: %w", err)
	}

	if _, err = h.WaitForImageBuildProcessing(imageBuildName, processingTimeout, processingPollPeriod); err != nil {
		return "", "", imageBuildSpec, exportSpec, fmt.Errorf("wait for image build processing: %w", err)
	}

	exportSpec = e2e.NewImageExportSpec(imageBuildName, imagebuilderapi.ExportFormatTypeQCOW2)
	By("Creating ImageExport as admin")
	if _, err = h.CreateImageExport(imageExportName, exportSpec); err != nil {
		return "", "", imageBuildSpec, exportSpec, fmt.Errorf("create image export: %w", err)
	}

	if waitExportCompleted {
		By("Waiting for admin ImageExport to complete")
		var finalExport *imagebuilderapi.ImageExport
		var exportStatus string
		finalExport, exportStatus, err = h.WaitForImageExportCompletion(imageExportName, imageExportTimeout)
		if err != nil {
			return imageBuildName, imageExportName, imageBuildSpec, exportSpec, fmt.Errorf("wait for image export completion: %w", err)
		}
		if finalExport == nil {
			return imageBuildName, imageExportName, imageBuildSpec, exportSpec, fmt.Errorf("image export %q: nil resource after completion wait", imageExportName)
		}
		if exportStatus != string(imagebuilderapi.ImageExportConditionReasonCompleted) {
			return imageBuildName, imageExportName, imageBuildSpec, exportSpec, fmt.Errorf(
				"image export %q: expected Completed, got %s", imageExportName, exportStatus)
		}
	}

	return imageBuildName, imageExportName, imageBuildSpec, exportSpec, nil
}

func switchToRBACUser(h *e2e.Harness, username, password string) error {
	orgID, err := h.GetOrganizationIDByDisplayName(flightctlOrgName)
	if err != nil {
		return fmt.Errorf("get organization %q: %w", flightctlOrgName, err)
	}
	if err := login.Login(h, username, password); err != nil {
		return fmt.Errorf("login as %q: %w", username, err)
	}
	if err := h.SetCurrentOrganization(orgID); err != nil {
		return fmt.Errorf("set organization: %w", err)
	}
	return nil
}

func errUnlessHTTPForbidden(err error, desc string) error {
	if err == nil {
		return fmt.Errorf("%s: expected error (HTTP 403 Forbidden), got nil", desc)
	}
	var apiErr *e2e.APIError
	if !errors.As(err, &apiErr) {
		return fmt.Errorf("%s: expected *e2e.APIError, got %T: %v", desc, err, err)
	}
	if apiErr.StatusCode != http.StatusForbidden {
		return fmt.Errorf("%s: expected HTTP 403 Forbidden, got %d", desc, apiErr.StatusCode)
	}
	return nil
}

// readImageBuildExportAccess performs get/list/read logs; returns an error if any step fails expectations.
func readImageBuildExportAccess(h *e2e.Harness, imageBuildName, imageExportName string, opts rbacReadOpts) error {
	suffix := ""
	if opts.roleLabel != "" {
		suffix = fmt.Sprintf(" as %s (should succeed)", opts.roleLabel)
	}

	By("Get ImageBuild" + suffix)
	build, err := h.GetImageBuild(imageBuildName)
	if err != nil {
		return fmt.Errorf("get image build: %w", err)
	}
	if build == nil {
		return fmt.Errorf("get image build: nil resource")
	}

	By("Get ImageExport" + suffix)
	export, err := h.GetImageExport(imageExportName)
	if err != nil {
		return fmt.Errorf("get image export: %w", err)
	}
	if export == nil {
		return fmt.Errorf("get image export: nil resource")
	}

	By("List ImageBuilds" + suffix)
	if _, err = h.ListImageBuilds(nil); err != nil {
		return fmt.Errorf("list image builds: %w", err)
	}

	By("List ImageExports" + suffix)
	if _, err = h.ListImageExports(nil); err != nil {
		return fmt.Errorf("list image exports: %w", err)
	}

	if opts.IncludeBuildLogs {
		By("Show ImageBuild logs" + suffix)
		if _, err := h.GetImageBuildLogs(imageBuildName); err != nil {
			return fmt.Errorf("get image build logs: %w", err)
		}
	}

	if opts.IncludeExportLogs {
		By("Show ImageExport logs" + suffix)
		if _, err := h.GetImageExportLogs(imageExportName); err != nil {
			return fmt.Errorf("get image export logs: %w", err)
		}
	}

	return nil
}

func rbacDeniedCancelCreateDelete(
	h *e2e.Harness,
	roleLabel, forbiddenNamePrefix, testID string,
	imageBuildName, imageExportName string,
	imageBuildSpec imagebuilderapi.ImageBuildSpec,
	exportSpec imagebuilderapi.ImageExportSpec,
	download rbacDownloadExpectation,
) error {
	By(fmt.Sprintf("Cancel ImageBuild as %s (should fail with 403 Forbidden)", roleLabel))
	if err := errUnlessHTTPForbidden(h.CancelImageBuild(imageBuildName), fmt.Sprintf("Cancel ImageBuild as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Cancel ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
	if err := errUnlessHTTPForbidden(h.CancelImageExport(imageExportName), fmt.Sprintf("Cancel ImageExport as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Create ImageBuild as %s (should fail with 403 Forbidden)", roleLabel))
	_, err := h.CreateImageBuild(fmt.Sprintf("%s-build-%s", forbiddenNamePrefix, testID), imageBuildSpec)
	if err := errUnlessHTTPForbidden(err, fmt.Sprintf("Create ImageBuild as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Delete ImageBuild as %s (should fail with 403 Forbidden)", roleLabel))
	if err := errUnlessHTTPForbidden(h.DeleteImageBuild(imageBuildName), fmt.Sprintf("Delete ImageBuild as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Create ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
	_, err = h.CreateImageExport(fmt.Sprintf("%s-export-%s", forbiddenNamePrefix, testID), exportSpec)
	if err := errUnlessHTTPForbidden(err, fmt.Sprintf("Create ImageExport as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Delete ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
	if err := errUnlessHTTPForbidden(h.DeleteImageExport(imageExportName), fmt.Sprintf("Delete ImageExport as %s", roleLabel)); err != nil {
		return err
	}

	switch download {
	case rbacDownloadForbidden:
		By(fmt.Sprintf("Download ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
		_, _, err = h.DownloadImageExport(imageExportName)
		return errUnlessHTTPForbidden(err, fmt.Sprintf("Download ImageExport as %s", roleLabel))
	case rbacDownloadSuccess:
		By(fmt.Sprintf("Download ImageExport as %s (should succeed; installer role grants imageexports/download)", roleLabel))
		body, contentLength, err := h.DownloadImageExport(imageExportName)
		if err != nil {
			return fmt.Errorf("download image export: %w", err)
		}
		if body == nil {
			return fmt.Errorf("download image export: nil body")
		}
		defer body.Close()
		if contentLength <= 0 {
			return fmt.Errorf("download image export: expected positive Content-Length, got %d", contentLength)
		}
		return nil
	default:
		return fmt.Errorf("unknown download expectation")
	}
}

func runImageBuildRBACFlow(h *e2e.Harness, sourceRepoName, destRepoName string) error {
	testID := h.GetTestIDFromContext()
	imageBuildName := fmt.Sprintf("test-build-%s", testID)
	imageBuildAltName := fmt.Sprintf("test-build-alt-%s", testID)
	imageExportName := fmt.Sprintf("test-export-qcow2-%s", testID)
	imageExportAltName := fmt.Sprintf("test-export-qcow2-alt-%s", testID)

	By("Creating ImageBuild")
	imageBuildSpec := newRBACImageBuildSpec(sourceRepoName, destRepoName, testID)

	if _, err := h.CreateImageBuild(imageBuildName, imageBuildSpec); err != nil {
		return fmt.Errorf("create image build: %w", err)
	}

	if _, err := h.WaitForImageBuildProcessing(imageBuildName, processingTimeout, processingPollPeriod); err != nil {
		return fmt.Errorf("wait for image build processing: %w", err)
	}

	if err := h.CancelImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("cancel image build: %w", err)
	}

	By("Verifying ImageBuild reached Canceled state")
	wantCanceled := string(imagebuilderapi.ImageBuildConditionReasonCanceled)
	deadline := time.Now().Add(cancelTimeout)
	var lastReason string
	canceled := false
	for time.Now().Before(deadline) {
		reason, _ := h.GetImageBuildConditionReason(imageBuildName)
		lastReason = reason
		if reason == wantCanceled {
			canceled = true
			break
		}
		time.Sleep(processingPollPeriod)
	}
	if !canceled {
		return fmt.Errorf("image build %s should be Canceled, last condition reason %q", imageBuildName, lastReason)
	}

	finalBuild, err := h.WaitForImageBuildWithLogs(imageBuildName, imageBuildTimeout)
	if err != nil {
		return fmt.Errorf("wait for image build with logs: %w", err)
	}
	if finalBuild == nil {
		return fmt.Errorf("wait for image build with logs: nil build")
	}

	if err := h.DeleteImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("delete image build: %w", err)
	}

	if _, err := h.CreateImageBuild(imageBuildAltName, imageBuildSpec); err != nil {
		return fmt.Errorf("create alt image build: %w", err)
	}

	defer func() { _ = h.DeleteImageBuild(imageBuildAltName) }()

	if _, err := h.WaitForImageBuildProcessing(imageBuildAltName, processingTimeout, processingPollPeriod); err != nil {
		return fmt.Errorf("wait for alt image build processing: %w", err)
	}
	reason, _ := h.GetImageBuildConditionReason(imageBuildAltName)
	okReason := reason == string(imagebuilderapi.ImageBuildConditionReasonBuilding) ||
		reason == string(imagebuilderapi.ImageBuildConditionReasonPushing) ||
		reason == string(imagebuilderapi.ImageBuildConditionReasonCompleted)
	if !okReason {
		return fmt.Errorf("alt image build %s: expected Building, Pushing, or Completed, got %s", imageBuildAltName, reason)
	}

	exportSpec := e2e.NewImageExportSpec(imageBuildAltName, imagebuilderapi.ExportFormatTypeQCOW2)

	if _, err := h.CreateImageExport(imageExportName, exportSpec); err != nil {
		return fmt.Errorf("create image export: %w", err)
	}

	if _, err := h.WaitForImageExportProcessing(imageExportName, testutil.LONG_TIMEOUT, processingPollPeriod); err != nil {
		return fmt.Errorf("wait for image export processing: %w", err)
	}

	if err := h.CancelImageExport(imageExportName); err != nil {
		return fmt.Errorf("cancel image export: %w", err)
	}

	if err := h.DeleteImageExport(imageExportName); err != nil {
		return fmt.Errorf("delete image export: %w", err)
	}

	if _, err := h.CreateImageExport(imageExportAltName, exportSpec); err != nil {
		return fmt.Errorf("create alt image export: %w", err)
	}

	finalExport, exportStatus, err := h.WaitForImageExportCompletion(imageExportAltName, imageExportTimeout)
	if err != nil {
		return fmt.Errorf("wait for image export completion: %w", err)
	}
	if finalExport == nil {
		return fmt.Errorf("alt image export completion: nil export")
	}
	if exportStatus != string(imagebuilderapi.ImageExportConditionReasonCompleted) {
		return fmt.Errorf("alt image export: expected Completed, got %s", exportStatus)
	}

	body, contentLength, err := h.DownloadImageExport(imageExportAltName)
	if err != nil {
		return fmt.Errorf("download image export: %w", err)
	}
	if body == nil {
		return fmt.Errorf("download image export: nil body")
	}
	defer body.Close()
	if contentLength <= 0 {
		return fmt.Errorf("download image export: expected positive Content-Length, got %d", contentLength)
	}

	return readImageBuildExportAccess(h, imageBuildAltName, imageExportAltName, rbacReadOpts{
		IncludeBuildLogs:  true,
		IncludeExportLogs: true,
	})
}
