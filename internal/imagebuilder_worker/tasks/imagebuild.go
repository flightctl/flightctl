package tasks

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	imagebuilderapi "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	imagebuilderservice "github.com/flightctl/flightctl/internal/imagebuilder_api/service"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// agentConfigPath is the destination path for the agent config in the image
	agentConfigPath = "/etc/flightctl/config.yaml"
)

// containerfileTemplate is embedded from the templates directory for easier editing
//
//go:embed templates/Containerfile.tmpl
var containerfileTemplate string

// ContainerfileResult contains the generated Containerfile and any associated files
type ContainerfileResult struct {
	// Containerfile is the generated Containerfile content (static template)
	Containerfile string
	// BuildArgs contains the arguments to pass to podman build via --build-arg
	BuildArgs containerfileBuildArgs
	// AgentConfig contains the full agent config.yaml content (for early binding)
	// This includes: client-certificate-data, client-key-data, certificate-authority-data, server URL
	AgentConfig []byte
	// Publickey contains the SSH public key content (for user configuration)
	Publickey []byte
}

// processImageBuild processes an imageBuild event by loading the ImageBuild resource
// and routing to the appropriate build handler
func (c *Consumer) processImageBuild(ctx context.Context, eventWithOrgId worker_client.EventWithOrgId, log logrus.FieldLogger) error {
	event := eventWithOrgId.Event
	orgID := eventWithOrgId.OrgId
	imageBuildName := event.InvolvedObject.Name

	log = log.WithField("imageBuild", imageBuildName).WithField("orgId", orgID)
	log.Info("Processing imageBuild event")

	// Load the ImageBuild resource from the database
	imageBuild, status := c.imageBuilderService.ImageBuild().Get(ctx, orgID, imageBuildName, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		return fmt.Errorf("failed to load ImageBuild %q: %v", imageBuildName, status)
	}

	log.WithField("spec", imageBuild.Spec).Debug("Loaded ImageBuild resource")

	// Initialize status if nil
	if imageBuild.Status == nil {
		imageBuild.Status = &domain.ImageBuildStatus{}
	}

	// Check current state - only process if Pending (or has no Ready condition)
	// We only lock resources that are in Pending state to avoid stealing work from other processes
	var readyCondition *domain.ImageBuildCondition
	if imageBuild.Status.Conditions != nil {
		readyCondition = domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	}
	if readyCondition != nil {
		reason := readyCondition.Reason
		// Skip if already in terminal state (completed, failed, canceled)
		if reason == string(domain.ImageBuildConditionReasonCompleted) ||
			reason == string(domain.ImageBuildConditionReasonFailed) ||
			reason == string(domain.ImageBuildConditionReasonCanceled) {
			log.Infof("ImageBuild %q already in terminal state %q, skipping", imageBuildName, reason)
			return nil
		}
		// Skip if already Building - another process is handling it
		if reason == string(domain.ImageBuildConditionReasonBuilding) {
			log.Infof("ImageBuild %q is already being processed (Building), skipping", imageBuildName)
			return nil
		}
		// Skip if Pushing - another process is handling it
		if reason == string(domain.ImageBuildConditionReasonPushing) {
			log.Infof("ImageBuild %q is already being processed (Pushing), skipping", imageBuildName)
			return nil
		}
		// If Canceling and we haven't started processing yet, complete the cancellation
		// This happens when a Pending build was canceled before the worker picked it up
		if reason == string(domain.ImageBuildConditionReasonCanceling) {
			log.Infof("ImageBuild %q was canceled before processing started, completing cancellation", imageBuildName)
			if err := c.markImageBuildAsCanceled(ctx, orgID, imageBuild, log); err != nil {
				return fmt.Errorf("failed to mark ImageBuild as canceled: %w", err)
			}
			return nil
		}
		// Only proceed if Pending - if it's any other state, skip (shouldn't happen, but defensive)
		if reason != string(domain.ImageBuildConditionReasonPending) {
			log.Warnf("ImageBuild %q is in unexpected state %q (expected Pending), skipping", imageBuildName, reason)
			return nil
		}
	}
	// If no Ready condition exists, treat as Pending and proceed

	// Lock the build: atomically transition from Pending to Building state using resource_version
	// This ensures only one process can start processing
	now := time.Now().UTC()
	buildingCondition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageBuildConditionReasonBuilding),
		Message:            "Build is in progress",
		LastTransitionTime: now,
	}

	// Prepare status with Building condition
	if imageBuild.Status == nil {
		imageBuild.Status = &domain.ImageBuildStatus{}
	}
	if imageBuild.Status.Conditions == nil {
		imageBuild.Status.Conditions = &[]domain.ImageBuildCondition{}
	}
	domain.SetImageBuildStatusCondition(imageBuild.Status.Conditions, buildingCondition)

	// Synchronously update status to Building - this will fail if resource_version changed
	_, err := c.imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		if errors.Is(err, flterrors.ErrNoRowsUpdated) {
			// Another process updated the resource - it's likely already Building
			log.Infof("ImageBuild %q was updated by another process, skipping", imageBuildName)
			return nil
		}
		return fmt.Errorf("failed to lock ImageBuild for processing: %w", err)
	}

	log.Info("Successfully locked ImageBuild for processing (status set to Building)")

	// Create a cancelable context for the build process
	// The cancel function is passed to statusUpdater which will call it when cancellation is received via Redis
	buildCtx, cancelBuild := context.WithCancel(ctx)
	defer cancelBuild()

	// Start status updater goroutine - this is the single writer for all status updates
	// It handles both LastSeen (periodic) and condition updates (on-demand)
	// It also listens for cancellation signals via Redis Stream
	// IMPORTANT: Pass the original ctx (not buildCtx) so the updater can complete
	// final status updates (like Canceled) even after buildCtx is canceled
	statusUpdater, cleanupStatusUpdater := StartStatusUpdater(ctx, cancelBuild, c.imageBuilderService.ImageBuild(), orgID, imageBuildName, c.kvStore, c.cfg, log)
	defer cleanupStatusUpdater()

	// Step 1: Generate Containerfile
	log.Info("Generating Containerfile for image build")
	containerfileResult, err := c.generateContainerfile(buildCtx, orgID, imageBuild, log)
	if err != nil {
		if c.handleBuildError(ctx, orgID, imageBuildName, err, statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to generate Containerfile: %w", err)
	}

	log.WithField("containerfile_length", len(containerfileResult.Containerfile)).Info("Containerfile generated successfully")
	log.Debug("Generated Containerfile: ", containerfileResult.Containerfile)

	// Step 2: Start podman worker container
	podmanWorker, err := c.startPodmanWorker(buildCtx, orgID, imageBuild, statusUpdater, log)
	if err != nil {
		if c.handleBuildError(ctx, orgID, imageBuildName, err, statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to start podman worker: %w", err)
	}
	defer podmanWorker.Cleanup()

	// Step 3: Build with podman container in container
	err = c.buildImageWithPodman(buildCtx, orgID, imageBuild, containerfileResult, podmanWorker, log)
	if err != nil {
		if c.handleBuildError(ctx, orgID, imageBuildName, err, statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to build image with podman: %w", err)
	}

	// Step 4: Push image to registry
	imageRef, err := c.pushImageWithPodman(buildCtx, orgID, imageBuild, podmanWorker, log)
	if err != nil {
		if c.handleBuildError(ctx, orgID, imageBuildName, err, statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to push image with podman: %w", err)
	}

	// Update ImageBuild status with the pushed image reference and mark as Completed
	statusUpdater.UpdateImageReference(imageRef)

	// Mark as Completed
	now = time.Now().UTC()
	completedCondition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusTrue,
		Reason:             string(domain.ImageBuildConditionReasonCompleted),
		Message:            "Build completed successfully",
		LastTransitionTime: now,
	}
	statusUpdater.UpdateCondition(completedCondition)

	log.Info("ImageBuild marked as Completed")
	return nil
}

// handleBuildError handles errors during the build process.
// If the error was due to cancellation (status is Canceling), it sets the final status and returns true.
// - For user cancellation: status becomes Canceled
// - For timeout: status becomes Failed (with the timeout message preserved)
// Otherwise, it sets status to Failed and returns false.
// The ctx parameter should be the original context (not buildCtx) to ensure we can still query the database.
func (c *Consumer) handleBuildError(ctx context.Context, orgID uuid.UUID, imageBuildName string, err error, statusUpdater *statusUpdater, log logrus.FieldLogger) bool {
	// Check if the current status is Canceling (set by API or timeout task)
	currentBuild, status := c.imageBuilderService.ImageBuild().Get(ctx, orgID, imageBuildName, false)
	if currentBuild != nil && imagebuilderapi.IsStatusOK(status) &&
		currentBuild.Status != nil && currentBuild.Status.Conditions != nil {
		readyCondition := domain.FindImageBuildStatusCondition(*currentBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
		if readyCondition != nil && readyCondition.Reason == string(domain.ImageBuildConditionReasonCanceling) {
			// This was a cancellation - check if it was a timeout or user cancellation
			message := readyCondition.Message
			isTimeout := strings.Contains(message, "timed out")

			if isTimeout {
				// Timeout - set Failed status with the timeout message
				log.WithField("message", message).Info("Build timed out, setting status to Failed")
				failedCondition := domain.ImageBuildCondition{
					Type:               domain.ImageBuildConditionTypeReady,
					Status:             domain.ConditionStatusFalse,
					Reason:             string(domain.ImageBuildConditionReasonFailed),
					Message:            message,
					LastTransitionTime: time.Now().UTC(),
				}
				statusUpdater.UpdateCondition(failedCondition)
			} else {
				// User cancellation - set Canceled status
				if message == "" {
					message = "Build was canceled"
				}
				log.WithField("message", message).Info("Build was canceled, setting status to Canceled")
				canceledCondition := domain.ImageBuildCondition{
					Type:               domain.ImageBuildConditionTypeReady,
					Status:             domain.ConditionStatusFalse,
					Reason:             string(domain.ImageBuildConditionReasonCanceled),
					Message:            message,
					LastTransitionTime: time.Now().UTC(),
				}
				statusUpdater.UpdateCondition(canceledCondition)
			}
			return true // Cancellation/timeout handled
		}
	}

	// Not canceled - set Failed status
	failedCondition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageBuildConditionReasonFailed),
		Message:            err.Error(),
		LastTransitionTime: time.Now().UTC(),
	}
	statusUpdater.UpdateCondition(failedCondition)
	return false
}

// containerfileBuildArgs holds the build arguments for the Containerfile
// These are passed via --build-arg flags to podman build for safer execution
type containerfileBuildArgs struct {
	RegistryHostname    string
	ImageName           string
	ImageTag            string
	EarlyBinding        bool
	AgentConfigDestPath string
	Username            string
	HasUserConfig       bool
	RPMRepoURL          string
}

// getRPMRepoURL returns the RPM repo URL from config, falling back to default if not configured
func (c *Consumer) getRPMRepoURL() string {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.RPMRepoURL != "" {
		return c.cfg.ImageBuilderWorker.RPMRepoURL
	}
	return config.NewDefaultImageBuilderWorkerConfig().RPMRepoURL
}

// EnrollmentCredentialGenerator is an interface for generating enrollment credentials
// This allows for easier testing by mocking the service handler
type EnrollmentCredentialGenerator interface {
	GenerateEnrollmentCredential(ctx context.Context, orgId uuid.UUID, baseName string, ownerKind string, ownerName string) (*crypto.EnrollmentCredential, coredomain.Status)
}

// GenerateContainerfile generates a Containerfile from an ImageBuild spec
// This function is exported for testing purposes
func GenerateContainerfile(
	ctx context.Context,
	mainStore store.Store,
	credentialGenerator EnrollmentCredentialGenerator,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	log logrus.FieldLogger,
) (*ContainerfileResult, error) {
	// Create a temporary consumer for testing purposes
	var serviceHandler *service.ServiceHandler
	if sh, ok := credentialGenerator.(*service.ServiceHandler); ok {
		serviceHandler = sh
	}
	c := &Consumer{
		mainStore:      mainStore,
		serviceHandler: serviceHandler,
		log:            log,
	}
	return c.generateContainerfileWithGenerator(ctx, orgID, imageBuild, credentialGenerator, log)
}

// generateContainerfile generates a Containerfile from an ImageBuild spec
func (c *Consumer) generateContainerfile(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	log logrus.FieldLogger,
) (*ContainerfileResult, error) {
	return c.generateContainerfileWithGenerator(ctx, orgID, imageBuild, c.serviceHandler, log)
}

// generateContainerfileWithGenerator generates a Containerfile from an ImageBuild spec
// credentialGenerator can be provided for testing with mocks
func (c *Consumer) generateContainerfileWithGenerator(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	credentialGenerator EnrollmentCredentialGenerator,
	log logrus.FieldLogger,
) (*ContainerfileResult, error) {
	// Check context before starting work
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if imageBuild == nil {
		return nil, fmt.Errorf("imageBuild cannot be nil")
	}

	spec := imageBuild.Spec

	// Load the source repository to get the registry hostname
	repo, err := c.mainStore.Repository().Get(ctx, orgID, spec.Source.Repository)
	if err != nil {
		return nil, fmt.Errorf("failed to get source repository: %w", err)
	}

	// Validate that the repository is of OCI type
	repoType, err := repo.Spec.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine repository type: %w", err)
	}
	if repoType != string(coredomain.RepoSpecTypeOci) {
		return nil, fmt.Errorf("repository %q must be of type 'oci', got %q", spec.Source.Repository, repoType)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	// ociSpec.Registry is already the hostname (no scheme)
	registryHostname := ociSpec.Registry

	// Determine binding type
	bindingType, err := spec.Binding.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine binding type: %w", err)
	}

	log.WithFields(logrus.Fields{
		"registryHostname": registryHostname,
		"imageName":        spec.Source.ImageName,
		"imageTag":         spec.Source.ImageTag,
		"bindingType":      bindingType,
	}).Debug("Generating Containerfile")

	isEarlyBinding := bindingType == string(domain.BindingTypeEarly)
	hasUserConfig := spec.UserConfiguration != nil

	// Prepare build arguments (passed via --build-arg to podman build)
	buildArgs := containerfileBuildArgs{
		RegistryHostname:    registryHostname,
		ImageName:           spec.Source.ImageName,
		ImageTag:            spec.Source.ImageTag,
		EarlyBinding:        isEarlyBinding,
		AgentConfigDestPath: agentConfigPath,
		HasUserConfig:       hasUserConfig,
		RPMRepoURL:          c.getRPMRepoURL(),
	}

	result := &ContainerfileResult{
		Containerfile: containerfileTemplate, // Static template, no Go template rendering needed
		BuildArgs:     buildArgs,
	}

	// Add user configuration if provided
	if hasUserConfig {
		buildArgs.Username = spec.UserConfiguration.Username
		result.BuildArgs = buildArgs
		result.Publickey = []byte(spec.UserConfiguration.Publickey)
	}

	// Handle early binding - generate enrollment credentials
	if isEarlyBinding {
		// Generate a unique name for this build's enrollment credentials
		imageBuildName := lo.FromPtr(imageBuild.Metadata.Name)
		credentialName := fmt.Sprintf("imagebuild-%s-%s", imageBuildName, orgID.String()[:8])

		agentConfig, err := c.generateAgentConfigWithGenerator(ctx, orgID, credentialName, imageBuildName, credentialGenerator)
		if err != nil {
			return nil, fmt.Errorf("failed to generate agent config for early binding: %w", err)
		}

		result.AgentConfig = agentConfig
		log.WithField("credentialName", credentialName).Debug("Generated agent config for early binding")
	}

	return result, nil
}

