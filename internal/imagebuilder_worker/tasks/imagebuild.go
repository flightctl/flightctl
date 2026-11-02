package tasks

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
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
	"github.com/flightctl/flightctl/internal/imagebuilder_worker/vm"
	"github.com/flightctl/flightctl/internal/instrumentation/encryption"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/flightctl/flightctl/internal/service"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	trustifyv2 "github.com/flightctl/flightctl/internal/trustify/v2"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const (
	// agentConfigPath is the destination path for the agent config in the image
	agentConfigPath = "/etc/flightctl/config.yaml"

	// Registry credentials stay in guest-local files outside the build context.
	// Separate source and destination files support different credentials on one registry.
	containerAuthFile   = "/tmp/source-auth.json"
	destinationAuthFile = "/tmp/destination-auth.json"
	serviceAuthFile     = "/tmp/service-auth.json"
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

// shouldProcessImageBuild checks if the ImageBuild is in a state that should be processed.
// Returns (true, nil) if processing should proceed, or (false, err) if it should be skipped.
func (c *Consumer) shouldProcessImageBuild(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, name string, log logrus.FieldLogger) (bool, error) {
	if imageBuild.Status.Conditions == nil {
		return true, nil
	}
	readyCondition := domain.FindImageBuildStatusCondition(*imageBuild.Status.Conditions, domain.ImageBuildConditionTypeReady)
	if readyCondition == nil {
		return true, nil
	}

	reason := readyCondition.Reason

	switch reason {
	case string(domain.ImageBuildConditionReasonCompleted),
		string(domain.ImageBuildConditionReasonFailed),
		string(domain.ImageBuildConditionReasonCanceled):
		log.Infof("ImageBuild %q already in terminal state %q, skipping", name, reason)
		return false, nil

	case string(domain.ImageBuildConditionReasonBuilding):
		log.Infof("ImageBuild %q is already being processed (Building), skipping", name)
		return false, nil

	case string(domain.ImageBuildConditionReasonPushing):
		log.Infof("ImageBuild %q is already being processed (Pushing), skipping", name)
		return false, nil

	case string(domain.ImageBuildConditionReasonGeneratingSBOM):
		log.Infof("ImageBuild %q is already being processed (GeneratingSBOM), skipping", name)
		return false, nil

	case string(domain.ImageBuildConditionReasonCanceling):
		log.Infof("ImageBuild %q was canceled before processing started, completing cancellation", name)
		if err := c.markImageBuildAsCanceled(ctx, orgID, imageBuild, log); err != nil {
			return false, fmt.Errorf("failed to mark ImageBuild as canceled: %w", err)
		}
		return false, nil

	case string(domain.ImageBuildConditionReasonPending):
		return true, nil

	default:
		log.Warnf("ImageBuild %q is in unexpected state %q (expected Pending), skipping", name, reason)
		return false, nil
	}
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

	proceed, err := c.shouldProcessImageBuild(ctx, orgID, imageBuild, imageBuildName, log)
	if !proceed {
		return err
	}

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
	_, err = c.imageBuilderService.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
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

	// Step 2: Start task VM
	podmanWorker, err := c.startPodmanWorker(buildCtx, orgID, imageBuild, statusUpdater, log)
	if err != nil {
		if c.handleBuildError(ctx, orgID, imageBuildName, err, statusUpdater, log) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to start build VM: %w", err)
	}
	defer podmanWorker.Cleanup()
	handleExecutionError := func(err error) bool {
		_ = podmanWorker.Session.Close()
		return c.handleBuildError(ctx, orgID, imageBuildName, err, statusUpdater, log)
	}

	// Step 2b: Install CA certificates for source and destination registries
	// Done once at startup so all subsequent podman operations (login, build, push)
	// automatically trust these CAs via /etc/containers/certs.d/
	for _, repoRef := range []string{imageBuild.Spec.Source.Repository, imageBuild.Spec.Destination.Repository} {
		ociSpec, err := c.getOciRepoSpec(buildCtx, orgID, repoRef, "ca-setup")
		if err != nil {
			err = fmt.Errorf("failed to get OCI spec for %q: %w", repoRef, err)
			if handleExecutionError(err) {
				return nil
			}
			return err
		}
		if err := installCACertInWorker(buildCtx, ociSpec.CaCrt, podmanWorker.Session, ociSpec.Registry, log); err != nil {
			err = fmt.Errorf("failed to install CA cert for %q: %w", repoRef, err)
			if handleExecutionError(err) {
				return nil
			}
			return err
		}
	}

	// Step 3: Build with Podman inside the guest
	err = c.buildImageWithPodman(buildCtx, orgID, imageBuild, containerfileResult, podmanWorker, log)
	if err != nil {
		if handleExecutionError(err) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to build image with podman: %w", err)
	}

	// Step 4: Push image to registry
	imageRef, manifestDigest, err := c.pushImageWithPodman(buildCtx, orgID, imageBuild, podmanWorker, log)
	if err != nil {
		if handleExecutionError(err) {
			return nil // Cancellation handled
		}
		return fmt.Errorf("failed to push image with podman: %w", err)
	}

	if manifestDigest == "" {
		log.Error("Push succeeded but manifest digest is empty")
		return fmt.Errorf("push succeeded but manifest digest is empty")
	}

	// Update ImageBuild status with the pushed image reference and manifest digest
	statusUpdater.UpdateImageReference(imageRef, manifestDigest)

	// Step 5: Generate and distribute SBOM when enabled and a destination is configured
	if c.shouldRunSBOMPipeline() {
		c.processSBOM(buildCtx, ctx, orgID, imageBuild, imageRef, manifestDigest, podmanWorker, statusUpdater, log)
	}
	if err := buildCtx.Err(); err != nil {
		if handleExecutionError(err) {
			return nil
		}
		return err
	}

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

// processSBOM handles SBOM generation and distribution.
// SBOM failures are non-fatal - the build succeeds even if SBOM generation fails.
func (c *Consumer) processSBOM(
	buildCtx context.Context,
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	imageRef string,
	manifestDigest string,
	podmanWorker *podmanWorker,
	statusUpdater *statusUpdater,
	log logrus.FieldLogger,
) {
	// Update status to GeneratingSBOM
	now := time.Now().UTC()
	sbomCondition := domain.ImageBuildCondition{
		Type:               domain.ImageBuildConditionTypeReady,
		Status:             domain.ConditionStatusFalse,
		Reason:             string(domain.ImageBuildConditionReasonGeneratingSBOM),
		Message:            "Scanning for vulnerabilities",
		LastTransitionTime: now,
	}
	statusUpdater.UpdateCondition(sbomCondition)

	log.Info("Phase: SBOM Generation Started")

	// Generate SBOM
	sbomResult, err := c.generateSBOM(buildCtx, imageRef, manifestDigest, podmanWorker, log)
	if err != nil {
		log.WithError(err).Warn("SBOM generation failed (non-fatal)")
		return
	}

	log.WithFields(logrus.Fields{
		"sbomPath":    sbomResult.SBOMPath,
		"imageDigest": sbomResult.ImageDigest,
	}).Info("SBOM generated successfully")

	// Push SBOM to OCI registry as referrer (if enabled)
	if c.shouldPushSBOMToRegistry() {
		if err := c.pushSBOMAsReferrer(buildCtx, orgID, imageBuild, sbomResult, statusUpdater, log); err != nil {
			log.WithError(err).Warn("SBOM push to registry failed (non-fatal)")
		}
	}

	// Upload SBOM to Trustify (if enabled and the backend requires SBOM upload)
	if c.shouldUploadSBOMToTrustify() && c.cfg.VulnerabilityReporting != nil &&
		c.cfg.VulnerabilityReporting.Enabled &&
		vulnerability.RequiresSBOMUpload(c.cfg.VulnerabilityReporting.EffectiveBackend()) {
		trustifyClient, err := trustifyv2.NewVulnerabilityClient(ctx, c.cfg.VulnerabilityReporting.Trustify)
		if err != nil {
			log.WithError(err).Warn("Failed to create Trustify client for SBOM upload (non-fatal)")
		} else if trustifyClient != nil {
			if err := c.uploadSBOMToTrustify(buildCtx, trustifyClient, sbomResult, log); err != nil {
				log.WithError(err).Warn("SBOM upload to Trustify failed (non-fatal)")
			}
		}
	}

	log.Info("Phase: SBOM Generation Completed")
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
	InstallOnboarding   bool
	RPMRepoAdd          bool
	RPMRepoAddURL       string
	RPMRepoEnable       string
	DNFTimeout          int
	DNFRetries          int
	DNFSkipUnavailable  bool
}

// getRPMRepoAdd returns whether to add the RPM repo via dnf config-manager --add-repo.
// Defaults to true (upstream behavior) when not explicitly configured.
func (c *Consumer) getRPMRepoAdd() bool {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.RPMRepoAdd != nil {
		return *c.cfg.ImageBuilderWorker.RPMRepoAdd
	}
	return true
}

// getRPMRepoAddURL returns the RPM repo URL to add, falling back to default if not configured
func (c *Consumer) getRPMRepoAddURL() string {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.RPMRepoURL != "" {
		return c.cfg.ImageBuilderWorker.RPMRepoURL
	}
	return config.NewDefaultImageBuilderWorkerConfig().RPMRepoURL
}

// getRPMRepoEnable returns the RPM repo name to enable via --enablerepo, or empty string
func (c *Consumer) getRPMRepoEnable() string {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil {
		return c.cfg.ImageBuilderWorker.RPMRepoEnable
	}
	return ""
}

// getDNFTimeout returns the DNF timeout in seconds, defaulting to 5
func (c *Consumer) getDNFTimeout() int {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.DNFTimeout != nil {
		return *c.cfg.ImageBuilderWorker.DNFTimeout
	}
	return *config.NewDefaultImageBuilderWorkerConfig().DNFTimeout
}

// getDNFRetries returns the DNF retry count, defaulting to 0
func (c *Consumer) getDNFRetries() int {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.DNFRetries != nil {
		return *c.cfg.ImageBuilderWorker.DNFRetries
	}
	return *config.NewDefaultImageBuilderWorkerConfig().DNFRetries
}

// getDNFSkipUnavailable returns whether to skip unavailable packages, defaulting to true
func (c *Consumer) getDNFSkipUnavailable() bool {
	if c.cfg != nil && c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.DNFSkipUnavailable != nil {
		return *c.cfg.ImageBuilderWorker.DNFSkipUnavailable
	}
	return *config.NewDefaultImageBuilderWorkerConfig().DNFSkipUnavailable
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
	repositories repositoryservice.Service,
	credentialGenerator EnrollmentCredentialGenerator,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	log logrus.FieldLogger,
) (*ContainerfileResult, error) {
	// Create a temporary consumer for testing purposes
	c := &Consumer{
		repositories: repositories,
		log:          log,
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
	repo, status := c.repositories.GetRepository(ctx, orgID, spec.Source.Repository)
	if err := statusToErr(status); err != nil {
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
	installOnboarding := spec.Onboarding != nil && *spec.Onboarding

	// Prepare build arguments (passed via --build-arg to podman build)
	buildArgs := containerfileBuildArgs{
		RegistryHostname:    registryHostname,
		ImageName:           spec.Source.ImageName,
		ImageTag:            spec.Source.ImageTag,
		EarlyBinding:        isEarlyBinding,
		AgentConfigDestPath: agentConfigPath,
		HasUserConfig:       hasUserConfig,
		InstallOnboarding:   installOnboarding,
		RPMRepoAdd:          c.getRPMRepoAdd(),
		RPMRepoAddURL:       c.getRPMRepoAddURL(),
		RPMRepoEnable:       c.getRPMRepoEnable(),
		DNFTimeout:          c.getDNFTimeout(),
		DNFRetries:          c.getDNFRetries(),
		DNFSkipUnavailable:  c.getDNFSkipUnavailable(),
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

// podmanWorker holds information about a running task VM
type podmanWorker struct {
	Session             *vm.Session
	TmpDir              string
	TmpOutDir           string
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

// runInWorker streams the guest Podman command output to the status updater.
func (w *podmanWorker) runInWorker(ctx context.Context, log logrus.FieldLogger, phaseName string, envVars map[string]string, args ...string) error {
	var output bytes.Buffer
	writer := &statusWriter{buf: &output, statusUpdater: w.statusUpdater}
	if err := w.Session.Run(ctx, append([]string{"podman"}, args...), envVars, nil, writer); err != nil {
		return fmt.Errorf("%s failed: %w", phaseName, err)
	}
	return nil
}

// startPodmanWorker starts a disposable VM for one image build.
func (c *Consumer) startPodmanWorker(ctx context.Context, orgID uuid.UUID, imageBuild *domain.ImageBuild, statusUpdater *statusUpdater, log logrus.FieldLogger) (*podmanWorker, error) {
	if c.cfg == nil || c.cfg.ImageBuilderWorker == nil || c.cfg.ImageBuilderWorker.VM == nil {
		return nil, fmt.Errorf("VM configuration is required")
	}
	input, err := os.MkdirTemp("", "imagebuild-*")
	if err != nil {
		return nil, err
	}
	output, err := os.MkdirTemp("", "imagebuild-out-*")
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
	cleanup := func() { _ = session.Close(); _ = os.RemoveAll(input); _ = os.RemoveAll(output) }
	worker := &podmanWorker{Session: session, TmpDir: input, TmpOutDir: output, Cleanup: cleanup, statusUpdater: statusUpdater}
	var images []string
	if c.shouldRunSBOMPipeline() {
		images = []string{c.cfg.ImageBuilderWorker.EffectiveSyftImage()}
	}
	worker.HasEntitlementCerts, err = c.stageGuestAssets(ctx, session, images)
	if err != nil {
		cleanup()
		return nil, err
	}
	return worker, nil
}

// installCACertInWorker trusts a registry CA inside this task's guest.
func installCACertInWorker(ctx context.Context, caCrt *string, session *vm.Session, registryHostname string, log logrus.FieldLogger) error {
	if caCrt == nil || *caCrt == "" {
		return nil
	}
	ca, err := base64.StdEncoding.DecodeString(*caCrt)
	if err != nil {
		return fmt.Errorf("failed to decode CA certificate: %w", err)
	}
	if registryHostname == "" || strings.ContainsAny(registryHostname, "/\\\x00\n\r") || registryHostname == "." || registryHostname == ".." {
		return fmt.Errorf("invalid registry hostname")
	}
	return writeGuestFile(ctx, session, filepath.Join("/etc/containers/certs.d", registryHostname, "ca.crt"), ca)
}

func writeGuestFile(ctx context.Context, session *vm.Session, path string, data []byte) error {
	if err := session.Run(ctx, []string{"mkdir", "-p", filepath.Dir(path)}, nil, nil, io.Discard); err != nil {
		return err
	}
	return session.Run(ctx, []string{"sh", "-c", `umask 077; cat > "$1"`, "write-file", path}, nil, bytes.NewReader(data), io.Discard)
}

// authFileEnv returns the env map that wires REGISTRY_AUTH_FILE to containerAuthFile
// for source image operations.
func authFileEnv() map[string]string {
	return map[string]string{
		"REGISTRY_AUTH_FILE": containerAuthFile,
	}
}

// loginToRegistry keeps source and destination credentials in separate guest files.
func (c *Consumer) loginToRegistry(ctx context.Context, worker *podmanWorker, registryHostname, username, password, authFile string, ociSpec *coredomain.OciRepoSpec, log logrus.FieldLogger) error {
	if username == "" || password == "" {
		return nil
	}
	if len(username) > 256 || strings.ContainsAny(username, "\x00\n\r") || len(registryHostname) > 256 || strings.ContainsAny(registryHostname, "\x00\n\r") {
		return fmt.Errorf("invalid registry credentials")
	}
	args := []string{"podman", "login", "--authfile", authFile, "-u", username, "--password-stdin"}
	if insecureRegistry(ociSpec) {
		args = append(args, "--tls-verify=false")
	}
	args = append(args, registryHostname)
	return worker.Session.Run(ctx, args, nil, strings.NewReader(password), io.Discard)
}

func insecureRegistry(spec *coredomain.OciRepoSpec) bool {
	return spec != nil && ((spec.Scheme != nil && *spec.Scheme == coredomain.OciRepoSchemeHttp) || (spec.SkipServerVerification != nil && *spec.SkipServerVerification))
}

// getOciRepoSpec retrieves and validates a repository as OCI type, returning its spec.
func (c *Consumer) getOciRepoSpec(ctx context.Context, orgID uuid.UUID, repoName string, repoRole string) (*coredomain.OciRepoSpec, error) {
	repo, status := c.repositories.GetRepository(ctx, orgID, repoName)
	if err := statusToErr(status); err != nil {
		return nil, fmt.Errorf("failed to get %s repository: %w", repoRole, err)
	}

	repoType, err := repo.Spec.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine %s repository type: %w", repoRole, err)
	}
	if repoType != string(coredomain.RepoSpecTypeOci) {
		return nil, fmt.Errorf("%s repository %q must be of type 'oci', got %q", repoRole, repoName, repoType)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to parse %s OCI repository spec: %w", repoRole, err)
	}

	return &ociSpec, nil
}

// writeBuildContextFiles writes the containerfile and associated files to the build directory.
func writeBuildContextFiles(tmpDir string, containerfileResult *ContainerfileResult) error {
	// Write Containerfile
	containerfilePath := filepath.Join(tmpDir, "Containerfile")
	if err := os.WriteFile(containerfilePath, []byte(containerfileResult.Containerfile), 0600); err != nil {
		return fmt.Errorf("failed to write Containerfile: %w", err)
	}

	// Write agent config file (required by COPY instruction, empty if not early binding)
	agentConfigPath := filepath.Join(tmpDir, "agent-config.yaml")
	agentConfigContent := containerfileResult.AgentConfig
	if agentConfigContent == nil {
		agentConfigContent = []byte{}
	}
	if err := os.WriteFile(agentConfigPath, agentConfigContent, 0600); err != nil {
		return fmt.Errorf("failed to write agent-config.yaml: %w", err)
	}

	// Write public key file (required by COPY instruction, empty if no user config)
	publickeyPath := filepath.Join(tmpDir, "user-publickey.txt")
	publickeyContent := containerfileResult.Publickey
	if publickeyContent == nil {
		publickeyContent = []byte{}
	}
	if err := os.WriteFile(publickeyPath, publickeyContent, 0600); err != nil {
		return fmt.Errorf("failed to write user-publickey.txt: %w", err)
	}

	return nil
}

// buildImageWithPodman builds the image using podman in a disposable guest.
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

	// Get and validate destination repository
	destOciSpec, err := c.getOciRepoSpec(ctx, orgID, spec.Destination.Repository, "destination")
	if err != nil {
		return err
	}

	// Validate image reference components (defense-in-depth)
	if err := validateImageRefComponents(spec.Destination.ImageName, spec.Destination.ImageTag); err != nil {
		return fmt.Errorf("invalid image reference components: %w", err)
	}

	// ociSpec.Registry is already the hostname (no scheme)
	destRegistryHostname := destOciSpec.Registry
	imageRef := oci.ImageDestRef(destRegistryHostname, spec.Destination.ImageName, spec.Destination.ImageTag)

	// Determine platform from ImageBuild status architecture, default to linux/amd64
	platform := "linux/amd64"
	if imageBuild.Status != nil && imageBuild.Status.Architecture != nil && *imageBuild.Status.Architecture != "" {
		platform = *imageBuild.Status.Architecture
	}

	log.WithFields(logrus.Fields{
		"imageRef": imageRef,
		"platform": platform,
	}).Info("Starting podman build")

	// Write build context files (Containerfile, agent-config.yaml, user-publickey.txt)
	if err := writeBuildContextFiles(podmanWorker.TmpDir, containerfileResult); err != nil {
		return err
	}

	// Get and validate source repository for pulling the base image (FROM)
	ociSpec, err := c.getOciRepoSpec(ctx, orgID, spec.Source.Repository, "source")
	if err != nil {
		return err
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
			decryptedPassword, ok, decErr := encryption.Decrypt(ctx, encryption.Ciphertext(dockerAuth.Password))
			if decErr != nil {
				return fmt.Errorf("failed to decrypt OCI password: %w", decErr)
			}
			if !ok {
				log.WithField("registry", sourceRegistryHostname).Warn("source registry password is stored as plaintext; expected an encrypted value")
			}
			if err := c.loginToRegistry(ctx, podmanWorker, sourceRegistryHostname, dockerAuth.Username, string(decryptedPassword), containerAuthFile, ociSpec, log); err != nil {
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
		"build", "--isolation", "chroot", "--network", "host",
		"--platform", platform,
		"--manifest", imageRef,
	}

	// Disable TLS verification for HTTP source registries or when explicitly requested
	if ociSpec.Scheme != nil && *ociSpec.Scheme == coredomain.OciRepoSchemeHttp {
		podmanBuildArgs = append(podmanBuildArgs, "--tls-verify=false")
		log.Debug("Using --tls-verify=false for HTTP source registry")
	} else if ociSpec.SkipServerVerification != nil && *ociSpec.SkipServerVerification {
		podmanBuildArgs = append(podmanBuildArgs, "--tls-verify=false")
		log.Debug("Using --tls-verify=false due to source SkipServerVerification")
	}

	podmanBuildArgs = append(podmanBuildArgs,
		"--build-arg", fmt.Sprintf("REGISTRY_HOSTNAME=%s", args.RegistryHostname),
		"--build-arg", fmt.Sprintf("IMAGE_NAME=%s", args.ImageName),
		"--build-arg", fmt.Sprintf("IMAGE_TAG=%s", args.ImageTag),
		"--build-arg", fmt.Sprintf("EARLY_BINDING=%t", args.EarlyBinding),
		"--build-arg", fmt.Sprintf("HAS_USER_CONFIG=%t", args.HasUserConfig),
		"--build-arg", fmt.Sprintf("USERNAME=%s", args.Username),
		"--build-arg", fmt.Sprintf("AGENT_CONFIG_DEST_PATH=%s", args.AgentConfigDestPath),
		"--build-arg", fmt.Sprintf("INSTALL_ONBOARDING=%t", args.InstallOnboarding),
		"--build-arg", fmt.Sprintf("RPM_REPO_ADD=%t", args.RPMRepoAdd),
		"--build-arg", fmt.Sprintf("RPM_REPO_ADD_URL=%s", args.RPMRepoAddURL),
		"--build-arg", fmt.Sprintf("RPM_REPO_ENABLE=%s", args.RPMRepoEnable),
		"--build-arg", fmt.Sprintf("DNF_TIMEOUT=%d", args.DNFTimeout),
		"--build-arg", fmt.Sprintf("DNF_RETRIES=%d", args.DNFRetries),
		"--build-arg", fmt.Sprintf("DNF_SKIP_UNAVAILABLE=%t", args.DNFSkipUnavailable),
	)

	// Mount entitlement certs into the build if available (for RHEL subscription repos)
	if podmanWorker.HasEntitlementCerts {
		podmanBuildArgs = append(podmanBuildArgs, "--volume", fmt.Sprintf("%s:%s:ro", entitlementCertsPath, entitlementCertsPath))
	}

	podmanBuildArgs = append(podmanBuildArgs,
		"-f", containerContainerfilePath,
		containerBuildDir,
	)

	buildEnvVars := authFileEnv()
	if err := podmanWorker.runInWorker(ctx, log, "build", buildEnvVars, podmanBuildArgs...); err != nil {
		return err
	}

	log.Info("Phase: Build Completed")

	return nil
}

// pushImageWithPodman pushes the built image to the destination registry.
// It returns the image reference and manifest digest of the pushed image.
func (c *Consumer) pushImageWithPodman(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	podmanWorker *podmanWorker,
	log logrus.FieldLogger,
) (string, string, error) {
	// Check context before starting work
	if err := ctx.Err(); err != nil {
		return "", "", err
	}

	spec := imageBuild.Spec

	// Get and validate destination repository
	ociSpec, err := c.getOciRepoSpec(ctx, orgID, spec.Destination.Repository, "destination")
	if err != nil {
		return "", "", err
	}

	// Validate image reference components (defense-in-depth)
	if err := validateImageRefComponents(spec.Destination.ImageName, spec.Destination.ImageTag); err != nil {
		return "", "", fmt.Errorf("invalid image reference components: %w", err)
	}

	// ociSpec.Registry is already the hostname (no scheme)
	destRegistryHostname := ociSpec.Registry
	imageRef := oci.ImageDestRef(destRegistryHostname, spec.Destination.ImageName, spec.Destination.ImageTag)

	// Login to registry using podman login with stdin
	// This is more reliable than authfile for push operations
	if ociSpec.OciAuth != nil {
		dockerAuth, err := ociSpec.OciAuth.AsDockerAuth()
		if err == nil && dockerAuth.Username != "" && dockerAuth.Password != "" {
			decryptedPassword, ok, decErr := encryption.Decrypt(ctx, encryption.Ciphertext(dockerAuth.Password))
			if decErr != nil {
				return "", "", fmt.Errorf("failed to decrypt OCI password: %w", decErr)
			}
			if !ok {
				log.WithField("registry", destRegistryHostname).Warn("destination registry password is stored as plaintext; expected an encrypted value")
			}
			if err := c.loginToRegistry(ctx, podmanWorker, destRegistryHostname, dockerAuth.Username, string(decryptedPassword), destinationAuthFile, ociSpec, log); err != nil {
				return "", "", fmt.Errorf("failed to login to destination registry: %w", err)
			}
		}
	}

	// ---------------------------------------------------------
	// PHASE: PUSH (Explicit State Transition)
	// ---------------------------------------------------------
	log.Info("Phase: Push Started")

	// Push with --digestfile to capture the manifest digest
	// Note: We push the MANIFEST (imageRef), which pushes all layers
	// Authentication is handled via podman login above
	const digestFileName = "push-digest.txt"
	workerDigestPath := filepath.Join("/output", digestFileName)
	pushArgs := []string{"push", "--digestfile", workerDigestPath}

	// Disable TLS verification for HTTP registries or when explicitly requested
	if ociSpec.Scheme != nil && *ociSpec.Scheme == coredomain.OciRepoSchemeHttp {
		pushArgs = append(pushArgs, "--tls-verify=false")
		log.Debug("Using --tls-verify=false for HTTP registry")
	} else if ociSpec.SkipServerVerification != nil && *ociSpec.SkipServerVerification {
		pushArgs = append(pushArgs, "--tls-verify=false")
		log.Debug("Using --tls-verify=false due to SkipServerVerification")
	}

	pushArgs = append(pushArgs, imageRef)
	pushEnvVars := map[string]string{"REGISTRY_AUTH_FILE": destinationAuthFile}
	if err := podmanWorker.runInWorker(ctx, log, "push", pushEnvVars, pushArgs...); err != nil {
		return "", "", err
	}

	// Read the manifest digest from the digest file (host path maps to /output in worker)
	digestBytes, err := podmanWorker.Session.ReadArtifact(ctx, digestFileName, 256)
	if err != nil {
		return "", "", fmt.Errorf("reading manifest digest file: %w", err)
	}
	manifestDigest := strings.TrimSpace(string(digestBytes))
	if _, err := digest.Parse(manifestDigest); err != nil {
		return "", "", fmt.Errorf("invalid manifest digest: %w", err)
	}

	log.WithFields(logrus.Fields{
		"imageRef":       imageRef,
		"manifestDigest": manifestDigest,
	}).Info("Phase: Push Completed - Image pushed successfully")

	return imageRef, manifestDigest, nil
}
