package tasks

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
)

// containerdiskTemplate is embedded from the templates directory for building containerdisk images
//
//go:embed templates/Containerfile.containerdisk.tmpl
var containerdiskTemplate string

var (
	// errImageBuildNotReady is returned when ImageBuild is not ready yet (pending state)
	errImageBuildNotReady = fmt.Errorf("imageBuild not ready")
)

// exportSource contains the information needed to reference a bootc image for export
type exportSource struct {
	OciRepoSpec *coredomain.OciRepoSpec
	ImageName   string
	ImageTag    string
}

// processImageExport processes an imageExport event by loading the ImageExport resource
// and converting/pushing the image to the target format.
// This method is part of the Consumer type defined in consumer.go.
func (c *Consumer) processImageExport(ctx context.Context, eventWithOrgId worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	event := eventWithOrgId.Event
	orgID := eventWithOrgId.OrgId
	imageExportName := event.InvolvedObject.Name

	log = log.WithField("imageExport", imageExportName).WithField("orgId", orgID)
	log.Info("Processing imageExport event")

	// Load the ImageExport resource from the database
	imageExport, status := c.imageBuilderService.ImageExport().Get(ctx, orgID, imageExportName)
	if imageExport == nil || !imagebuilderapi.IsStatusOK(status) {
		return fmt.Errorf("failed to load ImageExport %q: %v", imageExportName, status)
	}

	log.WithField("spec", imageExport.Spec).Debug("Loaded ImageExport resource")

	// Initialize status if nil
	if imageExport.Status == nil {
		imageExport.Status = &domain.ImageExportStatus{}
	}

	// Check current state - only process if Pending (or has no Ready condition)
	// We only lock resources that are in Pending state to avoid stealing work from other processes
	var readyCondition *domain.ImageExportCondition
	if imageExport.Status.Conditions != nil {
		readyCondition = domain.FindImageExportStatusCondition(*imageExport.Status.Conditions, domain.ImageExportConditionTypeReady)
	}
	if readyCondition != nil {
		reason := readyCondition.Reason
		// Skip if already in terminal state (completed, failed, canceled)
		if reason == string(domain.ImageExportConditionReasonCompleted) ||
			reason == string(domain.ImageExportConditionReasonFailed) ||
			reason == string(domain.ImageExportConditionReasonCanceled) {
			log.Infof("ImageExport %q already in terminal state %q, skipping", imageExportName, reason)
			return nil
		}
		// If Canceling and we haven't started processing yet, complete the cancellation
		// This happens when a Pending export was canceled before the worker picked it up
		if reason == string(domain.ImageExportConditionReasonCanceling) {
			log.Infof("ImageExport %q was canceled before processing started, completing cancellation", imageExportName)
			if err := c.markImageExportAsCanceled(ctx, orgID, imageExport, log); err != nil {
				return fmt.Errorf("failed to mark ImageExport as canceled: %w", err)
			}
			return nil
		}
		// Skip if already Converting - another process is handling it
		if reason == string(domain.ImageExportConditionReasonConverting) {
			log.Infof("ImageExport %q is already being processed (Converting), skipping", imageExportName)
			return nil
		}
		// Skip if Pushing - another process is handling it
		if reason == string(domain.ImageExportConditionReasonPushing) {
			log.Infof("ImageExport %q is already being processed (Pushing), skipping", imageExportName)
			return nil
		}
		// Only proceed if Pending - if it's any other state, skip (shouldn't happen, but defensive)
		if reason != string(domain.ImageExportConditionReasonPending) {
			log.Warnf("ImageExport %q is in unexpected state %q (expected Pending), skipping", imageExportName, reason)
			return nil
		}
	}
	// If no Ready condition exists, treat as Pending and proceed

	// Validate and normalize source to exportSource
	source, err := c.validateAndNormalizeSource(ctx, orgID, imageExport, log)
	if err != nil {
		// Check if this is a pending state (ImageBuild not ready)
		// Resource should already be in Pending state, but update message if different
		if errors.Is(err, errImageBuildNotReady) {
			// Only update if message is different (resource is already Pending)
			if readyCondition == nil || readyCondition.Message != err.Error() {
				updateCondition(ctx, c.imageBuilderService.ImageExport(), orgID, imageExport, domain.ImageExportCondition{
					Type:               domain.ImageExportConditionTypeReady,
					Status:             domain.ConditionStatusFalse,
					Reason:             string(domain.ImageExportConditionReasonPending),
					Message:            err.Error(),
					LastTransitionTime: time.Now().UTC(),
				}, log)
			}
			return nil
		}
		// For other errors, update condition and return error
		updateCondition(ctx, c.imageBuilderService.ImageExport(), orgID, imageExport, domain.ImageExportCondition{
			Type:               domain.ImageExportConditionTypeReady,
			Status:             domain.ConditionStatusFalse,
			Reason:             string(domain.ImageExportConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: time.Now().UTC(),
		}, log)
		return err
	}

	// Lock the export: atomically transition to Converting state using resource_version
	// This ensures only one process can start processing
	now := time.Now().UTC()
	convertingCondition := domain.ImageExportCondition{
		Type:               domain.ImageExportConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageExportConditionReasonConverting),
		Message:            "Export conversion in progress",
		LastTransitionTime: now,
	}

	// Prepare status with Converting condition
	if imageExport.Status == nil {
		imageExport.Status = &domain.ImageExportStatus{}
	}
	if imageExport.Status.Conditions == nil {
		imageExport.Status.Conditions = &[]domain.ImageExportCondition{}
	}
	domain.SetImageExportStatusCondition(imageExport.Status.Conditions, convertingCondition)
	// Set initial lastSeen when locking the resource
	imageExport.Status.LastSeen = &now

	// Synchronously update status to Converting - this will fail if resource_version changed
	_, err = c.imageBuilderService.ImageExport().UpdateStatus(ctx, orgID, imageExport)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			// Another process updated the resource - it's likely already Converting
			log.Infof("ImageExport %q was updated by another process, skipping", imageExportName)
			return nil
		}
		return fmt.Errorf("failed to lock ImageExport for processing: %w", err)
	}

	log.Info("Successfully locked ImageExport for processing (status set to Converting)")

	// Create a cancelable context for the export process
	// The cancel function is passed to statusUpdater which will call it when cancellation is received via Redis
	exportCtx, cancelExport := context.WithCancel(ctx)
	defer cancelExport()

	// Start status updater goroutine - this is the single writer for all status updates
	// It handles both LastSeen (periodic) and condition updates (on-demand)
	// It also listens for cancellation signals via Redis Stream
	// IMPORTANT: Pass the original ctx (not exportCtx) so the updater can complete
	// final status updates (like Canceled) even after exportCtx is canceled
	statusUpdater, cleanupStatusUpdater := startImageExportStatusUpdater(ctx, cancelExport, c.imageBuilderService.ImageExport(), orgID, imageExportName, c.kvStore, c.cfg, log)
	defer cleanupStatusUpdater()

	// Send initial lastSeen update to status updater to ensure it's tracking the current time
	select {
	case statusUpdater.updateChan <- newImageExportStatusUpdateRequest():
	case <-exportCtx.Done():
		if c.handleExportError(ctx, orgID, imageExportName, exportCtx.Err(), statusUpdater, log) {
			return nil // Cancellation handled
		}
		return exportCtx.Err()
	}

	// Execute the export
	outputFilePath, cleanup, err := c.executeExport(exportCtx, orgID, imageExport, source, statusUpdater, log)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		if c.handleExportError(ctx, orgID, imageExportName, err, statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to execute export: %w", err)
	}

	log.WithField("outputFile", outputFilePath).Info("Export output file created")

	// Push artifact to destination (as a referrer to the source image)
	if err := c.pushArtifact(exportCtx, orgID, imageExport, source, outputFilePath, statusUpdater, log); err != nil {
		if c.handleExportError(ctx, orgID, imageExportName, fmt.Errorf("failed to push artifact: %w", err), statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to push artifact: %w", err)
	}

	// Mark as Completed
	now = time.Now().UTC()
	completedCondition := domain.ImageExportCondition{
		Type:               domain.ImageExportConditionTypeReady,
		Status:             domain.ConditionStatusTrue,
		Reason:             string(domain.ImageExportConditionReasonCompleted),
		Message:            "Export completed successfully",
		LastTransitionTime: now,
	}
	statusUpdater.updateCondition(completedCondition)

	log.Info("ImageExport marked as Completed")
	return nil
}