// generateAgentConfigWithGenerator generates a complete agent config.yaml for early binding.
// credentialGenerator can be provided for testing with mocks
func (c *Consumer) generateAgentConfigWithGenerator(ctx context.Context, orgID uuid.UUID, name string, imageBuildName string, credentialGenerator EnrollmentCredentialGenerator) ([]byte, error) {
	// Generate enrollment credential using the credential generator
	// This will create a CSR, auto-approve it, sign it, and return the credential
	// The CSR owner is set to the ImageBuild resource for traceability
	credential, status := credentialGenerator.GenerateEnrollmentCredential(ctx, orgID, name, string(domain.ResourceKindImageBuild), imageBuildName)
	if err := service.ApiStatusToErr(status); err != nil {
		return nil, fmt.Errorf("generating enrollment credential: %w", err)
	}

	// Convert to agent config.yaml format
	agentConfig, err := credential.ToAgentConfig()
	if err != nil {
		return nil, fmt.Errorf("converting credential to agent config: %w", err)
	}

	return agentConfig, nil
}

// entitlementCertsPath is the standard RHEL entitlement certificates path
const entitlementCertsPath = "/etc/pki/entitlement"

// podmanWorker holds information about a running podman worker container
type podmanWorker struct {
	ContainerName       string
	TmpDir              string
	TmpOutDir           string
	TmpContainerStorage string
	Cleanup             func()
	statusUpdater       *statusUpdater // Reference to status updater for output reporting
	HasEntitlementCerts bool           // Whether entitlement certs are mounted (for RHEL subscription repos)
}

