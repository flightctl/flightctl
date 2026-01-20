package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

var (
	// errImageBuildNotReady is returned when ImageBuild is not ready yet (pending state)
	errImageBuildNotReady = fmt.Errorf("imageBuild not ready")
)

// exportSource contains the information needed to reference a bootc image for export
type exportSource struct {
	OciRepoSpec *v1beta1.OciRepoSpec
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
		imageExport.Status = &api.ImageExportStatus{}
	}

	// Check current state - only process if Pending (or has no Ready condition)
	// We only lock resources that are in Pending state to avoid stealing work from other processes
	var readyCondition *api.ImageExportCondition
	if imageExport.Status.Conditions != nil {
		readyCondition = api.FindImageExportStatusCondition(*imageExport.Status.Conditions, api.ImageExportConditionTypeReady)
	}
	if readyCondition != nil {
		reason := readyCondition.Reason
		// Skip if already completed or failed
		if reason == string(api.ImageExportConditionReasonCompleted) || reason == string(api.ImageExportConditionReasonFailed) {
			log.Infof("ImageExport %q already in terminal state %q, skipping", imageExportName, reason)
			return nil
		}
		// Skip if already Converting - another process is handling it
		if reason == string(api.ImageExportConditionReasonConverting) {
			log.Infof("ImageExport %q is already being processed (Converting), skipping", imageExportName)
			return nil
		}
		// Skip if Pushing - another process is handling it
		if reason == string(api.ImageExportConditionReasonPushing) {
			log.Infof("ImageExport %q is already being processed (Pushing), skipping", imageExportName)
			return nil
		}
		// Only proceed if Pending - if it's any other state, skip (shouldn't happen, but defensive)
		if reason != string(api.ImageExportConditionReasonPending) {
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
				updateCondition(ctx, c.imageBuilderService.ImageExport(), orgID, imageExport, api.ImageExportCondition{
					Type:               api.ImageExportConditionTypeReady,
					Status:             v1beta1.ConditionStatusFalse,
					Reason:             string(api.ImageExportConditionReasonPending),
					Message:            err.Error(),
					LastTransitionTime: time.Now().UTC(),
				}, log)
			}
			return nil
		}
		// For other errors, update condition and return error
		updateCondition(ctx, c.imageBuilderService.ImageExport(), orgID, imageExport, api.ImageExportCondition{
			Type:               api.ImageExportConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageExportConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: time.Now().UTC(),
		}, log)
		return err
	}

	// Lock the export: atomically transition to Converting state using resource_version
	// This ensures only one process can start processing
	now := time.Now().UTC()
	convertingCondition := api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(api.ImageExportConditionReasonConverting),
		Message:            "Export conversion in progress",
		LastTransitionTime: now,
	}

	// Prepare status with Converting condition
	if imageExport.Status == nil {
		imageExport.Status = &api.ImageExportStatus{}
	}
	if imageExport.Status.Conditions == nil {
		imageExport.Status.Conditions = &[]api.ImageExportCondition{}
	}
	api.SetImageExportStatusCondition(imageExport.Status.Conditions, convertingCondition)
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

	// Start status updater goroutine - this is the single writer for all status updates
	statusUpdater, cleanupStatusUpdater := startImageExportStatusUpdater(ctx, c.imageBuilderService.ImageExport(), orgID, imageExportName, c.cfg, log)
	defer cleanupStatusUpdater()

	// Send initial lastSeen update to status updater to ensure it's tracking the current time
	select {
	case statusUpdater.updateChan <- newImageExportStatusUpdateRequest():
	case <-ctx.Done():
		return ctx.Err()
	}

	// Execute the export
	outputFilePath, cleanup, err := c.executeExport(ctx, orgID, imageExport, source, statusUpdater, log)
	if cleanup != nil {
		defer cleanup()
	}
	if err != nil {
		failedTime := time.Now().UTC()
		failedCondition := api.ImageExportCondition{
			Type:               api.ImageExportConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageExportConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: failedTime,
		}
		statusUpdater.updateCondition(failedCondition)
		return fmt.Errorf("failed to execute export: %w", err)
	}

	log.WithField("outputFile", outputFilePath).Info("Export output file created")

	// Push artifact to destination (as a referrer to the source image)
	if err := c.pushArtifact(ctx, orgID, imageExport, source, outputFilePath, statusUpdater, log); err != nil {
		failedTime := time.Now().UTC()
		failedCondition := api.ImageExportCondition{
			Type:               api.ImageExportConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageExportConditionReasonFailed),
			Message:            fmt.Sprintf("Failed to push artifact: %v", err),
			LastTransitionTime: failedTime,
		}
		statusUpdater.updateCondition(failedCondition)
		return fmt.Errorf("failed to push artifact: %w", err)
	}

	// Mark as Completed
	now = time.Now().UTC()
	completedCondition := api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageExportConditionReasonCompleted),
		Message:            "Export completed successfully",
		LastTransitionTime: now,
	}
	statusUpdater.updateCondition(completedCondition)

	log.Info("ImageExport marked as Completed")
	return nil
}