// handleExportError checks if the error was due to cancellation and sets the appropriate status.
// If the error was due to cancellation (status is Canceling), it sets the final status and returns true.
// - For user cancellation: status becomes Canceled
// - For timeout: status becomes Failed (with the timeout message preserved)
// Otherwise, it sets status to Failed and returns false.
func (c *Consumer) handleExportError(ctx context.Context, orgID uuid.UUID, imageExportName string, err error, statusUpdater *imageExportStatusUpdater, log logrus.FieldLogger) bool {
	// Check if the current status is Canceling (set by API or timeout task)
	currentExport, status := c.imageBuilderService.ImageExport().Get(ctx, orgID, imageExportName)
	if currentExport != nil && imagebuilderapi.IsStatusOK(status) &&
		currentExport.Status != nil && currentExport.Status.Conditions != nil {
		readyCondition := domain.FindImageExportStatusCondition(*currentExport.Status.Conditions, domain.ImageExportConditionTypeReady)
		if readyCondition != nil && readyCondition.Reason == string(domain.ImageExportConditionReasonCanceling) {
			// This was a cancellation - check if it was a timeout or user cancellation
			message := readyCondition.Message
			isTimeout := strings.Contains(message, "timed out")

			if isTimeout {
				// Timeout - set Failed status with the timeout message
				log.WithField("message", message).Info("Export timed out, setting status to Failed")
				failedCondition := domain.ImageExportCondition{
					Type:               domain.ImageExportConditionTypeReady,
					Status:             domain.ConditionStatusFalse,
					Reason:             string(domain.ImageExportConditionReasonFailed),
					Message:            message,
					LastTransitionTime: time.Now().UTC(),
				}
				statusUpdater.updateCondition(failedCondition)
			} else {
				// User cancellation - set Canceled status
				if message == "" {
					message = "Export was canceled"
				}
				log.WithField("message", message).Info("Export was canceled, setting status to Canceled")
				canceledCondition := domain.ImageExportCondition{
					Type:               domain.ImageExportConditionTypeReady,
					Status:             domain.ConditionStatusFalse,
					Reason:             string(domain.ImageExportConditionReasonCanceled),
					Message:            message,
					LastTransitionTime: time.Now().UTC(),
				}
				statusUpdater.updateCondition(canceledCondition)
			}
			return true // Cancellation/timeout handled
		}
	}

	// Not canceled - set Failed status
	failedCondition := domain.ImageExportCondition{
		Type:               domain.ImageExportConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageExportConditionReasonFailed),
		Message:            err.Error(),
		LastTransitionTime: time.Now().UTC(),
	}
	statusUpdater.updateCondition(failedCondition)
	return false
}

