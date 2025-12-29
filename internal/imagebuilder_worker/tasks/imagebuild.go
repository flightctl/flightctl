package tasks

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"text/template"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/v1beta1"
	api "github.com/flightctl/flightctl/api/v1beta1/imagebuilder"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/crypto"
	imagebuilderstore "github.com/flightctl/flightctl/internal/imagebuilder_api/store"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
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

// processImageBuild processes an imageBuild job by loading the ImageBuild resource
// and routing to the appropriate build handler
func processImageBuild(
	ctx context.Context,
	store imagebuilderstore.Store,
	mainStore store.Store,
	kvStore kvstore.KVStore,
	serviceHandler *service.ServiceHandler,
	cfg *config.Config,
	job Job,
	log logrus.FieldLogger,
) error {
	log = log.WithField("job", job.Name).WithField("orgId", job.OrgID)
	log.Info("Processing imageBuild job")

	// Parse org ID
	orgID, err := uuid.Parse(job.OrgID)
	if err != nil {
		return fmt.Errorf("invalid org ID %q: %w", job.OrgID, err)
	}

	// Load the ImageBuild resource from the database
	imageBuild, err := store.ImageBuild().Get(ctx, orgID, job.Name)
	if err != nil {
		return fmt.Errorf("failed to load ImageBuild %q: %w", job.Name, err)
	}

	log.WithField("spec", imageBuild.Spec).Debug("Loaded ImageBuild resource")

	// Initialize status if nil
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	// Check if already completed or failed - skip if so
	if imageBuild.Status.Conditions != nil {
		for _, cond := range *imageBuild.Status.Conditions {
			if cond.Type == api.ImageBuildConditionTypeReady {
				isCompleted := cond.Reason == string(api.ImageBuildConditionReasonCompleted) && cond.Status == v1beta1.ConditionStatusTrue
				isFailed := cond.Reason == string(api.ImageBuildConditionReasonFailed) && cond.Status == v1beta1.ConditionStatusFalse
				if isCompleted || isFailed {
					log.Infof("ImageBuild %q already in terminal state %q, skipping", job.Name, cond.Reason)
					return nil
				}
			}
		}
	}

	// Update status to Building
	now := time.Now().UTC()
	setImageBuildCondition(imageBuild, api.ImageBuildConditionTypeReady, v1beta1.ConditionStatusFalse, api.ImageBuildConditionReasonBuilding, "Build is in progress", now)
	imageBuild.Status.LastSeen = lo.ToPtr(now)

	_, err = store.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status to Building: %w", err)
	}

	log.Info("Updated ImageBuild status to Building")

	// Check context for cancellation
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	// Step 1: Generate Containerfile
	log.Info("Generating Containerfile for image build")
	containerfileResult, err := GenerateContainerfile(ctx, mainStore, serviceHandler, orgID, imageBuild, log)
	if err != nil {
		failedTime := time.Now().UTC()
		setImageBuildCondition(imageBuild, api.ImageBuildConditionTypeReady, v1beta1.ConditionStatusFalse, api.ImageBuildConditionReasonFailed, err.Error(), failedTime)
		if _, updateErr := store.ImageBuild().UpdateStatus(ctx, orgID, imageBuild); updateErr != nil {
			log.WithError(updateErr).Error("failed to update ImageBuild status to Failed")
		}
		return fmt.Errorf("failed to generate Containerfile: %w", err)
	}

	log.WithField("containerfile_length", len(containerfileResult.Containerfile)).Info("Containerfile generated successfully")
	log.Debug("Generated Containerfile:\n", containerfileResult.Containerfile)

	// TODO(E5): Add more build steps here:
	// Step 2: Write Containerfile to temporary directory
	// Step 3: If early binding, write certificate file alongside Containerfile
	// Step 4: Set up podman with bootc-image-builder
	// Step 5: Stream build logs to Redis pub/sub for real-time viewing
	// Step 6: Handle build completion and push results

	// For now, mark as Completed (placeholder)
	now = time.Now().UTC()
	setImageBuildCondition(imageBuild, api.ImageBuildConditionTypeReady, v1beta1.ConditionStatusTrue, api.ImageBuildConditionReasonCompleted, "Build completed successfully (placeholder)", now)

	_, err = store.ImageBuild().UpdateStatus(ctx, orgID, imageBuild)
	if err != nil {
		return fmt.Errorf("failed to update ImageBuild status to Completed: %w", err)
	}

	log.Info("ImageBuild marked as Completed (placeholder)")
	return nil
}