// statusWriter is a thread-safe writer that captures output to a buffer
// and streams it to the status updater for progress tracking
type statusWriter struct {
	mu            sync.Mutex
	buf           *bytes.Buffer
	statusUpdater *statusUpdater
}

// Write implements io.Writer to handle the stream safely
func (w *statusWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// 1. Capture to memory buffer
	w.buf.Write(p)

	// 2. Stream to updater
	if w.statusUpdater != nil {
		w.statusUpdater.ReportOutput(p)
	}
	return len(p), nil
}

// validateImageRefComponents validates the components of an image reference
// This is a defense-in-depth check - user input is already validated at the service layer
func validateImageRefComponents(imageName, imageTag string) error {
	if errs := imagebuilderservice.ValidateImageName(&imageName, "imageName"); len(errs) > 0 {
		return fmt.Errorf("invalid image name in image reference: %v", errs)
	}
	if errs := imagebuilderservice.ValidateImageTag(&imageTag, "imageTag"); len(errs) > 0 {
		return fmt.Errorf("invalid image tag in image reference: %v", errs)
	}
	return nil
}

// runInWorker runs a podman command inside the worker container
// It streams output to the status updater to track progress
// envVars is a map of environment variable names to values (e.g., {"REGISTRY_AUTH_FILE": "/build/auth.json"})
func (w *podmanWorker) runInWorker(ctx context.Context, log logrus.FieldLogger, phaseName string, envVars map[string]string, args ...string) error {
	// We use "podman exec" to run inside the running container
	execArgs := []string{"exec"}

	// Add environment variables using -e flag
	// Iterating over a nil map is safe in Go (no-op)
	for key, value := range envVars {
		execArgs = append(execArgs, "-e", fmt.Sprintf("%s=%s", key, value))
	}

	execArgs = append(execArgs, w.ContainerName, "podman")
	execArgs = append(execArgs, args...)
	cmd := exec.CommandContext(ctx, "podman", execArgs...)

	// Create a shared buffer for the final output
	var outputBuffer bytes.Buffer

	// Create our thread-safe writer
	writer := &statusWriter{
		buf:           &outputBuffer,
		statusUpdater: w.statusUpdater,
	}

	// Assign the SAME writer to both stdout and stderr.
	cmd.Stdout = writer
	cmd.Stderr = writer

	// Run() handles the starting, streaming, and waiting automatically.
	if err := cmd.Run(); err != nil {
		output := outputBuffer.String()
		log.Debugf("%s output:\n%s", phaseName, output)
		return fmt.Errorf("%s failed: %w. Output: %s", phaseName, err, output)
	}

	output := outputBuffer.String()
	log.Debugf("%s output:\n%s", phaseName, output)
	return nil
}