// progressReader wraps an io.Reader and reports progress as data is read
type progressReader struct {
	reader     io.Reader
	totalBytes int64
	bytesRead  int64
	onProgress func(bytesRead int64, totalBytes int64)
}

// newProgressReader creates a new progress reader that reports progress during reads
func newProgressReader(reader io.Reader, totalBytes int64, onProgress func(bytesRead int64, totalBytes int64)) *progressReader {
	return &progressReader{
		reader:     reader,
		totalBytes: totalBytes,
		onProgress: onProgress,
	}
}

// Read implements io.Reader
func (pr *progressReader) Read(p []byte) (n int, err error) {
	n, err = pr.reader.Read(p)
	pr.bytesRead += int64(n)
	if pr.onProgress != nil {
		pr.onProgress(pr.bytesRead, pr.totalBytes)
	}
	return n, err
}

// vmExportWorker holds information about a running export VM
type vmExportWorker struct {
	Session       *vm.Session
	TmpDir        string
	TmpOutDir     string
	Cleanup       func()
	statusUpdater *imageExportStatusUpdater
}

// statusWriter is a thread-safe writer that captures output to a buffer
type imageExportStatusWriter struct {
	mu            sync.Mutex
	buf           *bytes.Buffer
	statusUpdater *imageExportStatusUpdater
}

// Write implements io.Writer to handle the stream safely
func (w *imageExportStatusWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.buf.Write(p)

	if w.statusUpdater != nil {
		w.statusUpdater.reportOutput(p)
	}
	return len(p), nil
}

// executeExport executes the actual export using bootc-image-builder
// Returns the path to the output file, a cleanup function to delete the temp output dir, and an error
func (c *Consumer) executeExport(
	ctx context.Context,
	orgID uuid.UUID,
	imageExport *domain.ImageExport,
	exportSource *exportSource,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) (string, func(), error) {
	if c.cfg == nil || c.cfg.ImageBuilderWorker == nil || c.cfg.ImageBuilderWorker.VM == nil {
		return "", nil, fmt.Errorf("VM configuration is required")
	}
	if c.cfg.ImageBuilderWorker.VM.Accelerator != "kvm" {
		return "", nil, fmt.Errorf("ImageExport requires KVM")
	}
	// Build image reference string for logging and podman operations
	registryHostname := exportSource.OciRepoSpec.Registry
	bootcImageRef := fmt.Sprintf("%s/%s:%s", registryHostname, exportSource.ImageName, exportSource.ImageTag)

	log.WithField("bootcImage", bootcImageRef).Info("Resolved bootc image reference")

	// Step 2: Start the export VM
	worker, err := c.startBootcImageBuilderContainer(ctx, orgID, imageExport, statusUpdater, log)
	if err != nil {
		return "", nil, fmt.Errorf("failed to start export VM: %w", err)
	}
	defer worker.Cleanup()
	// Create cleanup function to delete the temporary output directory
	cleanup := func() {
		log.WithField("tmpOutDir", worker.TmpOutDir).Debug("Cleaning up temporary output directory")
		if err := os.RemoveAll(worker.TmpOutDir); err != nil {
			log.WithError(err).WithField("tmpOutDir", worker.TmpOutDir).Warn("Failed to remove temporary output directory")
		}
	}

	// Step 2.5: Initialize podman storage inside the container
	// Podman needs to initialize its storage database files, not just the directory structure
	// This must happen before any podman commands (like podman pull) are executed
	if err := c.initializePodmanStorage(ctx, worker, log); err != nil {
		return "", cleanup, fmt.Errorf("failed to initialize podman storage: %w", err)
	}

	// Step 2.6: Install CA certificate for source registry if configured
	if err := installCACertInWorker(ctx, exportSource.OciRepoSpec.CaCrt, worker.Session, registryHostname, log); err != nil {
		return "", cleanup, fmt.Errorf("failed to install CA cert for source registry: %w", err)
	}

	// Step 3: Login to registry if credentials are provided
	if exportSource.OciRepoSpec.OciAuth != nil {
		dockerAuth, err := exportSource.OciRepoSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			decryptedPassword, _, decErr := encryption.Decrypt(ctx, encryption.Ciphertext(dockerAuth.Password))
			if decErr != nil {
				return "", cleanup, fmt.Errorf("failed to decrypt OCI password: %w", decErr)
			}
			if err := c.loginToRegistryForExport(ctx, worker, registryHostname, dockerAuth.Username, string(decryptedPassword), exportSource.OciRepoSpec, log); err != nil {
				return "", cleanup, fmt.Errorf("failed to login to registry: %w", err)
			}
		}
	}

	// Step 4: Pull the source image
	if err := c.pullSourceImage(ctx, worker, bootcImageRef, exportSource.OciRepoSpec, log); err != nil {
		return "", cleanup, fmt.Errorf("failed to pull source image: %w", err)
	}

	// Step 5: Run bootc-image-builder conversion
	if err := c.runBootcImageBuilder(ctx, worker, imageExport.Spec.Format, bootcImageRef, log); err != nil {
		return "", cleanup, fmt.Errorf("failed to run bootc-image-builder: %w", err)
	}

	// Step 6: Find the output file
	outputFilePath, err := c.findOutputFile(worker.TmpOutDir, imageExport.Spec.Format, log)
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to find output file: %w", err)
	}
	relativePath, err := filepath.Rel(worker.TmpOutDir, outputFilePath)
	if err != nil {
		return "", cleanup, err
	}
	outputFilePath, err = worker.Session.VerifyArtifact(ctx, relativePath)
	if err != nil {
		return "", cleanup, err
	}

	// Step 7: For qcow2-disk-container, build the container disk image and save to OCI directory
	if imageExport.Spec.Format == domain.ExportFormatTypeQCOW2DiskContainer {
		ociDirPath, err := c.buildContainerDiskImage(ctx, worker, outputFilePath, log)
		if err != nil {
			return "", cleanup, fmt.Errorf("failed to build container disk image: %w", err)
		}
		outputFilePath = ociDirPath
		log.WithField("ociDir", ociDirPath).Info("Container disk image built and saved to OCI directory")
	}

	if imageExport.Spec.Format == domain.ExportFormatTypeQCOW2DiskContainer {
		if err := verifyGuestDirectory(ctx, worker.Session, worker.TmpOutDir, outputFilePath); err != nil {
			return "", cleanup, err
		}
	}
	if err := worker.Session.Close(); err != nil {
		return "", cleanup, err
	}
	if err := validateGuestOutput(ctx, worker.TmpOutDir); err != nil {
		return "", cleanup, err
	}
	log.WithField("outputFile", outputFilePath).Info("Export completed successfully")
	return outputFilePath, cleanup, nil
}