// imageExportStatusUpdateRequest represents a request to update the ImageExport status
type imageExportStatusUpdateRequest struct {
	Condition      *api.ImageExportCondition
	LastSeen       *time.Time
	ManifestDigest *string
}

// newImageExportStatusUpdateRequest creates a new update request with LastSeen automatically set to now
func newImageExportStatusUpdateRequest() imageExportStatusUpdateRequest {
	now := time.Now().UTC()
	return imageExportStatusUpdateRequest{
		LastSeen: &now,
	}
}

// imageExportStatusUpdater manages all status updates for an ImageExport, ensuring atomic updates
// and preventing race conditions between LastSeen and condition updates.
type imageExportStatusUpdater struct {
	imageExportService imagebuilderapi.ImageExportService
	orgID              uuid.UUID
	imageExportName    string
	updateChan         chan imageExportStatusUpdateRequest
	outputChan         chan []byte // Central channel for all task outputs
	ctx                context.Context
	cancel             context.CancelFunc
	wg                 sync.WaitGroup
	log                logrus.FieldLogger
}

// startImageExportStatusUpdater starts a goroutine that is the single writer for ImageExport status updates.
func startImageExportStatusUpdater(
	ctx context.Context,
	imageExportService imagebuilderapi.ImageExportService,
	orgID uuid.UUID,
	imageExportName string,
	cfg *config.Config,
	log logrus.FieldLogger,
) (*imageExportStatusUpdater, func()) {
	updaterCtx, updaterCancel := context.WithCancel(ctx)

	updater := &imageExportStatusUpdater{
		imageExportService: imageExportService,
		orgID:              orgID,
		imageExportName:    imageExportName,
		updateChan:         make(chan imageExportStatusUpdateRequest),
		outputChan:         make(chan []byte, 100),
		ctx:                updaterCtx,
		cancel:             updaterCancel,
		log:                log,
	}

	updater.wg.Add(1)
	go updater.run(cfg)

	cleanup := func() {
		updaterCancel()
		close(updater.updateChan)
		close(updater.outputChan)
		updater.wg.Wait()
	}

	return updater, cleanup
}

// run is the main loop for the status updater goroutine
func (u *imageExportStatusUpdater) run(cfg *config.Config) {
	defer u.wg.Done()

	if cfg == nil || cfg.ImageBuilderWorker == nil {
		u.log.Error("Config or ImageBuilderWorker config is nil, cannot update status")
		return
	}
	updateInterval := time.Duration(cfg.ImageBuilderWorker.LastSeenUpdateInterval)
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	var pendingCondition *api.ImageExportCondition
	lastSeenUpdateTime := time.Now().UTC()

	var lastOutputTime *time.Time
	var lastSetLastSeen *time.Time

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			if lastOutputTime != nil {
				if lastSetLastSeen == nil || !lastOutputTime.Equal(*lastSetLastSeen) {
					lastSeenUpdateTime = *lastOutputTime
					lastSetLastSeenCopy := *lastOutputTime
					lastSetLastSeen = &lastSetLastSeenCopy
					u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, nil)
					pendingCondition = nil
				}
			}
		case output := <-u.outputChan:
			now := time.Now().UTC()
			lastOutputTime = &now
			u.log.Debugf("Task output: %s", string(output))
		case req := <-u.updateChan:
			if req.Condition != nil {
				pendingCondition = req.Condition
			}
			if req.LastSeen != nil {
				lastSeenUpdateTime = *req.LastSeen
			}
			// Update immediately when condition or manifest digest changes
			// LastSeen-only updates are handled immediately to set initial value
			if req.Condition != nil || req.ManifestDigest != nil || req.LastSeen != nil {
				u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, req.ManifestDigest)
				pendingCondition = nil
			}
		}
	}
}