// startPodmanWorker starts a detached podman worker container for building images.
// It returns the container name, worker info, and a cleanup function.
func (c *Consumer) startPodmanWorker(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	statusUpdater *statusUpdater,
	log logrus.FieldLogger,
) (*podmanWorker, error) {
	// Check context before starting work
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Use podman image from config (defaults are applied during config loading)
	if c.cfg == nil || c.cfg.ImageBuilderWorker == nil {
		return nil, fmt.Errorf("config or ImageBuilderWorker config is nil")
	}
	podmanImage := c.cfg.ImageBuilderWorker.PodmanImage

	// Create temporary directories for the worker
	tmpDir, err := os.MkdirTemp("", "imagebuild-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temporary directory: %w", err)
	}

	tmpOutDir, err := os.MkdirTemp("", "imagebuild-out-*")
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to create temporary output directory: %w", err)
	}

	baseStorageDir := "/var/tmp/flightctl-builds"

	// This creates a unique, throw-away directory for THIS specific build.
	// It ensures no caching between jobs.
	tmpContainerStorage, err := os.MkdirTemp(baseStorageDir, "storage-*")
	if err != nil {
		return nil, err
	}
	// 1. Create a clean storage.conf on the host
	storageConfPath := filepath.Join(tmpDir, "storage.conf")
	// This config forces overlay and has NO mount_program defined
	storageConfContent := `[storage]
driver = "overlay"
graphroot = "/var/lib/containers"
runroot = "/run/containers/storage"
[storage.options]
# Empty options here is the key. It removes "mount_program".
mount_program = ""
ignore_chown_errors = "true"
`
	//nolint:gosec // G306: 0644 permissions required - file is mounted into container and must be readable by processes inside
	if err := os.WriteFile(storageConfPath, []byte(storageConfContent), 0644); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("failed to write storage.conf: %w", err)
	}

	// Container paths
	containerBuildDir := "/build"
	containerOutDir := "/output"
	containerStorageDir := "/var/lib/containers"

	// Generate a unique container name so we can reference it easily
	imageBuildName := lo.FromPtr(imageBuild.Metadata.Name)
	containerName := fmt.Sprintf("build-worker-%s-%s", orgID.String()[:8], imageBuildName)

	// Start the worker container in detached mode
	log.Info("Starting worker container")
	startArgs := []string{
		"run", "-d", "--rm", // -d for detached, --rm to clean up when killed
		"--name", containerName,
		"--security-opt", "seccomp=unconfined",
		"--security-opt", "label=disable",
		"--storage-driver", "overlay",
		"--net=host",
		"--cgroups=disabled",
		"--userns=host",
		"-v", fmt.Sprintf("%s:%s:Z", tmpDir, containerBuildDir),
		"-v", fmt.Sprintf("%s:%s:Z", tmpOutDir, containerOutDir),
		"-v", fmt.Sprintf("%s:%s:Z", tmpContainerStorage, containerStorageDir),
		"-v", fmt.Sprintf("%s:/etc/containers/storage.conf:Z", storageConfPath),
		"-v", "/dev/null:/dev/null:rw",
		"-v", "/dev/zero:/dev/zero:rw",
		"-v", "/dev/random:/dev/random:rw",
		"-v", "/dev/urandom:/dev/urandom:rw",
		"-v", "/dev/full:/dev/full:rw",
		"-v", "/dev/tty:/dev/tty:rw", // Good practice for build logs
	}

	// Auto-detect and mount entitlement certs if present (for RHEL subscription repos)
	hasEntitlementCerts := false
	if _, err := os.Stat(entitlementCertsPath); err == nil {
		startArgs = append(startArgs, "-v", fmt.Sprintf("%s:%s:ro,Z", entitlementCertsPath, entitlementCertsPath))
		hasEntitlementCerts = true
		log.Debug("Mounting entitlement certificates (auto-detected)")
	}

	startArgs = append(startArgs,
		"--cap-add=SYS_ADMIN",
		podmanImage,
		"sleep", "infinity",
	)

	// Pretty print the command for debugging
	cmdParts := []string{"podman"}
	cmdParts = append(cmdParts, startArgs...)
	cmdStr := strings.Join(cmdParts, " ")
	log.WithField("command", cmdStr).Debug("Executing podman command")

	if out, err := exec.CommandContext(ctx, "podman", startArgs...).CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		os.RemoveAll(tmpOutDir)
		os.RemoveAll(tmpContainerStorage)
		return nil, fmt.Errorf("failed to start worker: %w, output: %s", err, string(out))
	}

	cleanup := func() {
		log.Debug("Cleaning up worker container")
		killCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := exec.CommandContext(killCtx, "podman", "kill", containerName).Run(); err != nil {
			log.WithError(err).Warn("Failed to kill worker container during cleanup")
		}
		os.RemoveAll(tmpDir)
		os.RemoveAll(tmpOutDir)
		os.RemoveAll(tmpContainerStorage)
	}

	return &podmanWorker{
		ContainerName:       containerName,
		TmpDir:              tmpDir,
		TmpOutDir:           tmpOutDir,
		TmpContainerStorage: tmpContainerStorage,
		Cleanup:             cleanup,
		statusUpdater:       statusUpdater,
		HasEntitlementCerts: hasEntitlementCerts,
	}, nil
}