// startBootcImageBuilderContainer starts an isolated export guest.
func (c *Consumer) startBootcImageBuilderContainer(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, statusUpdater *imageExportStatusUpdater, log logrus.FieldLogger) (*vmExportWorker, error) {
	input, err := os.MkdirTemp("", "imageexport-*")
	if err != nil {
		return nil, err
	}
	output, err := os.MkdirTemp("", "imageexport-out-*")
	if err != nil {
		_ = os.RemoveAll(input)
		return nil, err
	}
	session, err := vm.Start(ctx, *c.cfg.ImageBuilderWorker.VM, input, output)
	if err != nil {
		_ = os.RemoveAll(input)
		_ = os.RemoveAll(output)
		return nil, err
	}
	cleanup := func() { _ = session.Close(); _ = os.RemoveAll(input) }
	if _, err := c.stageGuestAssets(ctx, session, []string{c.cfg.ImageBuilderWorker.EffectiveBootcImageBuilderImage()}); err != nil {
		cleanup()
		_ = os.RemoveAll(output)
		return nil, err
	}
	return &vmExportWorker{Session: session, TmpDir: input, TmpOutDir: output, Cleanup: cleanup, statusUpdater: statusUpdater}, nil
}

func (w *vmExportWorker) run(ctx context.Context, args ...string) error {
	var output bytes.Buffer
	return w.Session.Run(ctx, args, map[string]string{"REGISTRY_AUTH_FILE": containerAuthFile}, nil, &imageExportStatusWriter{buf: &output, statusUpdater: w.statusUpdater})
}

func (c *Consumer) initializePodmanStorage(ctx context.Context, worker *vmExportWorker, log logrus.FieldLogger) error {
	return worker.run(ctx, "podman", "info")
}

func (c *Consumer) loginToRegistryForExport(ctx context.Context, worker *vmExportWorker, registryHostname, username, password string, spec *coredomain.OciRepoSpec, log logrus.FieldLogger) error {
	return c.loginToRegistry(ctx, &podmanWorker{Session: worker.Session}, registryHostname, username, password, containerAuthFile, spec, log)
}

func (c *Consumer) pullSourceImage(ctx context.Context, worker *vmExportWorker, imageRef string, spec *coredomain.OciRepoSpec, log logrus.FieldLogger) error {
	args := []string{"podman", "pull", "--authfile", containerAuthFile}
	if insecureRegistry(spec) {
		args = append(args, "--tls-verify=false")
	}
	return worker.run(ctx, append(args, imageRef)...)
}

// runBootcImageBuilder confines privileged BIB execution and container storage to the guest.
func (c *Consumer) runBootcImageBuilder(ctx context.Context, worker *vmExportWorker, format domain.ExportFormatType, imageRef string, log logrus.FieldLogger) error {
	if format == domain.ExportFormatTypeQCOW2DiskContainer {
		format = domain.ExportFormatTypeQCOW2
	}
	image := c.cfg.ImageBuilderWorker.EffectiveBootcImageBuilderImage()
	pull := []string{"podman", "pull", "--authfile", serviceAuthFile}
	if c.cfg.ImageBuilderWorker.EffectiveBootcImageBuilderSkipTLSVerify() {
		pull = append(pull, "--tls-verify=false")
	}
	if err := worker.run(ctx, append(pull, image)...); err != nil {
		return err
	}
	if err := worker.run(ctx, "mkdir", "-p", "/var/lib/containers/output"); err != nil {
		return err
	}
	args := []string{"podman", "run", "--rm", "--privileged", "--security-opt", "label=type:unconfined_t", "--network", "host",
		"-v", "/var/lib/containers/storage:/var/lib/containers/storage",
		"-v", "/var/lib/containers/output:/output",
		"-v", containerAuthFile + ":" + containerAuthFile + ":ro",
		"-v", "/etc/containers/certs.d:/etc/containers/certs.d:ro",
		"-e", "REGISTRY_AUTH_FILE=" + containerAuthFile,
		image, "build", "--type", string(format), "--rootfs", "xfs", "--progress", "verbose", "--output", "/output", imageRef}
	if err := worker.run(ctx, args...); err != nil {
		return err
	}
	return worker.run(ctx, "cp", "-r", "/var/lib/containers/output/.", "/output")
}