// updateStatus performs the actual database update
func (u *imageExportStatusUpdater) updateStatus(ctx context.Context, condition *api.ImageExportCondition, lastSeen *time.Time, manifestDigest *string) {
	imageExport, status := u.imageExportService.Get(ctx, u.orgID, u.imageExportName)
	if imageExport == nil || !imagebuilderapi.IsStatusOK(status) {
		u.log.WithField("status", status).Warn("Failed to load ImageExport for status update")
		return
	}

	if imageExport.Status == nil {
		imageExport.Status = &api.ImageExportStatus{}
	}

	if lastSeen != nil {
		imageExport.Status.LastSeen = lastSeen
	}

	if condition != nil {
		if imageExport.Status.Conditions == nil {
			imageExport.Status.Conditions = &[]api.ImageExportCondition{}
		}
		api.SetImageExportStatusCondition(imageExport.Status.Conditions, *condition)
	}

	if manifestDigest != nil {
		imageExport.Status.ManifestDigest = manifestDigest
	}

	_, err := u.imageExportService.UpdateStatus(ctx, u.orgID, imageExport)
	if err != nil {
		u.log.WithError(err).Warn("Failed to update ImageExport status")
	}
}

// updateCondition sends a condition update request to the updater goroutine
func (u *imageExportStatusUpdater) updateCondition(condition api.ImageExportCondition) {
	req := newImageExportStatusUpdateRequest()
	req.Condition = &condition
	select {
	case u.updateChan <- req:
	case <-u.ctx.Done():
	}
}

// setManifestDigest sets the manifest digest in the ImageExport status
func (u *imageExportStatusUpdater) setManifestDigest(manifestDigest string) {
	req := newImageExportStatusUpdateRequest()
	req.ManifestDigest = &manifestDigest
	select {
	case u.updateChan <- req:
	case <-u.ctx.Done():
	}
}

