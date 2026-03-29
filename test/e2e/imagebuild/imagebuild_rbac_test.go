package imagebuild_test

import (
	"errors"
	"fmt"
	"net/http"
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
	flightctlOrgName        = "flightctl"
	defaultOrgName          = "Default"
	operatorUser            = "operator"
	operatorPassword        = "operator"
	viewerUser              = "viewer"
	viewerPassword          = "viewer"
	installerUser           = "installer"
	installerPassword       = "installer"
	rbacSharedNamePrefix    = "rbac-shared"
	imageBuildCancelTimeout = 2 * time.Minute
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

// imageBuilderResourceKind selects ImageBuild vs ImageExport for wait helpers.
type imageBuilderResourceKind int

const (
	imageBuilderResourceKindBuild imageBuilderResourceKind = iota
	imageBuilderResourceKindExport
)

// imageBuilderWaitPhase selects which terminal state to wait for.
type imageBuilderWaitPhase int

const (
	// imageBuilderWaitProcessing waits until the resource leaves Pending (harness WaitFor*Processing).
	imageBuilderWaitProcessing imageBuilderWaitPhase = iota
	// imageBuilderWaitConditionReason waits until Ready condition reason equals wantReason.
	imageBuilderWaitConditionReason
	// imageBuilderWaitDeleted waits until Get returns HTTP 404 (resource removed).
	imageBuilderWaitDeleted
)

var _ = Describe("ImageBuild", Label("87344", "imagebuild", "slow"), func() {
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
			// Delete all ImageExports, ImageBuilds, and Repositories labeled test-id=<this spec's id> (same as harness.CleanUpTestResources).
			// Admin + flightctl org: required because listing/deleting is org-scoped and tests may leave a non-admin kubeconfig.
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

		It("roles should follow expected RBAC rules", Label("imagebuild", "slow"), func() {
			testID := workerHarness.GetTestIDFromContext()

			By("Admin: create shared ImageBuild and ImageExport and wait for export to complete (used for read/download by all roles)")
			sharedBuild, sharedExport, sharedImageBuildSpec, sharedExportSpec, err := adminProvisionRBACImageBuildExport(
				workerHarness, sourceRepoName, destRepoName, rbacSharedNamePrefix, true)
			Expect(err).ToNot(HaveOccurred())

			// Non-admin role exercises require switching users (viewer/installer/operator). Skip the rest when
			// the environment has no RBAC provider (cannot rely on non-admin sessions).
			p := setup.GetDefaultProviders()
			if p == nil {
				Skip("ImageBuild RBAC role tests: default providers not set (non-admin sessions unavailable)")
			}
			infra.SkipIfRBACNotSupported(p.RBAC)

			By("Viewer: read-only (including logs per Helm), forbidden mutations and download")
			Expect(login.SwitchToUser(workerHarness, viewerUser, viewerPassword, flightctlOrgName)).To(Succeed())
			Expect(readImageBuildExportAccess(workerHarness, sharedBuild, sharedExport, rbacReadOpts{
				roleLabel:         "viewer",
				IncludeBuildLogs:  true,
				IncludeExportLogs: true,
			})).To(Succeed())
			Expect(rbacDeniedCancelCreateDelete(
				workerHarness, "viewer", "viewer", testID, sharedBuild, sharedExport, sharedImageBuildSpec, sharedExportSpec, rbacDownloadForbidden,
			)).To(Succeed())

			By("Installer: read ImageBuild/ImageExport resources; ImageBuild/ImageExport logs forbidden (403); download shared export; forbidden mutations")
			Expect(login.SwitchToUser(workerHarness, installerUser, installerPassword, flightctlOrgName)).To(Succeed())
			Expect(readImageBuildExportAccess(workerHarness, sharedBuild, sharedExport, rbacReadOpts{
				roleLabel:         "installer",
				IncludeBuildLogs:  false,
				IncludeExportLogs: false,
			})).To(Succeed())
			Expect(rbacDeniedImageBuildExportLogs(workerHarness, sharedBuild, sharedExport, "installer")).To(Succeed())
			Expect(rbacDeniedCancelCreateDelete(
				workerHarness, "installer", "installer", testID, sharedBuild, sharedExport, sharedImageBuildSpec, sharedExportSpec, rbacDownloadSuccess,
			)).To(Succeed())

			By("Operator: own ImageBuild/ImageExport (cancel/delete without waiting for export completion); download admin shared ImageExport")
			Expect(runOperatorRBACWithSharedDownload(
				workerHarness, sourceRepoName, destRepoName, testID, sharedBuild, sharedExport,
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

// adminProvisionRBACImageBuildExport creates one ImageBuild and one ImageExport as admin.
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

	if err := waitImageBuilderResourcePhase(h, imageBuilderResourceKindBuild, imageBuildName, imageBuilderWaitProcessing, "",
		processingTimeout, processingPollPeriod, ""); err != nil {
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

// rbacDeniedImageBuildExportLogs asserts GET imagebuilds/{name}/log and imageexports/{name}/log return HTTP 403.
// Installer has get/list on imagebuilds and imageexports but not on imagebuilds/log or imageexports/log.
func rbacDeniedImageBuildExportLogs(h *e2e.Harness, imageBuildName, imageExportName, roleLabel string) error {
	By(fmt.Sprintf("Show ImageBuild logs as %s (should fail with 403 Forbidden)", roleLabel))
	_, logErr := h.GetImageBuildLogs(imageBuildName)
	if err := e2e.ErrUnlessHTTPForbidden(logErr, fmt.Sprintf("Get ImageBuild logs as %s", roleLabel)); err != nil {
		return err
	}
	By(fmt.Sprintf("Show ImageExport logs as %s (should fail with 403 Forbidden)", roleLabel))
	_, logErr = h.GetImageExportLogs(imageExportName)
	if err := e2e.ErrUnlessHTTPForbidden(logErr, fmt.Sprintf("Get ImageExport logs as %s", roleLabel)); err != nil {
		return err
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
	if err := e2e.ErrUnlessHTTPForbidden(h.CancelImageBuild(imageBuildName), fmt.Sprintf("Cancel ImageBuild as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Cancel ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
	if err := e2e.ErrUnlessHTTPForbidden(h.CancelImageExport(imageExportName), fmt.Sprintf("Cancel ImageExport as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Create ImageBuild as %s (should fail with 403 Forbidden)", roleLabel))
	_, err := h.CreateImageBuild(fmt.Sprintf("%s-build-%s", forbiddenNamePrefix, testID), imageBuildSpec)
	if err := e2e.ErrUnlessHTTPForbidden(err, fmt.Sprintf("Create ImageBuild as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Delete ImageBuild as %s (should fail with 403 Forbidden)", roleLabel))
	if err := e2e.ErrUnlessHTTPForbidden(h.DeleteImageBuild(imageBuildName), fmt.Sprintf("Delete ImageBuild as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Create ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
	_, err = h.CreateImageExport(fmt.Sprintf("%s-export-%s", forbiddenNamePrefix, testID), exportSpec)
	if err := e2e.ErrUnlessHTTPForbidden(err, fmt.Sprintf("Create ImageExport as %s", roleLabel)); err != nil {
		return err
	}

	By(fmt.Sprintf("Delete ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
	if err := e2e.ErrUnlessHTTPForbidden(h.DeleteImageExport(imageExportName), fmt.Sprintf("Delete ImageExport as %s", roleLabel)); err != nil {
		return err
	}

	switch download {
	case rbacDownloadForbidden:
		By(fmt.Sprintf("Download ImageExport as %s (should fail with 403 Forbidden)", roleLabel))
		_, _, err = h.DownloadImageExport(imageExportName)
		return e2e.ErrUnlessHTTPForbidden(err, fmt.Sprintf("Download ImageExport as %s", roleLabel))
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
	imageBuildSpec := newRBACImageBuildSpec(sourceRepoName, destRepoName, testID)

	By("Operator: create/cancel/delete ImageBuild")
	if _, err := h.CreateImageBuild(imageBuildName, imageBuildSpec); err != nil {
		return fmt.Errorf("operator create image build: %w", err)
	}
	if err := waitImageBuilderResourcePhase(h, imageBuilderResourceKindBuild, imageBuildName, imageBuilderWaitProcessing, "",
		processingTimeout, processingPollPeriod, ""); err != nil {
		return fmt.Errorf("operator wait for image build processing: %w", err)
	}
	if err := h.CancelImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("operator cancel image build: %w", err)
	}
	wantCanceled := string(imagebuilderapi.ImageBuildConditionReasonCanceled)
	if err := waitImageBuilderResourcePhase(h, imageBuilderResourceKindBuild, imageBuildName, imageBuilderWaitConditionReason, wantCanceled,
		cancelTimeout, processingPollPeriod, "Operator: wait for ImageBuild Canceled (first build)"); err != nil {
		return fmt.Errorf("operator image build %s after cancel: %w", imageBuildName, err)
	}

	if err := h.DeleteImageBuild(imageBuildName); err != nil {
		return fmt.Errorf("operator delete image build: %w", err)
	}
	if err := waitImageBuilderResourcePhase(h, imageBuilderResourceKindBuild, imageBuildName, imageBuilderWaitDeleted, "",
		imageBuildCancelTimeout, processingPollPeriod, "Operator: wait for ImageBuild deletion (first build)"); err != nil {
		return fmt.Errorf("operator image build %s after delete: %w", imageBuildName, err)
	}

	By("Operator: create ImageBuild (second time)")
	if _, err := h.CreateImageBuild(imageBuildName, imageBuildSpec); err != nil {
		return fmt.Errorf("operator create image build: %w", err)
	}

	By("Operator: Create an image export for the second ImageBuild")
	opExportSpec := e2e.NewImageExportSpec(imageBuildName, imagebuilderapi.ExportFormatTypeQCOW2)

	// Cancel without waiting for processing: waiting for "processing" lets the worker leave Pending and can
	// reach pushArtifact (which loads the destination Repository from the main store). That path can fail for
	// the operator flow even when admin succeeded; this test only needs cancel/delete, not a full export.
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
	if err := waitImageBuilderResourcePhase(h, imageBuilderResourceKindBuild, imageBuildName, imageBuilderWaitDeleted, "",
		imageBuildCancelTimeout, processingPollPeriod, "Operator: wait for ImageBuild deletion (second build)"); err != nil {
		return fmt.Errorf("operator image build %s after second delete: %w", imageBuildName, err)
	}

	By("Operator: read shared admin ImageBuild/ImageExport")
	if err := readImageBuildExportAccess(h, adminSharedBuild, adminSharedExport, rbacReadOpts{
		roleLabel:         "operator",
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

// waitImageBuilderResourcePhase polls until the desired processing / condition / deleted state.
// wantReason is used only when phase == imageBuilderWaitConditionReason.
// byStep is optional Ginkgo By text (empty skips By).
func waitImageBuilderResourcePhase(
	h *e2e.Harness,
	kind imageBuilderResourceKind,
	name string,
	phase imageBuilderWaitPhase,
	wantReason string,
	timeout, poll time.Duration,
	byStep string,
) error {
	if byStep != "" {
		By(byStep)
	}
	switch phase {
	case imageBuilderWaitProcessing:
		if kind == imageBuilderResourceKindExport {
			_, err := h.WaitForImageExportProcessing(name, timeout, poll)
			return err
		}
		_, err := h.WaitForImageBuildProcessing(name, timeout, poll)
		return err
	case imageBuilderWaitConditionReason:
		deadline := time.Now().Add(timeout)
		var lastReason string
		for time.Now().Before(deadline) {
			var reason string
			var err error
			if kind == imageBuilderResourceKindExport {
				reason, err = h.GetImageExportConditionReason(name)
			} else {
				reason, err = h.GetImageBuildConditionReason(name)
			}
			if err != nil {
				return fmt.Errorf("get condition reason for %q: %w", name, err)
			}
			lastReason = reason
			if reason == wantReason {
				return nil
			}
			time.Sleep(poll)
		}
		resKind := "ImageBuild"
		if kind == imageBuilderResourceKindExport {
			resKind = "ImageExport"
		}
		return fmt.Errorf("%s %q: expected condition reason %q, last %q", resKind, name, wantReason, lastReason)
	case imageBuilderWaitDeleted:
		resKind := "ImageBuild"
		if kind == imageBuilderResourceKindExport {
			resKind = "ImageExport"
		}
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			var err error
			if kind == imageBuilderResourceKindExport {
				_, err = h.GetImageExport(name)
			} else {
				_, err = h.GetImageBuild(name)
			}
			if err == nil {
				time.Sleep(poll)
				continue
			}
			var apiErr *e2e.APIError
			if errors.As(err, &apiErr) && apiErr.IsStatusCode(http.StatusNotFound) {
				return nil
			}
			return fmt.Errorf("%s %q: wait for delete: %w", resKind, name, err)
		}
		var err error
		if kind == imageBuilderResourceKindExport {
			_, err = h.GetImageExport(name)
		} else {
			_, err = h.GetImageBuild(name)
		}
		if err == nil {
			return fmt.Errorf("%s %q should be deleted", resKind, name)
		}
		var apiErr *e2e.APIError
		if errors.As(err, &apiErr) && apiErr.IsStatusCode(http.StatusNotFound) {
			return nil
		}
		return fmt.Errorf("%s %q: wait for delete: %w", resKind, name, err)
	default:
		return fmt.Errorf("unknown imageBuilderWaitPhase %d", phase)
	}
}