// findOutputFile returns the path to the output file created by bootc-image-builder
// bootc-image-builder creates files at {type}/disk.{type} relative to the working directory
// Since we run with -w /output, files are at /output/{type}/disk.{type} in container
// which maps to {outputDir}/{type}/disk.{type} on the host
// Exception: ISO format uses bootiso/install.iso instead of iso/disk.iso
// Exception: qcow2-disk-container uses qcow2/disk.qcow2 (same as qcow2)
func (c *Consumer) findOutputFile(outputDir string, format domain.ExportFormatType, log logrus.FieldLogger) (string, error) {
	var outputFilePath string
	switch format {
	case domain.ExportFormatTypeISO:
		// ISO format uses bootiso/install.iso instead of iso/disk.iso
		outputFilePath = filepath.Join(outputDir, "bootiso", "install.iso")
	case domain.ExportFormatTypeQCOW2DiskContainer:
		// qcow2-disk-container uses qcow2 output from bootc-image-builder
		outputFilePath = filepath.Join(outputDir, "qcow2", "disk.qcow2")
	default:
		// Other formats (vmdk, qcow2) use {format}/disk.{format}
		outputFilePath = filepath.Join(outputDir, string(format), "disk."+string(format))
	}

	// Verify the file exists
	if _, err := os.Stat(outputFilePath); err != nil {
		return "", fmt.Errorf("output file not found at expected path %q: %w", outputFilePath, err)
	}

	log.WithField("outputFile", outputFilePath).Info("Found output file")
	return outputFilePath, nil
}

// buildContainerDiskImage builds a container image wrapping the qcow2 disk for OpenShift Virt
// and saves it to OCI directory format. Returns the path to the OCI directory.
func (c *Consumer) buildContainerDiskImage(ctx context.Context, worker *vmExportWorker, qcow2Path string, log logrus.FieldLogger) (string, error) {
	if err := writeGuestFile(ctx, worker.Session, "/output/qcow2/Containerfile", []byte(containerdiskTemplate)); err != nil {
		return "", err
	}
	if err := worker.run(ctx, "podman", "build", "--isolation", "chroot", "--network", "host", "-t", "containerdisk:latest", "--build-arg", "DISK_IMAGE_FILE="+filepath.Base(qcow2Path), "-f", "/output/qcow2/Containerfile", "/output/qcow2"); err != nil {
		return "", err
	}
	if err := worker.run(ctx, "podman", "save", "--format", "oci-dir", "-o", "/output/containerdisk-oci", "containerdisk:latest"); err != nil {
		return "", err
	}
	path := filepath.Join(worker.TmpOutDir, "containerdisk-oci")
	if _, err := os.Stat(path); err != nil {
		return "", err
	}
	return path, nil
}

// updateCondition updates the ImageExport condition and status
func updateCondition(
	ctx context.Context,
	imageExportService imagebuilderapi.ImageExportService,
	orgID uuid.UUID,
	imageExport *domain.ImageExport,
	condition domain.ImageExportCondition,
	log logrus.FieldLogger,
) {
	now := time.Now().UTC()
	if imageExport.Status == nil {
		imageExport.Status = &domain.ImageExportStatus{}
	}
	if imageExport.Status.Conditions == nil {
		imageExport.Status.Conditions = &[]domain.ImageExportCondition{}
	}
	domain.SetImageExportStatusCondition(imageExport.Status.Conditions, condition)
	imageExport.Status.LastSeen = &now
	if _, updateErr := imageExportService.UpdateStatus(ctx, orgID, imageExport); updateErr != nil {
		log.WithError(updateErr).Error("failed to update ImageExport status")
	}
}

// validateAndNormalizeSource validates the ImageExport source and returns a normalized exportSource
func (c *Consumer) validateAndNormalizeSource(ctx context.Context, orgID uuid.UUID, imageExport *domain.ImageExport, log logrus.FieldLogger) (*exportSource, error) {
	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine source type: %w", err)
	}

	var repoName string
	var imageName string
	var imageTag string

	switch sourceType {
	case string(domain.ImageExportSourceTypeImageBuild):
		source, err := imageExport.Spec.Source.AsImageBuildRefSource()
		if err != nil {
			return nil, fmt.Errorf("failed to parse imageBuild source: %w", err)
		}

		imageBuild, status := c.imageBuilderService.ImageBuild().Get(ctx, orgID, source.ImageBuildRef, false)
		if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
			return nil, fmt.Errorf("failed to get ImageBuild %q: %v", source.ImageBuildRef, status)
		}

		// Check if ImageBuild is ready
		if !isImageBuildReady(imageBuild) {
			return nil, fmt.Errorf("%w: ImageBuild %q not ready yet", errImageBuildNotReady, source.ImageBuildRef)
		}

		repoName = imageBuild.Spec.Destination.Repository
		imageName = imageBuild.Spec.Destination.ImageName
		imageTag = imageBuild.Spec.Destination.ImageTag

		log.Infof("ImageBuild %q is ready, proceeding with export", source.ImageBuildRef)

	default:
		return nil, fmt.Errorf("unknown source type: %q", sourceType)
	}

	// Get repository and extract OCI spec (common for both source types)
	repo, status := c.repositories.GetRepository(ctx, orgID, repoName)
	if err := statusToErr(status); err != nil {
		return nil, fmt.Errorf("failed to get repository %q: %w", repoName, err)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	return &exportSource{
		OciRepoSpec: &ociSpec,
		ImageName:   imageName,
		ImageTag:    imageTag,
	}, nil
}