// reportOutput sends task output to the central output handler
func (u *imageExportStatusUpdater) reportOutput(output []byte) {
	select {
	case u.outputChan <- output:
	case <-u.ctx.Done():
	}
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

// privilegedPodmanWorker holds information about a running privileged podman worker container
type privilegedPodmanWorker struct {
	ContainerName       string
	TmpDir              string
	TmpOutDir           string
	TmpContainerStorage string
	Cleanup             func()
	statusUpdater       *imageExportStatusUpdater
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
	imageExport *api.ImageExport,
	exportSource *exportSource,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) (string, func(), error) {
	// Build image reference string for logging and podman operations
	registryHostname := exportSource.OciRepoSpec.Registry
	bootcImageRef := fmt.Sprintf("%s/%s:%s", registryHostname, exportSource.ImageName, exportSource.ImageTag)

	log.WithField("bootcImage", bootcImageRef).Info("Resolved bootc image reference")

	// Step 2: Start bootc-image-builder container
	worker, err := c.startBootcImageBuilderContainer(ctx, orgID, imageExport, statusUpdater, log)
	if err != nil {
		return "", nil, fmt.Errorf("failed to start bootc-image-builder container: %w", err)
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

	// Step 3: Login to registry if credentials are provided
	if exportSource.OciRepoSpec.OciAuth != nil {
		dockerAuth, err := exportSource.OciRepoSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			if err := c.loginToRegistryForExport(ctx, worker, registryHostname, dockerAuth.Username, dockerAuth.Password, log); err != nil {
				return "", cleanup, fmt.Errorf("failed to login to registry: %w", err)
			}
		}
	}

	// Step 4: Pull the source image
	if err := c.pullSourceImage(ctx, worker, bootcImageRef, log); err != nil {
		return "", cleanup, fmt.Errorf("failed to pull source image: %w", err)
	}

	// Step 5: Run bootc-image-builder conversion
	if err := c.runBootcImageBuilder(ctx, worker, imageExport.Spec.Format, bootcImageRef, log); err != nil {
		return "", cleanup, fmt.Errorf("failed to run bootc-image-builder: %w", err)
	}

	// List output directory contents recursively after bootc conversion
	log.Debug("Listing output directory contents after bootc conversion")
	lsArgs := []string{"exec", worker.ContainerName, "ls", "-laR", "/output"}
	lsCmd := exec.CommandContext(ctx, "podman", lsArgs...)
	var lsOutput bytes.Buffer
	lsCmd.Stdout = &lsOutput
	lsCmd.Stderr = &lsOutput
	if lsErr := lsCmd.Run(); lsErr == nil {
		log.WithField("output_contents", lsOutput.String()).Debug("Output directory contents after bootc conversion")
	} else {
		log.WithError(lsErr).WithField("output", lsOutput.String()).Warn("Failed to list output directory contents")
	}

	// Step 6: Find the output file
	outputFilePath, err := c.findOutputFile(worker.TmpOutDir, imageExport.Spec.Format, log)
	if err != nil {
		return "", cleanup, fmt.Errorf("failed to find output file: %w", err)
	}

	log.WithField("outputFile", outputFilePath).Info("Export completed successfully")
	return outputFilePath, cleanup, nil
}

// startBootcImageBuilderContainer starts the bootc-image-builder container directly with sleep infinity
func (c *Consumer) startBootcImageBuilderContainer(
	ctx context.Context,
	orgID uuid.UUID,
	imageExport *api.ImageExport,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) (*privilegedPodmanWorker, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Get bootc-image-builder image from config
	if c.cfg == nil || c.cfg.ImageBuilderWorker == nil {
		return nil, fmt.Errorf("config or ImageBuilderWorker config is nil")
	}
	bootcImageBuilderImage := c.cfg.ImageBuilderWorker.BootcImageBuilderImage
	if bootcImageBuilderImage == "" {
		return nil, fmt.Errorf("bootcImageBuilderImage is not configured")
	}

	// Create temporary directories
	tmpDir, err := os.MkdirTemp("", "imageexport-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}

	tmpOutDir, err := os.MkdirTemp("", "imageexport-out-*")
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to create temporary output directory: %w", err)
	}

	baseStorageDir := "/var/tmp/flightctl-exports"
	tmpContainerStorage, err := os.MkdirTemp(baseStorageDir, "storage-*")
	if err != nil {
		os.RemoveAll(tmpDir)
		os.RemoveAll(tmpOutDir)
		return nil, fmt.Errorf("failed to create temporary container storage: %w", err)
	}

	// Container paths
	containerOutDir := "/output"
	containerStorageDir := "/var/lib/containers"

	// Generate unique container name
	imageExportName := lo.FromPtr(imageExport.Metadata.Name)
	containerName := fmt.Sprintf("bootc-builder-%s-%s", orgID.String()[:8], imageExportName)

	log.Info("Starting bootc-image-builder container")
	startArgs := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"--privileged",
		"--pull=newer",
		"--entrypoint", "sleep",
		"--security-opt", "label=type:unconfined_t",
		"-v", fmt.Sprintf("%s:%s:Z", tmpOutDir, containerOutDir),
		"-v", fmt.Sprintf("%s:%s:Z", tmpContainerStorage, containerStorageDir),
		bootcImageBuilderImage,
		"infinity",
	}

	cmdParts := []string{"podman"}
	cmdParts = append(cmdParts, startArgs...)
	cmdStr := strings.Join(cmdParts, " ")
	log.WithField("command", cmdStr).Debug("Executing podman command")

	if out, err := exec.CommandContext(ctx, "podman", startArgs...).CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		os.RemoveAll(tmpOutDir)
		os.RemoveAll(tmpContainerStorage)
		return nil, fmt.Errorf("failed to start bootc-image-builder container: %w, output: %s", err, string(out))
	}

	cleanup := func() {
		log.Debug("Cleaning up bootc-image-builder container")
		killCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := exec.CommandContext(killCtx, "podman", "kill", containerName).Run(); err != nil {
			log.WithError(err).Warn("Failed to kill bootc-image-builder container during cleanup")
		}
		os.RemoveAll(tmpDir)
		// Don't remove tmpOutDir here - it's cleaned up separately after pushArtifact completes
		os.RemoveAll(tmpContainerStorage)
	}

	return &privilegedPodmanWorker{
		ContainerName:       containerName,
		TmpDir:              tmpDir,
		TmpOutDir:           tmpOutDir,
		TmpContainerStorage: tmpContainerStorage,
		Cleanup:             cleanup,
		statusUpdater:       statusUpdater,
	}, nil
}