// setImageBuildCondition sets or updates a condition on the ImageBuild status
func setImageBuildCondition(imageBuild *api.ImageBuild, conditionType api.ImageBuildConditionType, status v1beta1.ConditionStatus, reason api.ImageBuildConditionReason, message string, transitionTime time.Time) {
	if imageBuild.Status == nil {
		imageBuild.Status = &api.ImageBuildStatus{}
	}

	if imageBuild.Status.Conditions == nil {
		imageBuild.Status.Conditions = &[]api.ImageBuildCondition{}
	}

	// Find existing condition or add new one
	conditions := *imageBuild.Status.Conditions
	found := false
	for i := range conditions {
		if conditions[i].Type == conditionType {
			conditions[i].Status = status
			conditions[i].Reason = string(reason)
			conditions[i].Message = message
			conditions[i].LastTransitionTime = transitionTime
			found = true
			break
		}
	}

	if !found {
		conditions = append(conditions, api.ImageBuildCondition{
			Type:               conditionType,
			Status:             status,
			Reason:             string(reason),
			Message:            message,
			LastTransitionTime: transitionTime,
		})
	}

	imageBuild.Status.Conditions = &conditions
}

// containerfileData holds the data for rendering the Containerfile template
type containerfileData struct {
	RegistryURL         string
	ImageName           string
	ImageTag            string
	EarlyBinding        bool
	AgentConfig         string
	AgentConfigDestPath string
	HeredocDelimiter    string
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
	if imageBuild == nil {
		return nil, fmt.Errorf("imageBuild cannot be nil")
	}

	spec := imageBuild.Spec

	// Load the source repository to get the registry URL
	registryURL, err := getRepositoryURL(ctx, mainStore, orgID, spec.Source.Repository)
	if err != nil {
		return nil, fmt.Errorf("failed to get source repository URL: %w", err)
	}

	// Determine binding type
	bindingType, err := spec.Binding.Discriminator()
	if err != nil {
		return nil, fmt.Errorf("failed to determine binding type: %w", err)
	}

	log.WithFields(logrus.Fields{
		"registryURL": registryURL,
		"imageName":   spec.Source.ImageName,
		"imageTag":    spec.Source.ImageTag,
		"bindingType": bindingType,
	}).Debug("Generating Containerfile")

	result := &ContainerfileResult{}

	// Generate a unique heredoc delimiter to avoid conflicts with config content
	heredocDelimiter := fmt.Sprintf("FLIGHTCTL_CONFIG_%s", uuid.NewString()[:8])

	// Prepare template data
	data := containerfileData{
		RegistryURL:         registryURL,
		ImageName:           spec.Source.ImageName,
		ImageTag:            spec.Source.ImageTag,
		EarlyBinding:        bindingType == string(api.BindingTypeEarly),
		AgentConfigDestPath: agentConfigPath,
		HeredocDelimiter:    heredocDelimiter,
	}

	// Handle early binding - generate enrollment credentials
	if data.EarlyBinding {
		// Generate a unique name for this build's enrollment credentials
		imageBuildName := lo.FromPtr(imageBuild.Metadata.Name)
		credentialName := fmt.Sprintf("imagebuild-%s-%s", imageBuildName, orgID.String()[:8])

		agentConfig, err := generateAgentConfig(ctx, credentialGenerator, orgID, credentialName, imageBuildName)
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

// getRepositoryURL retrieves the registry URL from a Repository resource
func getRepositoryURL(ctx context.Context, mainStore store.Store, orgID uuid.UUID, repoName string) (string, error) {
	repo, err := mainStore.Repository().Get(ctx, orgID, repoName)
	if err != nil {
		return "", fmt.Errorf("repository %q not found: %w", repoName, err)
	}

	// Get the repository spec type
	specType, err := repo.Spec.Discriminator()
	if err != nil {
		return "", fmt.Errorf("failed to determine repository spec type: %w", err)
	}

	// Only OCI repositories are supported for image builds
	if specType != string(v1beta1.RepoSpecTypeOci) {
		return "", fmt.Errorf("repository %q must be of type 'oci', got %q", repoName, specType)
	}

	ociSpec, err := repo.Spec.AsOciRepoSpec()
	if err != nil {
		return "", fmt.Errorf("failed to parse OCI repository spec: %w", err)
	}

	// Build the full registry URL with optional scheme
	registryURL := ociSpec.Registry
	if ociSpec.Scheme != nil {
		registryURL = fmt.Sprintf("%s://%s", *ociSpec.Scheme, ociSpec.Registry)
	}

	return registryURL, nil
}

// generateAgentConfig generates a complete agent config.yaml for early binding.
// This generates a new enrollment credential (key pair + signed certificate) for each image build
// using the CSR service for proper certificate issuance and audit trail.
func generateAgentConfig(ctx context.Context, credentialGenerator EnrollmentCredentialGenerator, orgID uuid.UUID, name string, imageBuildName string) ([]byte, error) {
	// Generate enrollment credential using the credential generator
	// This will create a CSR, auto-approve it, sign it, and return the credential
	// The CSR owner is set to the ImageBuild resource for traceability
	credential, status := credentialGenerator.GenerateEnrollmentCredential(ctx, orgID, name, api.ImageBuildKind, imageBuildName)
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