// getReferencedDigest resolves the destination image manifest and returns its descriptor.
// If the manifest is a manifest list (multi-arch), it resolves to a platform-specific manifest (default: linux/amd64).
func getReferencedDigest(
	ctx context.Context,
	repoRef *remote.Repository,
	imageTag string,
	destRef string,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) (ocispec.Descriptor, error) {
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Resolving destination image manifest for tag: %s\n", imageTag)))
	destManifestDesc, err := repoRef.Resolve(ctx, imageTag)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("failed to resolve destination image manifest: %w", err)
	}

	// If the resolved descriptor is a manifest list (multi-arch), resolve to a specific platform
	// Default to linux/amd64, but this could be made configurable in the future
	targetPlatform := "linux/amd64"
	if destManifestDesc.MediaType == ocispec.MediaTypeImageIndex {
		log.WithField("mediaType", destManifestDesc.MediaType).Info("Resolved manifest list, finding platform-specific manifest")
		statusUpdater.reportOutput([]byte(fmt.Sprintf("Resolved manifest list, finding platform-specific manifest for %s\n", targetPlatform)))

		// Fetch the manifest list content
		indexReader, err := repoRef.Fetch(ctx, destManifestDesc)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to fetch manifest list: %w", err)
		}
		defer indexReader.Close()

		indexBytes, err := io.ReadAll(indexReader)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to read manifest list: %w", err)
		}

		// Parse the manifest list (image index)
		var index ocispec.Index
		if err := json.Unmarshal(indexBytes, &index); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("failed to parse manifest list: %w", err)
		}

		// Find the manifest for the target platform
		var platformManifest *ocispec.Descriptor
		for _, manifest := range index.Manifests {
			if manifest.Platform != nil {
				platformStr := fmt.Sprintf("%s/%s", manifest.Platform.OS, manifest.Platform.Architecture)
				if platformStr == targetPlatform {
					platformManifest = &manifest
					break
				}
			}
		}

		if platformManifest == nil {
			return ocispec.Descriptor{}, fmt.Errorf("platform %q not found in manifest list", targetPlatform)
		}

		destManifestDesc = *platformManifest
		log.WithFields(logrus.Fields{
			"platform":       targetPlatform,
			"manifestDigest": destManifestDesc.Digest.String(),
		}).Info("Found platform-specific manifest in manifest list")
		statusUpdater.reportOutput([]byte(fmt.Sprintf("Found platform-specific manifest: %s\n", destManifestDesc.Digest.String())))
	}

	log.WithFields(logrus.Fields{
		"subject":       fmt.Sprintf("%s:%s", destRef, imageTag),
		"subjectDigest": destManifestDesc.Digest.String(),
		"mediaType":     destManifestDesc.MediaType,
	}).Info("Resolved destination image manifest for referrer")
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Resolved destination image manifest for tag %s (Digest: %s, MediaType: %s)\n", imageTag, destManifestDesc.Digest.String(), destManifestDesc.MediaType)))

	return destManifestDesc, nil
}