// initializePodmanStorage initializes podman storage inside the container
// Podman needs to create its storage database files and structure before it can be used.
// Running podman info will initialize the storage structure using the storage.conf that's mounted.
func (c *Consumer) initializePodmanStorage(ctx context.Context, worker *privilegedPodmanWorker, log logrus.FieldLogger) error {
	log.Debug("Initializing podman storage inside bootc-image-builder container")

	// Run podman info to initialize the storage structure and database files
	// This creates the necessary internal files and directories that podman needs
	// The storage.conf and containers.conf are already mounted, so podman will use them
	execArgs := []string{"exec", worker.ContainerName, "podman", "info"}
	cmd := exec.CommandContext(ctx, "podman", execArgs...)

	var outputBuffer bytes.Buffer
	writer := &imageExportStatusWriter{
		buf:           &outputBuffer,
		statusUpdater: worker.statusUpdater,
	}

	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Run(); err != nil {
		output := outputBuffer.String()
		log.Debugf("podman info output:\n%s", output)
		return fmt.Errorf("failed to initialize podman storage: %w. Output: %s", err, output)
	}

	log.Debug("Podman storage initialized successfully")

	// Verify that the overlay directory was created by podman info
	log.Debug("Verifying overlay directory was created")
	overlayCheckArgs := []string{"exec", worker.ContainerName, "test", "-d", "/var/lib/containers/storage/overlay"}
	overlayCheckCmd := exec.CommandContext(ctx, "podman", overlayCheckArgs...)
	if err := overlayCheckCmd.Run(); err != nil {
		// List the storage directory to see what was actually created
		lsArgs := []string{"exec", worker.ContainerName, "ls", "-laR", "/var/lib/containers/storage"}
		lsCmd := exec.CommandContext(ctx, "podman", lsArgs...)
		var lsOutput bytes.Buffer
		lsCmd.Stdout = &lsOutput
		lsCmd.Stderr = &lsOutput
		if lsErr := lsCmd.Run(); lsErr == nil {
			log.WithField("storage_contents", lsOutput.String()).Error("overlay directory not found after podman info. Storage contents:")
		}
		return fmt.Errorf("overlay directory was not created by podman info: %w", err)
	}

	log.Debug("overlay directory verified - podman storage initialization successful")
	return nil
}

// loginToRegistryForExport logs into a registry using podman login with stdin
// This is used for pull operations where authfile doesn't work reliably
func (c *Consumer) loginToRegistryForExport(
	ctx context.Context,
	worker *privilegedPodmanWorker,
	registryHostname string,
	username string,
	password string,
	log logrus.FieldLogger,
) error {
	if username == "" || password == "" {
		return nil
	}

	// Validate username to prevent command injection
	if strings.ContainsAny(username, ";|&`(){}[]<>\"'\\\n\r\t") {
		return fmt.Errorf("invalid username: contains unsafe characters")
	}
	if len(username) > 256 {
		return fmt.Errorf("invalid username: exceeds maximum length of 256 characters")
	}

	// Validate registryHostname to prevent command injection
	if strings.ContainsAny(registryHostname, ";|&`(){}[]<>\"'\\\n\r\t") {
		return fmt.Errorf("invalid registry hostname: contains unsafe characters")
	}
	if len(registryHostname) > 256 {
		return fmt.Errorf("invalid registry hostname: exceeds maximum length of 256 characters")
	}

	log.WithField("registry", registryHostname).Debug("Logging into registry with podman login")

	// Run podman login inside the container with stdin
	// Format: podman exec -i <container> podman login -u <username> --password-stdin <registry>
	// username and registryHostname are validated above to prevent command injection
	//nolint:gosec // G204: Inputs are validated above to prevent command injection. exec.CommandContext uses separate arguments (not shell), making this safe.
	loginCmd := exec.CommandContext(ctx, "podman", "exec", "-i", worker.ContainerName, "podman", "login", "-u", username, "--password-stdin", registryHostname)

	// Write password to stdin
	loginCmd.Stdin = strings.NewReader(password)

	var outputBuffer bytes.Buffer
	loginCmd.Stdout = &outputBuffer
	loginCmd.Stderr = &outputBuffer

	if err := loginCmd.Run(); err != nil {
		output := outputBuffer.String()
		log.WithError(err).Warnf("Failed to login to registry: %s", output)
		return fmt.Errorf("failed to login to registry %q: %w. Output: %s", registryHostname, err, output)
	}

	log.Debugf("Successfully logged into registry %q", registryHostname)
	return nil
}

