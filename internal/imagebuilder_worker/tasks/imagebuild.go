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
	"text/template"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	api "github.com/flightctl/flightctl/api/imagebuilder/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
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
	// Containerfile is the generated Containerfile content
	Containerfile string
	// AgentConfig contains the full agent config.yaml content (for early binding)
	// This includes: client-certificate-data, client-key-data, certificate-authority-data, server URL
	AgentConfig []byte
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
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	// Check current state - only process if Pending (or has no Ready condition)
	// We only lock resources that are in Pending state to avoid stealing work from other processes
	var readyCondition *api.ImageBuildCondition
	if imageBuild.Status.Conditions != nil {
		readyCondition = api.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, api.ImageBuildConditionTypeReady)
	}
	if readyCondition != nil {
		reason := readyCondition.Reason
		// Skip if already completed or failed
		if reason == string(api.ImageBuildConditionReasonCompleted) || reason == string(api.ImageBuildConditionReasonFailed) {
			log.Infof("ImageBuild %q already in terminal state %q, skipping", imageBuildName, reason)
			return nil
		}
		// Skip if already Building - another process is handling it
		if reason == string(api.ImageBuildConditionReasonBuilding) {
			log.Infof("ImageBuild %q is already being processed (Building), skipping", imageBuildName)
			return nil
		}
		// Skip if Pushing - another process is handling it
		if reason == string(api.ImageBuildConditionReasonPushing) {
			log.Infof("ImageBuild %q is already being processed (Pushing), skipping", imageBuildName)
			return nil
		}
		// Only proceed if Pending - if it's any other state, skip (shouldn't happen, but defensive)
		if reason != string(api.ImageBuildConditionReasonPending) {
			log.Warnf("ImageBuild %q is in unexpected state %q (expected Pending), skipping", imageBuildName, reason)
			return nil
		}
	}
	// If no Ready condition exists, treat as Pending and proceed

	// Lock the build: atomically transition from Pending to Building state using resource_version
	// This ensures only one process can start processing
	now := time.Now().UTC()
	buildingCondition := api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusFalse,
		Reason:             string(api.ImageBuildConditionReasonBuilding),
		Message:            "Build is in progress",
		LastTransitionTime: now,
	}

	// Prepare status with Building condition
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}
	if imageBuild.Status.Conditions == nil {
		imageBuild.Status.Conditions = &[]api.ImageBuildCondition{}
	}
	api.SetImageBuildStatusCondition(imageBuild.Status.Conditions, buildingCondition)

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

	// Start status updater goroutine - this is the single writer for all status updates
	// It handles both LastSeen (periodic) and condition updates (on-demand)
	statusUpdater, cleanupStatusUpdater := startStatusUpdater(ctx, c.imageBuilderService.ImageBuild(), orgID, imageBuildName, c.cfg, log)
	defer cleanupStatusUpdater()

	// Step 1: Generate Containerfile
	log.Info("Generating Containerfile for image build")
	containerfileResult, err := c.generateContainerfile(ctx, orgID, imageBuild, log)
	if err != nil {
		failedTime := time.Now().UTC()
		failedCondition := api.ImageBuildCondition{
			Type:               api.ImageBuildConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageBuildConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: failedTime,
		}
		statusUpdater.updateCondition(failedCondition)
		return fmt.Errorf("failed to generate Containerfile: %w", err)
	}

	log.WithField("containerfile_length", len(containerfileResult.Containerfile)).Info("Containerfile generated successfully")
	log.Debug("Generated Containerfile: ", containerfileResult.Containerfile)

	// Step 2: Start podman worker container
	podmanWorker, err := c.startPodmanWorker(ctx, orgID, imageBuild, statusUpdater, log)
	if err != nil {
		failedTime := time.Now().UTC()
		failedCondition := api.ImageBuildCondition{
			Type:               api.ImageBuildConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageBuildConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: failedTime,
		}
		statusUpdater.updateCondition(failedCondition)
		return fmt.Errorf("failed to start podman worker: %w", err)
	}
	defer podmanWorker.Cleanup()

	// Step 3: Build with podman container in container
	err = c.buildImageWithPodman(ctx, orgID, imageBuild, containerfileResult, podmanWorker, log)
	if err != nil {
		failedTime := time.Now().UTC()
		failedCondition := api.ImageBuildCondition{
			Type:               api.ImageBuildConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageBuildConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: failedTime,
		}
		statusUpdater.updateCondition(failedCondition)
		return fmt.Errorf("failed to build image with podman: %w", err)
	}

	// Step 4: Push image to registry
	imageRef, err := c.pushImageWithPodman(ctx, orgID, imageBuild, podmanWorker, log)
	if err != nil {
		failedTime := time.Now().UTC()
		failedCondition := api.ImageBuildCondition{
			Type:               api.ImageBuildConditionTypeReady,
			Status:             v1beta1.ConditionStatusFalse,
			Reason:             string(api.ImageBuildConditionReasonFailed),
			Message:            err.Error(),
			LastTransitionTime: failedTime,
		}
		statusUpdater.updateCondition(failedCondition)
		return fmt.Errorf("failed to push image with podman: %w", err)
	}

	// Update ImageBuild status with the pushed image reference and mark as Completed
	statusUpdater.updateImageReference(imageRef)

	// Mark as Completed
	now = time.Now().UTC()
	completedCondition := api.ImageBuildCondition{
		Type:               api.ImageBuildConditionTypeReady,
		Status:             v1beta1.ConditionStatusTrue,
		Reason:             string(api.ImageBuildConditionReasonCompleted),
		Message:            "Build completed successfully",
		LastTransitionTime: now,
	}
	statusUpdater.updateCondition(completedCondition)

	log.Info("ImageBuild marked as Completed")
	return nil
}