// pushArtifact pushes the exported artifact to the destination registry using oras-go/v2
// as a referrer artifact that references the original source image
func (c *Consumer) pushArtifact(
	ctx context.Context,
	orgID uuid.UUID,
	imageExport *domain.ImageExport,
	exportSource *exportSource,
	artifactPath string,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) error {
	// Get the ImageBuild to use its destination
	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		return fmt.Errorf("failed to determine source type: %w", err)
	}
	if sourceType != string(domain.ImageExportSourceTypeImageBuild) {
		return fmt.Errorf("unexpected source type: %q", sourceType)
	}

	source, err := imageExport.Spec.Source.AsImageBuildRefSource()
	if err != nil {
		return fmt.Errorf("failed to parse imageBuild source: %w", err)
	}

	imageBuild, status := c.imageBuilderService.ImageBuild().Get(ctx, orgID, source.ImageBuildRef, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		return fmt.Errorf("failed to get ImageBuild %q: %v", source.ImageBuildRef, status)
	}

	// Get destination repository for authentication and to get registry hostname
	repo, repoStatus := c.repositories.GetRepository(ctx, orgID, imageBuild.Spec.Destination.Repository)
	if err := statusToErr(repoStatus); err != nil {
		return fmt.Errorf("failed to load destination repository: %w", err)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	// ociSpec.Registry is already the hostname (no scheme)
	destRegistryHostname := ociSpec.Registry

	// With referrers API 1.1, we don't create a separate tag - the artifact is discoverable
	// via the referrers API when querying the original image
	// The destination reference is just the base repository (no tag)
	destRef := fmt.Sprintf("%s/%s", destRegistryHostname, imageBuild.Spec.Destination.ImageName)

	log.WithFields(logrus.Fields{
		"destination": destRef,
		"artifact":    artifactPath,
		"format":      imageExport.Spec.Format,
		"subject":     fmt.Sprintf("%s:%s", destRef, imageBuild.Spec.Destination.ImageTag),
	}).Info("Pushing artifact as referrer to destination")

	// Update condition to Pushing
	pushingTime := time.Now().UTC()
	pushingCondition := domain.ImageExportCondition{
		Type:               domain.ImageExportConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageExportConditionReasonPushing),
		Message:            "Pushing artifact to destination registry",
		LastTransitionTime: pushingTime,
	}
	statusUpdater.updateCondition(pushingCondition)
	statusUpdater.reportOutput([]byte("Starting artifact push to destination registry\n"))

	// Create repository reference (no tag needed - referrer will be discoverable via referrers API)
	repoRef, err := oci.BuildOciRepoRef(ctx, &ociSpec, destRef)
	if err != nil {
		return fmt.Errorf("failed to configure OCI repository reference: %w", err)
	}

	// Skip referrers GC to avoid authentication issues when pushing multiple artifacts
	// When multiple artifacts (e.g., QCOW2 and VMDK) point to the same subject image,
	// ORAS tries to delete old referrer indices which can cause auth failures with some registries
	repoRef.SkipReferrersGC = true

	if repoRef.PlainHTTP {
		log.Debug("Using PlainHTTP for HTTP registry")
	}
	if ociSpec.OciAuth != nil {
		log.Info("Successfully configured authentication for destination registry")
		statusUpdater.reportOutput([]byte("Authenticated with destination registry\n"))
	}

	// Resolve the destination image's manifest to get its digest for the referrer subject
	destImageTag := imageBuild.Spec.Destination.ImageTag
	destManifestDesc, err := getReferencedDigest(ctx, repoRef, destImageTag, destRef, statusUpdater, log)
	if err != nil {
		return err
	}

	// For qcow2-disk-container, push the OCI image with subject field
	if imageExport.Spec.Format == domain.ExportFormatTypeQCOW2DiskContainer {
		return c.pushContainerDiskAsReferrer(ctx, repoRef, artifactPath, destManifestDesc, statusUpdater, log)
	}

	// Determine media type based on format
	mediaType := fmt.Sprintf("application/vnd.%s", string(imageExport.Spec.Format))

	// Open the artifact file for streaming
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Opening artifact file: %s\n", filepath.Base(artifactPath))))
	artifactFile, err := os.Open(artifactPath)
	if err != nil {
		return fmt.Errorf("failed to open artifact file: %w", err)
	}
	defer artifactFile.Close()

	// Stat the file to get its size
	fileInfo, err := artifactFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat artifact file: %w", err)
	}
	fileSize := fileInfo.Size()

	// Compute digest by streaming through a digester
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Computing digest for artifact file (%d bytes)\n", fileSize)))
	digester := digest.Canonical.Digester()
	if _, err := io.Copy(digester.Hash(), artifactFile); err != nil {
		return fmt.Errorf("failed to compute digest: %w", err)
	}
	computedDigest := digester.Digest()

	// Seek the file back to the start for pushing
	if _, err := artifactFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek artifact file to start: %w", err)
	}

	// Build descriptor with MediaTypeImageLayer, Digest and Size
	blobDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageLayer,
		Digest:    computedDigest,
		Size:      fileSize,
	}

	// Create a progress-tracking reader that reports progress during push
	progressReader := newProgressReader(artifactFile, fileSize, func(bytesRead int64, totalBytes int64) {
		percent := float64(bytesRead) / float64(totalBytes) * 100
		statusUpdater.reportOutput([]byte(fmt.Sprintf("Pushing artifact: %d/%d bytes (%.1f%%)\n", bytesRead, totalBytes, percent)))
	})

	// Push the blob using Repository.Push which allows progress tracking
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Starting push of artifact blob (%d bytes) to repository\n", fileSize)))
	if err := repoRef.Push(ctx, blobDesc, progressReader); err != nil {
		return fmt.Errorf("failed to push artifact blob: %w", err)
	}

	statusUpdater.reportOutput([]byte(fmt.Sprintf("Successfully pushed blob: %s\n", blobDesc.Digest.String())))

	// Pack the artifact into a manifest as a referrer to the destination image
	// Using PackManifest with the destination image as subject to create a referrer artifact
	statusUpdater.reportOutput([]byte("Packing artifact as referrer manifest\n"))
	packOpts := oras.PackManifestOptions{
		Subject: &destManifestDesc, // This makes it a referrer to the destination image
		Layers:  []ocispec.Descriptor{blobDesc},
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationTitle: filepath.Base(artifactPath),
		},
	}
	manifestDesc, err := oras.PackManifest(ctx, repoRef, oras.PackManifestVersion1_1, mediaType, packOpts)
	if err != nil {
		return fmt.Errorf("failed to pack artifact manifest: %w", err)
	}
	// Get the referrer manifest digest (this is what oras discover shows)
	referrerManifestDigest := manifestDesc.Digest.String()
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Successfully created referrer manifest: %s\n", referrerManifestDigest)))

	// Get the artifact blob digest (for logging only)
	artifactBlobDigest := blobDesc.Digest.String()

	// Set the referrer manifest digest in the status (this is what oras discover shows)
	statusUpdater.setManifestDigest(referrerManifestDigest)

	log.WithFields(logrus.Fields{
		"destination":    destRef,
		"subject":        fmt.Sprintf("%s:%s", destRef, destImageTag),
		"mediaType":      mediaType,
		"manifestDigest": referrerManifestDigest,
		"artifactDigest": artifactBlobDigest,
		"subjectDigest":  destManifestDesc.Digest.String(),
	}).Info("Successfully pushed referrer artifact (discoverable via referrers API 1.1)")
	statusUpdater.reportOutput([]byte("Successfully pushed referrer artifact (discoverable via referrers API 1.1)\n"))
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Referrer manifest digest: %s\n", referrerManifestDigest)))
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Artifact blob digest: %s\n", artifactBlobDigest)))
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Subject digest: %s\n", destManifestDesc.Digest.String())))

	return nil
}