// pullSourceImage pulls the source image into the worker container
func (c *Consumer) pullSourceImage(ctx context.Context, worker *privilegedPodmanWorker, bootcImageRef string, log logrus.FieldLogger) error {
	log.WithField("image", bootcImageRef).Info("Pulling source image")

	// Run podman pull inside the container
	execArgs := []string{"exec", worker.ContainerName, "podman", "pull", bootcImageRef}
	cmd := exec.CommandContext(ctx, "podman", execArgs...)

	var outputBuffer bytes.Buffer
	writer := &imageExportStatusWriter{
		buf:           &outputBuffer,
		statusUpdater: worker.statusUpdater,
	}

	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Run(); err != nil {
		output := outputBuffer.String()
		log.Debugf("Pull output:\n%s", output)
		return fmt.Errorf("failed to pull image %q: %w. Output: %s", bootcImageRef, err, output)
	}

	output := outputBuffer.String()
	log.Debugf("Pull output:\n%s", output)
	log.Info("Successfully pulled source image")
	return nil
}

// runBootcImageBuilder runs bootc-image-builder entrypoint inside the existing container
func (c *Consumer) runBootcImageBuilder(
	ctx context.Context,
	worker *privilegedPodmanWorker,
	format api.ExportFormatType,
	bootcImageRef string,
	log logrus.FieldLogger,
) error {
	log.WithFields(logrus.Fields{
		"format": format,
		"image":  bootcImageRef,
	}).Info("Running bootc-image-builder")

	// Run bootc-image-builder entrypoint inside the existing container
	// Format: podman exec -w /output <container> bootc-image-builder --type qcow2 --rootfs xfs "${BOOTC_IMAGE}"
	// Use -w to set working directory to /output so files are saved there
	execArgs := []string{
		"exec",
		"-w", "/output",
		worker.ContainerName,
		"bootc-image-builder",
		"--type", string(format),
		"--rootfs", "xfs",
		bootcImageRef,
	}

	cmd := exec.CommandContext(ctx, "podman", execArgs...)

	var outputBuffer bytes.Buffer
	writer := &imageExportStatusWriter{
		buf:           &outputBuffer,
		statusUpdater: worker.statusUpdater,
	}

	cmd.Stdout = writer
	cmd.Stderr = writer

	if err := cmd.Run(); err != nil {
		output := outputBuffer.String()
		log.Debugf("bootc-image-builder output:\n%s", output)
		return fmt.Errorf("bootc-image-builder failed: %w. Output: %s", err, output)
	}

	output := outputBuffer.String()
	log.Debugf("bootc-image-builder output:\n%s", output)
	log.Info("bootc-image-builder completed successfully")
	return nil
}