// statusUpdateRequest represents a request to update the ImageBuild status
type statusUpdateRequest struct {
	Condition      *api.ImageBuildCondition
	LastSeen       *time.Time
	ImageReference *string
}

// statusUpdater manages all status updates for an ImageBuild, ensuring atomic updates
// and preventing race conditions between LastSeen and condition updates.
// It also tracks task outputs and only updates LastSeen when new data is received.
type statusUpdater struct {
	imageBuildService imagebuilderapi.ImageBuildService
	orgID             uuid.UUID
	imageBuildName    string
	updateChan        chan statusUpdateRequest
	outputChan        chan []byte // Central channel for all task outputs
	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	log               logrus.FieldLogger
}

// startStatusUpdater starts a goroutine that is the single writer for ImageBuild status updates.
// It receives condition updates via a channel and periodically updates LastSeen.
// Returns the updater and a cleanup function.
func startStatusUpdater(
	ctx context.Context,
	imageBuildService imagebuilderapi.ImageBuildService,
	orgID uuid.UUID,
	imageBuildName string,
	cfg *config.Config,
	log logrus.FieldLogger,
) (*statusUpdater, func()) {
	updaterCtx, updaterCancel := context.WithCancel(ctx)

	updater := &statusUpdater{
		imageBuildService: imageBuildService,
		orgID:             orgID,
		imageBuildName:    imageBuildName,
		updateChan:        make(chan statusUpdateRequest), // Unbuffered channel - blocks until processed
		outputChan:        make(chan []byte, 100),         // Buffered channel for task outputs
		ctx:               updaterCtx,
		cancel:            updaterCancel,
		log:               log,
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
func (u *statusUpdater) run(cfg *config.Config) {
	defer u.wg.Done()

	// Use LastSeenUpdateInterval from config (defaults are applied during config loading)
	if cfg == nil || cfg.ImageBuilderWorker == nil {
		u.log.Error("Config or ImageBuilderWorker config is nil, cannot update status")
		return
	}
	updateInterval := time.Duration(cfg.ImageBuilderWorker.LastSeenUpdateInterval)
	ticker := time.NewTicker(updateInterval)
	defer ticker.Stop()

	// Track pending updates
	var pendingCondition *api.ImageBuildCondition
	var pendingImageReference *string
	lastSeenUpdateTime := time.Now().UTC()

	// Track the last time output was received - updated when new output arrives
	var lastOutputTime *time.Time
	// Track the last LastSeen value we set in the database
	var lastSetLastSeen *time.Time

	for {
		select {
		case <-u.ctx.Done():
			return
		case <-ticker.C:
			// Periodic LastSeen update - only if we have new output time and haven't set it yet
			if lastOutputTime != nil {
				// Only update if this is a different time than what we last set
				if lastSetLastSeen == nil || !lastOutputTime.Equal(*lastSetLastSeen) {
					lastSeenUpdateTime = *lastOutputTime
					// Store a copy of the time we're setting
					lastSetLastSeenCopy := *lastOutputTime
					lastSetLastSeen = &lastSetLastSeenCopy
					u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, pendingImageReference)
					pendingCondition = nil      // Clear after update
					pendingImageReference = nil // Clear after update
				}
			}
		case output := <-u.outputChan:
			// Task output received - update local variable with current time
			now := time.Now().UTC()
			lastOutputTime = &now
			// Log output for debugging (can be removed or made conditional)
			u.log.Debugf("Task output: %s", string(output))
		case req := <-u.updateChan:
			// Status update requested
			if req.Condition != nil {
				pendingCondition = req.Condition
			}
			if req.LastSeen != nil {
				lastSeenUpdateTime = *req.LastSeen
			}
			if req.ImageReference != nil {
				pendingImageReference = req.ImageReference
			}
			// Update immediately when condition or image reference changes
			if req.Condition != nil || req.ImageReference != nil {
				u.updateStatus(u.ctx, pendingCondition, &lastSeenUpdateTime, pendingImageReference)
				pendingCondition = nil      // Clear after update
				pendingImageReference = nil // Clear after update
			}
		}
	}
}

// updateStatus performs the actual database update, merging conditions, LastSeen, and ImageReference
func (u *statusUpdater) updateStatus(ctx context.Context, condition *api.ImageBuildCondition, lastSeen *time.Time, imageReference *string) {
	// Load current status from database
	imageBuild, status := u.imageBuildService.Get(ctx, u.orgID, u.imageBuildName, false)
	if imageBuild == nil || !imagebuilderapi.IsStatusOK(status) {
		u.log.WithField("status", status).Warn("Failed to load ImageBuild for status update")
		return
	}

	// Initialize status if needed
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	// Update LastSeen
	if lastSeen != nil {
		imageBuild.Status.LastSeen = lastSeen
	}

	// Update condition if provided
	if condition != nil {
		if imageBuild.Status.Conditions == nil {
			imageBuild.Status.Conditions = &[]api.ImageBuildCondition{}
		}

		// Use helper function to set condition, keeping ImageBuildCondition type
		api.SetImageBuildStatusCondition(imageBuild.Status.Conditions, *condition)
	}

	// Update ImageReference if provided
	if imageReference != nil {
		imageBuild.Status.ImageReference = imageReference
	}

	// Write updated status atomically
	_, err := u.imageBuildService.UpdateStatus(ctx, u.orgID, imageBuild)
	if err != nil {
		u.log.WithError(err).Warn("Failed to update ImageBuild status")
	}
}

// updateCondition sends a condition update request to the updater goroutine
func (u *statusUpdater) updateCondition(condition api.ImageBuildCondition) {
	select {
	case u.updateChan <- statusUpdateRequest{Condition: &condition}:
	case <-u.ctx.Done():
		// Context cancelled, ignore update
	}
}

// updateImageReference sends an image reference update request to the updater goroutine
func (u *statusUpdater) updateImageReference(imageReference string) {
	select {
	case u.updateChan <- statusUpdateRequest{ImageReference: &imageReference}:
	case <-u.ctx.Done():
		// Context cancelled, ignore update
	}
}

// reportOutput sends task output to the central output handler
// This marks that progress has been made and LastSeen should be updated
func (u *statusUpdater) reportOutput(output []byte) {
	select {
	case u.outputChan <- output:
	case <-u.ctx.Done():
		// Context cancelled, ignore output
	}
}

// containerfileData holds the data for rendering the Containerfile template
type containerfileData struct {
	RegistryHostname    string
	ImageName           string
	ImageTag            string
	EarlyBinding        bool
	AgentConfig         string
	AgentConfigDestPath string
	HeredocDelimiter    string
	PublicKeyDelimiter  string
	Username            string
	Publickey           string
	HasUserConfig       bool
}

// EnrollmentCredentialGenerator is an interface for generating enrollment credentials
// This allows for easier testing by mocking the service handler
type EnrollmentCredentialGenerator interface {
	GenerateEnrollmentCredential(ctx context.Context, orgId uuid.UUID, baseName string, ownerKind string, ownerName string) (*crypto.EnrollmentCredential, v1beta1.Status)
}

// GenerateContainerfile generates a Containerfile from an ImageBuild spec
// This function is exported for testing purposes
func GenerateContainerfile(
	ctx context.Context,
	mainStore store.Store,
	credentialGenerator EnrollmentCredentialGenerator,
	orgID uuid.UUID,
	imageBuild *api.ImageBuild,
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
	imageBuild *api.ImageBuild,
	log logrus.FieldLogger,
) (*ContainerfileResult, error) {
	return c.generateContainerfileWithGenerator(ctx, orgID, imageBuild, c.serviceHandler, log)
}

// generateContainerfileWithGenerator generates a Containerfile from an ImageBuild spec
// credentialGenerator can be provided for testing with mocks
func (c *Consumer) generateContainerfileWithGenerator(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *api.ImageBuild,
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
	if repoType != string(v1beta1.RepoSpecTypeOci) {
		return nil, fmt.Errorf("repository %q must be of type 'oci', got %q", spec.Source.Repository, repoType)
	}

	ociSpec, err := repo.Spec.GetOciRepoSpec()
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

	result := &ContainerfileResult{}

	// Generate a unique heredoc delimiter to avoid conflicts with config content
	heredocDelimiter := fmt.Sprintf("FLIGHTCTL_CONFIG_%s", uuid.NewString()[:8])

	// Generate a unique heredoc delimiter for public key if user config is provided
	publicKeyDelimiter := ""
	if spec.UserConfiguration != nil {
		publicKeyDelimiter = fmt.Sprintf("FLIGHTCTL_PUBKEY_%s", uuid.NewString()[:8])
	}

	// Prepare template data
	data := containerfileData{
		RegistryHostname:    registryHostname,
		ImageName:           spec.Source.ImageName,
		ImageTag:            spec.Source.ImageTag,
		EarlyBinding:        bindingType == string(api.BindingTypeEarly),
		AgentConfigDestPath: agentConfigPath,
		HeredocDelimiter:    heredocDelimiter,
		PublicKeyDelimiter:  publicKeyDelimiter,
		HasUserConfig:       spec.UserConfiguration != nil,
	}

	// Add user configuration if provided
	if spec.UserConfiguration != nil {
		data.Username = spec.UserConfiguration.Username
		data.Publickey = spec.UserConfiguration.Publickey
	}

	// Handle early binding - generate enrollment credentials
	if data.EarlyBinding {
		// Generate a unique name for this build's enrollment credentials
		imageBuildName := lo.FromPtr(imageBuild.Metadata.Name)
		credentialName := fmt.Sprintf("imagebuild-%s-%s", imageBuildName, orgID.String()[:8])

		agentConfig, err := c.generateAgentConfigWithGenerator(ctx, orgID, credentialName, imageBuildName, credentialGenerator)
		if err != nil {
			return nil, fmt.Errorf("failed to generate agent config for early binding: %w", err)
		}

		// Store agent config as string for template rendering
		data.AgentConfig = string(agentConfig)
		result.AgentConfig = agentConfig
		log.WithField("credentialName", credentialName).Debug("Generated agent config for early binding")
	}

	// Render the Containerfile template
	containerfile, err := renderContainerfileTemplate(data)
	if err != nil {
		return nil, fmt.Errorf("failed to render Containerfile template: %w", err)
	}

	result.Containerfile = containerfile
	return result, nil
}

// generateAgentConfigWithGenerator generates a complete agent config.yaml for early binding.
// credentialGenerator can be provided for testing with mocks
func (c *Consumer) generateAgentConfigWithGenerator(ctx context.Context, orgID uuid.UUID, name string, imageBuildName string, credentialGenerator EnrollmentCredentialGenerator) ([]byte, error) {
	// Generate enrollment credential using the credential generator
	// This will create a CSR, auto-approve it, sign it, and return the credential
	// The CSR owner is set to the ImageBuild resource for traceability
	credential, status := credentialGenerator.GenerateEnrollmentCredential(ctx, orgID, name, string(api.ResourceKindImageBuild), imageBuildName)
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

// renderContainerfileTemplate renders the Containerfile template with the given data
func renderContainerfileTemplate(data containerfileData) (string, error) {
	tmpl, err := template.New("containerfile").Parse(containerfileTemplate)
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return buf.String(), nil
}

// podmanWorker holds information about a running podman worker container
type podmanWorker struct {
	ContainerName       string
	TmpDir              string
	TmpOutDir           string
	TmpContainerStorage string
	Cleanup             func()
	statusUpdater       *statusUpdater // Reference to status updater for output reporting
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
		w.statusUpdater.reportOutput(p)
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
	imageBuild *api.ImageBuild,
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
		"--cap-add=SYS_ADMIN",
		podmanImage,
		"sleep", "infinity",
	}

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
	imageBuild *api.ImageBuild,
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
	if destRepoType != string(v1beta1.RepoSpecTypeOci) {
		return fmt.Errorf("destination repository %q must be of type 'oci', got %q", spec.Destination.Repository, destRepoType)
	}

	destOciSpec, err := destRepo.Spec.GetOciRepoSpec()
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
	if repoType != string(v1beta1.RepoSpecTypeOci) {
		return fmt.Errorf("source repository %q must be of type 'oci', got %q", spec.Source.Repository, repoType)
	}

	ociSpec, err := repo.Spec.GetOciRepoSpec()
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
	buildArgs := []string{
		"build",
		"--platform", platform,
		"--manifest", imageRef,
		"-f", containerContainerfilePath,
		containerBuildDir,
	}

	if err := podmanWorker.runInWorker(ctx, log, "build", nil, buildArgs...); err != nil {
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
	imageBuild *api.ImageBuild,
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
	if repoType != string(v1beta1.RepoSpecTypeOci) {
		return "", fmt.Errorf("destination repository %q must be of type 'oci', got %q", spec.Destination.Repository, repoType)
	}

	ociSpec, err := repo.Spec.GetOciRepoSpec()
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