// pushContainerDiskAsReferrer pushes a container disk image from OCI directory as a referrer
// to the original bootc image. This creates a proper OCI Image Manifest with subject field.
// The manifest is pushed without a tag - it's discoverable only via the referrers API.
func (c *Consumer) pushContainerDiskAsReferrer(
	ctx context.Context,
	repoRef *remote.Repository,
	ociDirPath string,
	subjectDesc ocispec.Descriptor,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) error {
	log.WithFields(logrus.Fields{
		"ociDir":        ociDirPath,
		"subjectDigest": subjectDesc.Digest.String(),
	}).Info("Pushing container disk image as OCI image referrer")
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Pushing container disk image as referrer to %s\n", subjectDesc.Digest.String())))

	// Read index.json from OCI directory
	indexPath := filepath.Join(ociDirPath, "index.json")
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return fmt.Errorf("failed to read OCI index.json: %w", err)
	}

	var index ocispec.Index
	if err := json.Unmarshal(indexData, &index); err != nil {
		return fmt.Errorf("failed to parse OCI index.json: %w", err)
	}

	if len(index.Manifests) == 0 {
		return fmt.Errorf("OCI index.json contains no manifests")
	}

	// Get the first manifest descriptor
	manifestDesc := index.Manifests[0]
	log.WithField("manifestDigest", manifestDesc.Digest.String()).Debug("Found manifest in OCI index")

	// Read the manifest from blobs
	manifestPath := filepath.Join(ociDirPath, "blobs", manifestDesc.Digest.Algorithm().String(), manifestDesc.Digest.Encoded())
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest blob: %w", err)
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return fmt.Errorf("failed to parse manifest: %w", err)
	}

	statusUpdater.reportOutput([]byte(fmt.Sprintf("Found %d layers and config in OCI image\n", len(manifest.Layers))))

	// Push config blob
	log.Debug("Pushing config blob")
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Pushing config blob (%d bytes)...\n", manifest.Config.Size)))
	if err := c.pushBlobFromOCIDirWithProgress(ctx, repoRef, ociDirPath, manifest.Config, "config", statusUpdater); err != nil {
		return fmt.Errorf("failed to push config blob: %w", err)
	}

	// Push layer blobs with progress tracking
	for i, layer := range manifest.Layers {
		log.WithField("layer", i).Debug("Pushing layer blob")
		statusUpdater.reportOutput([]byte(fmt.Sprintf("Pushing layer %d/%d (%d bytes)...\n", i+1, len(manifest.Layers), layer.Size)))
		layerName := fmt.Sprintf("layer %d/%d", i+1, len(manifest.Layers))
		if err := c.pushBlobFromOCIDirWithProgress(ctx, repoRef, ociDirPath, layer, layerName, statusUpdater); err != nil {
			return fmt.Errorf("failed to push layer blob %d: %w", i, err)
		}
	}

	statusUpdater.reportOutput([]byte("All blobs pushed, creating OCI image manifest with subject\n"))

	// Create OCI Image Manifest with subject field using PackManifestVersion1_1
	// When ConfigDescriptor is provided, PackManifestVersion1_1 creates an OCI Image Manifest
	// (not an Artifact Manifest), which is pullable by kubevirt as a container image
	packOpts := oras.PackManifestOptions{
		Subject:          &subjectDesc, // Links to original bootc image for referrers API
		Layers:           manifest.Layers,
		ConfigDescriptor: &manifest.Config, // This makes it an OCI Image Manifest (not Artifact)
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationTitle: "containerdisk",
		},
	}

	// Use PackManifestVersion1_1 which supports subject field
	// Set artifact type for KubeVirt container disk discovery
	const containerDiskArtifactType = "application/vnd.kubevirt.containerdisk"
	newManifestDesc, err := oras.PackManifest(ctx, repoRef, oras.PackManifestVersion1_1, containerDiskArtifactType, packOpts)
	if err != nil {
		return fmt.Errorf("failed to pack OCI image manifest: %w", err)
	}

	referrerManifestDigest := newManifestDesc.Digest.String()
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Successfully created OCI image manifest: %s\n", referrerManifestDigest)))

	// Set the referrer manifest digest in the status
	statusUpdater.setManifestDigest(referrerManifestDigest)

	log.WithFields(logrus.Fields{
		"manifestDigest": referrerManifestDigest,
		"subjectDigest":  subjectDesc.Digest.String(),
	}).Info("Successfully pushed container disk as OCI image referrer (discoverable via referrers API 1.1)")

	statusUpdater.reportOutput([]byte("Successfully pushed container disk as OCI image referrer\n"))
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Referrer manifest digest: %s\n", referrerManifestDigest)))
	statusUpdater.reportOutput([]byte(fmt.Sprintf("Subject digest: %s\n", subjectDesc.Digest.String())))

	return nil
}

// pushBlobFromOCIDirWithProgress pushes a blob from an OCI directory to the remote repository
// with progress tracking similar to artifact push
func (c *Consumer) pushBlobFromOCIDirWithProgress(
	ctx context.Context,
	repoRef *remote.Repository,
	ociDirPath string,
	desc ocispec.Descriptor,
	blobName string,
	statusUpdater *imageExportStatusUpdater,
) error {
	blobPath := filepath.Join(ociDirPath, "blobs", desc.Digest.Algorithm().String(), desc.Digest.Encoded())
	blobFile, err := os.Open(blobPath)
	if err != nil {
		return fmt.Errorf("failed to open blob file: %w", err)
	}
	defer blobFile.Close()

	// Create a progress-tracking reader that reports progress during push
	progressReader := newProgressReader(blobFile, desc.Size, func(bytesRead int64, totalBytes int64) {
		percent := float64(bytesRead) / float64(totalBytes) * 100
		statusUpdater.reportOutput([]byte(fmt.Sprintf("Pushing %s: %d/%d bytes (%.1f%%)\n", blobName, bytesRead, totalBytes, percent)))
	})

	if err := repoRef.Push(ctx, desc, progressReader); err != nil {
		return fmt.Errorf("failed to push blob: %w", err)
	}

	statusUpdater.reportOutput([]byte(fmt.Sprintf("Successfully pushed %s: %s\n", blobName, desc.Digest.String())))
	return nil
}

// isImageBuildReady checks if an ImageBuild is ready (completed with image reference)
func isImageBuildReady(imageBuild *domain.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return false
	}

	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return false
	}

	if readyCondition.Reason != string(domain.ImageBuildConditionReasonCompleted) {
		return false
	}

	if imageBuild.Status.ImageReference == nil || *imageBuild.Status.ImageReference == "" {
		return false
	}

	return true
}