// findOutputFile returns the path to the output file created by bootc-image-builder
// bootc-image-builder creates files at {type}/disk.{type} relative to the working directory
// Since we run with -w /output, files are at /output/{type}/disk.{type} in container
// which maps to {outputDir}/{type}/disk.{type} on the host
// Exception: ISO format uses bootiso/install.iso instead of iso/disk.iso
func (c *Consumer) findOutputFile(outputDir string, format api.ExportFormatType, log logrus.FieldLogger) (string, error) {
	var outputFilePath string
	if format == api.ExportFormatTypeISO {
		// ISO format uses bootiso/install.iso instead of iso/disk.iso
		outputFilePath = filepath.Join(outputDir, "bootiso", "install.iso")
	} else {
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

// updateCondition updates the ImageExport condition and status
func updateCondition(
	ctx context.Context,
	imageExportService imagebuilderapi.ImageExportService,
	orgID uuid.UUID,
	imageExport *api.ImageExport,
	condition api.ImageExportCondition,
	log logrus.FieldLogger,
) {
	now := time.Now().UTC()
	if imageExport.Status == nil {
		imageExport.Status = &api.ImageExportStatus{}
	}
	if imageExport.Status.Conditions == nil {
		imageExport.Status.Conditions = &[]api.ImageExportCondition{}
	}
	api.SetImageExportStatusCondition(imageExport.Status.Conditions, condition)
	imageExport.Status.LastSeen = &now
	if _, updateErr := imageExportService.UpdateStatus(ctx, orgID, imageExport); updateErr != nil {
		log.WithError(updateErr).Error("failed to update ImageExport status")
	}
}

// validateAndNormalizeSource validates the ImageExport source and returns a normalized exportSource
func (c *Consumer) validateAndNormalizeSource(ctx context.Context, orgID uuid.UUID, imageExport *api.ImageExport, log logrus.FieldLogger) (*exportSource, error) {
	sourceType, err := imageExport.Spec.Source.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine source type: %w", err)
	}

	var repoName string
	var imageName string
	var imageTag string

	switch sourceType {
	case string(api.ImageExportSourceTypeImageBuild):
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
		imageTag = imageBuild.Spec.Destination.Tag

		log.Infof("ImageBuild %q is ready, proceeding with export", source.ImageBuildRef)

	case string(api.ImageExportSourceTypeImageReference):
		source, err := imageExport.Spec.Source.AsImageReferenceSource()
		if err != nil {
			return nil, fmt.Errorf("failed to parse imageReference source: %w", err)
		}

		repoName = source.Repository
		imageName = source.ImageName
		imageTag = source.ImageTag

	default:
		return nil, fmt.Errorf("unknown source type: %q", sourceType)
	}

	// Get repository and extract OCI spec (common for both source types)
	repo, err := c.mainStore.Repository().Get(ctx, orgID, repoName)
	if err != nil {
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
	imageExport *api.ImageExport,
	exportSource *exportSource,
	artifactPath string,
	statusUpdater *imageExportStatusUpdater,
	log logrus.FieldLogger,
) error {
	// Get destination repository for authentication and to get registry hostname
	repo, err := c.mainStore.Repository().Get(ctx, orgID, imageExport.Spec.Destination.Repository)
	if err != nil {
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
	destRef := fmt.Sprintf("%s/%s", destRegistryHostname, imageExport.Spec.Destination.ImageName)

	log.WithFields(logrus.Fields{
		"destination": destRef,
		"artifact":    artifactPath,
		"format":      imageExport.Spec.Format,
		"subject":     fmt.Sprintf("%s:%s", destRef, imageExport.Spec.Destination.Tag),
	}).Info("Pushing artifact as referrer to destination")

	// Update condition to Pushing
	pushingTime := time.Now().UTC()
	pushingCondition := api.ImageExportCondition{
		Type:               api.ImageExportConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(api.ImageExportConditionReasonPushing),
		Message:            "Pushing artifact to destination registry",
		LastTransitionTime: pushingTime,
	}
	statusUpdater.updateCondition(pushingCondition)
	statusUpdater.reportOutput([]byte("Starting artifact push to destination registry\n"))

	// Create repository reference (no tag needed - referrer will be discoverable via referrers API)
	repoRef, err := remote.NewRepository(destRef)
	if err != nil {
		return fmt.Errorf("failed to create repository reference: %w", err)
	}

	// Skip referrers GC to avoid authentication issues when pushing multiple artifacts
	// When multiple artifacts (e.g., QCOW2 and VMDK) point to the same subject image,
	// ORAS tries to delete old referrer indices which can cause auth failures with some registries
	repoRef.SkipReferrersGC = true

	// Set up authentication if credentials are provided
	if ociSpec.OciAuth != nil {
		dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			repoRef.Client = &auth.Client{
				Credential: auth.StaticCredential(destRegistryHostname, auth.Credential{
					Username: dockerAuth.Username,
					Password: dockerAuth.Password,
				}),
			}
			log.Info("Successfully configured authentication for destination registry")
			statusUpdater.reportOutput([]byte("Authenticated with destination registry\n"))
		}
	}

	// Resolve the destination image's manifest to get its digest for the referrer subject
	destImageTag := imageExport.Spec.Destination.Tag
	destManifestDesc, err := getReferencedDigest(ctx, repoRef, destImageTag, destRef, statusUpdater, log)
	if err != nil {
		return err
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

// isImageBuildReady checks if an ImageBuild is ready (completed with image reference)
func isImageBuildReady(imageBuild *api.ImageBuild) bool {
	if imageBuild.Status == nil || imageBuild.Status.Conditions == nil {
		return false
	}

	readyCondition := api.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, api.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return false
	}

	if readyCondition.Reason != string(api.ImageBuildConditionReasonCompleted) {
		return false
	}

	if imageBuild.Status.ImageReference == nil || *imageBuild.Status.ImageReference == "" {
		return false
	}

	return true
}