// loginToRegistry logs into a registry using podman login with stdin
// This is used for push operations where authfile doesn't work reliably
func (c *Consumer) loginToRegistry(
	ctx context.Context,
	podmanWorker *podmanWorker,
	registryHostname string,
	username string,
	password string,
	log logrus.FieldLogger,
) error {
	if username == "" || password == "" {
		return nil
	}

	// Validate username to prevent command injection
	// Username should not contain shell metacharacters that could be used for injection
	// Allow alphanumeric, dots, hyphens, underscores, @, and $ (for email-style and system usernames)
	// Block dangerous patterns like command substitution, pipes, and other shell operators
	if strings.ContainsAny(username, ";|&`(){}[]<>\"'\\\n\r\t") {
		return fmt.Errorf("invalid username: contains unsafe characters")
	}
	if len(username) > 256 {
		return fmt.Errorf("invalid username: exceeds maximum length of 256 characters")
	}

	// Validate registryHostname to prevent command injection
	// Registry hostname should not contain shell metacharacters that could be used for injection
	if strings.ContainsAny(registryHostname, ";|&`(){}[]<>\"'\\\n\r\t") {
		return fmt.Errorf("invalid registry hostname: contains unsafe characters")
	}
	if len(registryHostname) > 256 {
		return fmt.Errorf("invalid registry hostname: exceeds maximum length of 256 characters")
	}

	log.WithField("registry", registryHostname).Debug("Logging into registry with podman login")

	// Run podman login inside the container with stdin
	// Format: podman exec -i <container> podman login -u <username> -p <password> <registry>
	// username and registryHostname are validated above to prevent command injection
	//nolint:gosec // G204: Inputs are validated above to prevent command injection. exec.CommandContext uses separate arguments (not shell), making this safe.
	loginCmd := exec.CommandContext(ctx, "podman", "exec", "-i", podmanWorker.ContainerName, "podman", "login", "-u", username, "--password-stdin", registryHostname)

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

// buildImageWithPodman builds the image using podman in a container-in-container setup.
// It creates a manifest list, builds for AMD64 platform, and handles authentication.
func (c *Consumer) buildImageWithPodman(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	containerfileResult *ContainerfileResult,
	podmanWorker *podmanWorker,
	log logrus.FieldLogger,
) error {
	// Check context before starting work
	if err := ctx.Err(); err != nil {
		return err
	}

	spec := imageBuild.Spec

	// Get destination repository to get registry hostname and validate
	destRepo, err := c.mainStore.Repository().Get(ctx, orgID, spec.Destination.Repository)
	if err != nil {
		return fmt.Errorf("failed to get destination repository: %w", err)
	}

	// Validate that the destination repository is of OCI type
	destRepoType, err := destRepo.Spec.Discriminator()
	if err != nil {
		return fmt.Errorf("failed to determine destination repository type: %w", err)
	}
	if destRepoType != string(coredomain.RepoSpecTypeOci) {
		return fmt.Errorf("destination repository %q must be of type 'oci', got %q", spec.Destination.Repository, destRepoType)
	}

	destOciSpec, err := destRepo.Spec.AsOciRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to parse destination OCI repository spec: %w", err)
	}

	// Validate image reference components (defense-in-depth)
	if err := validateImageRefComponents(spec.Destination.ImageName, spec.Destination.ImageTag); err != nil {
		return fmt.Errorf("invalid image reference components: %w", err)
	}

	// ociSpec.Registry is already the hostname (no scheme)
	destRegistryHostname := destOciSpec.Registry
	imageRef := fmt.Sprintf("%s/%s:%s", destRegistryHostname, spec.Destination.ImageName, spec.Destination.ImageTag)

	// Determine platform from ImageBuild status architecture, default to linux/amd64
	platform := "linux/amd64"
	if imageBuild.Status != nil && imageBuild.Status.Architecture != nil && *imageBuild.Status.Architecture != "" {
		platform = *imageBuild.Status.Architecture
	}

	log.WithFields(logrus.Fields{
		"imageRef": imageRef,
		"platform": platform,
	}).Info("Starting podman build")

	// Write Containerfile to temporary directory
	containerfilePath := filepath.Join(podmanWorker.TmpDir, "Containerfile")
	if err := os.WriteFile(containerfilePath, []byte(containerfileResult.Containerfile), 0600); err != nil {
		return fmt.Errorf("failed to write Containerfile: %w", err)
	}

	// Write agent config file to build context (required by COPY instruction, empty if not early binding)
	agentConfigPath := filepath.Join(podmanWorker.TmpDir, "agent-config.yaml")
	agentConfigContent := containerfileResult.AgentConfig
	if agentConfigContent == nil {
		agentConfigContent = []byte{} // Empty placeholder
	}
	if err := os.WriteFile(agentConfigPath, agentConfigContent, 0600); err != nil {
		return fmt.Errorf("failed to write agent-config.yaml: %w", err)
	}

	// Write public key file to build context (required by COPY instruction, empty if no user config)
	publickeyPath := filepath.Join(podmanWorker.TmpDir, "user-publickey.txt")
	publickeyContent := containerfileResult.Publickey
	if publickeyContent == nil {
		publickeyContent = []byte{} // Empty placeholder
	}
	if err := os.WriteFile(publickeyPath, publickeyContent, 0600); err != nil {
		return fmt.Errorf("failed to write user-publickey.txt: %w", err)
	}

	// Get source repository credentials for pulling the base image (FROM)
	repo, err := c.mainStore.Repository().Get(ctx, orgID, spec.Source.Repository)
	if err != nil {
		return fmt.Errorf("failed to load source repository: %w", err)
	}

	// Validate that the source repository is of OCI type
	repoType, err := repo.Spec.Discriminator()
	if err != nil {
		return fmt.Errorf("failed to determine source repository type: %w", err)
	}
	if repoType != string(coredomain.RepoSpecTypeOci) {
		return fmt.Errorf("source repository %q must be of type 'oci', got %q", spec.Source.Repository, repoType)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	// Container paths
	containerBuildDir := "/build"

	// ociSpec.Registry is already the hostname (no scheme)
	sourceRegistryHostname := ociSpec.Registry

	// Login to source registry using podman login with stdin
	// This is used to pull the base image during build
	if ociSpec.OciAuth != nil {
		dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			if err := c.loginToRegistry(ctx, podmanWorker, sourceRegistryHostname, dockerAuth.Username, dockerAuth.Password, log); err != nil {
				return fmt.Errorf("failed to login to source registry: %w", err)
			}
		}
	}

	containerContainerfilePath := filepath.Join(containerBuildDir, "Containerfile")

	// ---------------------------------------------------------
	// PHASE 1: BUILD (Manifest + Build)
	// ---------------------------------------------------------
	log.Info("Phase: Build Started")

	// A. Create Manifest (ignore error if it already exists)
	if err := podmanWorker.runInWorker(ctx, log, "manifest create", nil, "manifest", "create", imageRef); err != nil {
		// Manifest might already exist, which is okay
		if !strings.Contains(err.Error(), "already exists") {
			return err
		}
		log.Debug("Manifest list already exists, continuing")
	}

	// B. Build
	// Authentication is handled via podman login above
	// Build arguments are passed via --build-arg for safer execution (no shell injection risk)
	args := containerfileResult.BuildArgs
	podmanBuildArgs := []string{
		"build",
		"--platform", platform,
		"--manifest", imageRef,
		"--build-arg", fmt.Sprintf("REGISTRY_HOSTNAME=%s", args.RegistryHostname),
		"--build-arg", fmt.Sprintf("IMAGE_NAME=%s", args.ImageName),
		"--build-arg", fmt.Sprintf("IMAGE_TAG=%s", args.ImageTag),
		"--build-arg", fmt.Sprintf("EARLY_BINDING=%t", args.EarlyBinding),
		"--build-arg", fmt.Sprintf("HAS_USER_CONFIG=%t", args.HasUserConfig),
		"--build-arg", fmt.Sprintf("USERNAME=%s", args.Username),
		"--build-arg", fmt.Sprintf("AGENT_CONFIG_DEST_PATH=%s", args.AgentConfigDestPath),
		"--build-arg", fmt.Sprintf("RPM_REPO_URL=%s", args.RPMRepoURL),
	}

	// Mount entitlement certs into the build if available (for RHEL subscription repos)
	if podmanWorker.HasEntitlementCerts {
		podmanBuildArgs = append(podmanBuildArgs, "--volume", fmt.Sprintf("%s:%s:ro", entitlementCertsPath, entitlementCertsPath))
	}

	podmanBuildArgs = append(podmanBuildArgs,
		"-f", containerContainerfilePath,
		containerBuildDir,
	)

	if err := podmanWorker.runInWorker(ctx, log, "build", nil, podmanBuildArgs...); err != nil {
		return err
	}

	log.Info("Phase: Build Completed")

	return nil
}

// pushImageWithPodman pushes the built image to the destination registry.
// It returns the image reference that was pushed.
func (c *Consumer) pushImageWithPodman(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	podmanWorker *podmanWorker,
	log logrus.FieldLogger,
) (string, error) {
	// Check context before starting work
	if err := ctx.Err(); err != nil {
		return "", err
	}

	spec := imageBuild.Spec

	// Get destination repository for authentication and to get registry hostname
	repo, err := c.mainStore.Repository().Get(ctx, orgID, spec.Destination.Repository)
	if err != nil {
		return "", fmt.Errorf("failed to load destination repository: %w", err)
	}

	// Validate that the destination repository is of OCI type
	repoType, err := repo.Spec.Discriminator()
	if err != nil {
		return "", fmt.Errorf("failed to determine destination repository type: %w", err)
	}
	if repoType != string(coredomain.RepoSpecTypeOci) {
		return "", fmt.Errorf("destination repository %q must be of type 'oci', got %q", spec.Destination.Repository, repoType)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return "", fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	// Validate image reference components (defense-in-depth)
	if err := validateImageRefComponents(spec.Destination.ImageName, spec.Destination.ImageTag); err != nil {
		return "", fmt.Errorf("invalid image reference components: %w", err)
	}

	// ociSpec.Registry is already the hostname (no scheme)
	destRegistryHostname := ociSpec.Registry
	imageRef := fmt.Sprintf("%s/%s:%s", destRegistryHostname, spec.Destination.ImageName, spec.Destination.ImageTag)

	// Login to registry using podman login with stdin
	// This is more reliable than authfile for push operations
	if ociSpec.OciAuth != nil {
		dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			if err := c.loginToRegistry(ctx, podmanWorker, destRegistryHostname, dockerAuth.Username, dockerAuth.Password, log); err != nil {
				return "", fmt.Errorf("failed to login to destination registry: %w", err)
			}
		}
	}

	// ---------------------------------------------------------
	// PHASE: PUSH (Explicit State Transition)
	// ---------------------------------------------------------
	log.Info("Phase: Push Started")

	// Push
	// Note: We push the MANIFEST (imageRef), which pushes all layers
	// Authentication is handled via podman login above
	pushArgs := []string{"push", imageRef}
	if err := podmanWorker.runInWorker(ctx, log, "push", nil, pushArgs...); err != nil {
		return "", err
	}

	log.WithField("imageRef", imageRef).Info("Phase: Push Completed - Image pushed successfully")

	return imageRef, nil
}
