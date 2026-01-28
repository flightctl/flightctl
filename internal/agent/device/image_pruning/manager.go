package imagepruning

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
)

var _ Manager = (*manager)(nil)

const (
	// ReferencesFileName is the name of the file that stores image and artifact references
	ReferencesFileName = "image-artifact-references.json"
)

// RefType constants for tracking what pulled each image
const (
	RefTypePodman   = "podman"   // Container images for podman apps (compose, quadlet, container)
	RefTypeCRI      = "cri"      // Container images for Kubernetes/Helm workloads
	RefTypeArtifact = "artifact" // OCI artifacts (volumes, etc.)
	RefTypeHelm     = "helm"     // Helm charts (stored in helm cache, not image storage)
)

// Manager provides the public API for managing image pruning operations.
type Manager interface {
	// Prune removes unused container images and OCI artifacts after successful spec reconciliation.
	// It preserves images required for current and desired operations.
	Prune(ctx context.Context) error
	// RecordReferences records all image and artifact references from current and desired specs to a file.
	// This ensures the references file exists and is kept up to date even when pruning is disabled,
	// so that when pruning is later enabled, it has accurate historical data to work with.
	// current and desired are the device specs to extract references from.
	RecordReferences(ctx context.Context, current, desired *v1beta1.Device) error
	// ReloadConfig reloads the pruning configuration from the agent config.
	// This is called when the agent receives a SIGHUP signal to reload configuration.
	ReloadConfig(ctx context.Context, cfg *config.Config) error

	// PrunePending indicates whether a prune operation is pending due to config change.
	PrunePending() bool
}

// ImagePruningConfig holds configuration for image pruning operations.
// This type is defined in internal/agent/config/config.go as config.ImagePruning
// and is aliased here for backward compatibility and clarity.
type ImagePruningConfig = config.ImagePruning

// manager implements the Manager interface for image pruning operations.
type manager struct {
	podmanClientFactory client.PodmanFactory
	rootPodmanClient    *client.Podman
	clients             client.CLIClients
	specManager         spec.Manager
	readWriter          fileio.ReadWriter
	rwFactory           fileio.ReadWriterFactory
	log                 *log.PrefixLogger
	config              atomic.Pointer[ImagePruningConfig]
	dataDir             string
	prunePending        atomic.Bool
}

// New creates a new pruning manager instance.
//
// Dependencies:
//   - podmanClient: Podman client for image/artifact operations
//   - clients: CLI clients for Helm and Kube operations
//   - specManager: Spec manager for reading current and desired specs
//   - readWriter: File I/O interface for any file operations
//   - log: Logger for structured logging
//   - config: Pruning configuration (enabled flag, etc.)
//   - dataDir: Directory where data files are stored
func New(
	podmanClientFactory client.PodmanFactory,
	rootPodmanClient *client.Podman,
	clients client.CLIClients,
	specManager spec.Manager,
	rwFactory fileio.ReadWriterFactory,
	readWriter fileio.ReadWriter,
	log *log.PrefixLogger,
	config ImagePruningConfig,
	dataDir string,
) Manager {
	ret := &manager{
		podmanClientFactory: podmanClientFactory,
		rootPodmanClient:    rootPodmanClient,
		clients:             clients,
		specManager:         specManager,
		rwFactory:           rwFactory,
		readWriter:          readWriter,
		log:                 log,
		dataDir:             dataDir,
	}
	ret.config.Store(&config)
	return ret
}

// Prune removes unused container images and OCI artifacts after successful spec reconciliation.
// It preserves images required for current and desired operations.
func (m *manager) Prune(ctx context.Context) error {
	m.prunePending.Store(false)
	if !lo.FromPtr(m.config.Load().Enabled) {
		m.log.Debug("Image pruning is disabled, skipping")
		return nil
	}

	// Determine eligible images and artifacts for pruning
	// This function handles all validation: it only considers images/artifacts that exist locally,
	// and builds a preserve set from required images in specs. Missing required images
	// cannot be pruned anyway, so they don't need explicit protection.
	eligible, err := m.determineEligibleImages(ctx)
	if err != nil {
		m.log.Warnf("Failed to determine eligible images: %v", err)
		// Don't block reconciliation on pruning errors
		return nil
	}

	totalEligible := len(eligible.Images) + len(eligible.Artifacts) + len(eligible.CRI) + len(eligible.Helm)

	var allRemovedRefs []ImageRef

	if totalEligible > 0 {
		m.log.Infof("Starting pruning of %d podman images, %d artifacts, %d CRI images, %d helm charts",
			len(eligible.Images), len(eligible.Artifacts), len(eligible.CRI), len(eligible.Helm))

		// Remove eligible items by type, tracking which ones were successfully removed
		removedImages, removedImageRefs, err := m.removeEligibleImages(ctx, eligible.Images)
		if err != nil {
			m.log.Warnf("Error during podman image removal: %v", err)
		}
		allRemovedRefs = append(allRemovedRefs, removedImageRefs...)

		removedArtifacts, removedArtifactRefs, err := m.removeEligibleArtifacts(ctx, eligible.Artifacts)
		if err != nil {
			m.log.Warnf("Error during artifact removal: %v", err)
		}
		allRemovedRefs = append(allRemovedRefs, removedArtifactRefs...)

		removedCRI, removedCRIRefs, err := m.removeEligibleCRIImages(ctx, eligible.CRI)
		if err != nil {
			m.log.Warnf("Error during CRI image removal: %v", err)
		}
		allRemovedRefs = append(allRemovedRefs, removedCRIRefs...)

		removedHelm, removedHelmRefs, err := m.removeEligibleHelmCharts(eligible.Helm)
		if err != nil {
			m.log.Warnf("Error during helm chart removal: %v", err)
		}
		allRemovedRefs = append(allRemovedRefs, removedHelmRefs...)

		m.log.Infof("Pruning complete: removed %d/%d podman images, %d/%d artifacts, %d/%d CRI images, %d/%d helm charts",
			removedImages, len(eligible.Images),
			removedArtifacts, len(eligible.Artifacts),
			removedCRI, len(eligible.CRI),
			removedHelm, len(eligible.Helm))
	} else {
		m.log.Debug("No images or artifacts eligible for pruning")
	}

	// Remove successfully pruned items from the references file
	if len(allRemovedRefs) > 0 {
		if err := m.removePrunedReferencesFromFile(allRemovedRefs, nil); err != nil {
			m.log.Warnf("Failed to remove pruned references from file: %v", err)
		}
	}

	// Validate capability after pruning
	if err := m.validateCapability(ctx); err != nil {
		m.log.Warnf("Capability validation failed after pruning: %v", err)
		// Log warning but don't block reconciliation
	}

	return nil
}

// RecordReferences records all image and artifact references from current and desired specs to a file.
// This ensures the references file exists and is kept up to date even when pruning is disabled,
// so that when pruning is later enabled, it has accurate historical data to work with.
// This is called on every successful sync, regardless of pruning enabled status.
func (m *manager) RecordReferences(ctx context.Context, current, desired *v1beta1.Device) error {
	if err := m.recordImageArtifactReferences(ctx, current, desired); err != nil {
		m.log.Warnf("Failed to record image/artifact references: %v", err)
		return err
	}

	return nil
}

// ReloadConfig reloads the pruning configuration from the agent config.
// This is called when the agent receives a SIGHUP signal to reload configuration.
func (m *manager) ReloadConfig(ctx context.Context, cfg *config.Config) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}

	prev := m.config.Swap(&cfg.ImagePruning)

	// Update the config with the new values
	if lo.FromPtr(cfg.ImagePruning.Enabled) && !lo.FromPtr(prev.Enabled) {
		m.prunePending.Store(true)
	}

	m.log.Infof("Image pruning config reloaded: enabled=%v", lo.FromPtr(cfg.ImagePruning.Enabled))
	return nil
}

func (m *manager) PrunePending() bool {
	return m.prunePending.Load()
}

// getImageReferencesFromSpecs extracts image references from a device spec.
// It includes both explicit references and nested targets extracted from image-based applications.
func (m *manager) getImageReferencesFromSpecs(ctx context.Context, device *v1beta1.Device) ([]ImageRef, error) {
	if device == nil || device.Spec == nil {
		return nil, nil
	}

	var images []ImageRef

	// Extract image/artifact references directly from the spec
	// This mirrors the logic in CollectBaseOCITargets but only extracts reference strings
	specRefs, err := m.extractReferencesFromSpec(ctx, device.Spec)
	if err != nil {
		return nil, fmt.Errorf("extracting references from spec: %w", err)
	}
	images = append(images, specRefs...)

	// Extract OS image (not included in extractReferencesFromSpec)
	// OS images are pulled via podman
	if device.Spec.Os != nil && device.Spec.Os.Image != "" {
		images = append(images, ImageRef{Image: device.Spec.Os.Image, Type: RefTypePodman})
	}

	// Extract nested targets from image-based applications
	nestedImages, err := m.extractNestedTargetsFromSpec(ctx, device)
	if err != nil {
		// Log warning but don't fail - nested extraction is best-effort
		m.log.Warnf("Failed to extract nested targets from spec: %v", err)
	} else {
		images = append(images, nestedImages...)
	}

	return lo.Uniq(images), nil
}

// ImageArtifactReferences holds all image and artifact references from specs.
// References are stored without categorization during recording.
// Categorization happens during pruning when we can check if they exist as images or artifacts.
type ImageArtifactReferences struct {
	Timestamp  string     `json:"timestamp"`
	References []ImageRef `json:"references"` // Single list of all references (no categorization)
}

// ImageRef represents a reference to an image, artifact, or helm chart with its source type.
type ImageRef struct {
	Image string           `json:"image"`
	Owner v1beta1.Username `json:"owner"`
	Type  string           `json:"type"` // One of RefType* constants
}

// setOwnerOnRefs sets the owner on all ImageRefs in the slice.
func setOwnerOnRefs(refs []ImageRef, owner v1beta1.Username) {
	for i := range refs {
		refs[i].Owner = owner
	}
}

// readPreviousReferences reads the previous image/artifact references from the file.
// Returns nil if the file doesn't exist (first run) or if there's an error reading it.
func (m *manager) readPreviousReferences() *ImageArtifactReferences {
	// Use filepath.Join to create the full path, then readWriter will handle it correctly
	filePath := filepath.Join(m.dataDir, ReferencesFileName)
	data, err := m.readWriter.ReadFile(filePath)
	if err != nil {
		// File doesn't exist or can't be read - this is expected on first run
		m.log.Debugf("Previous references file not found or unreadable: %v", err)
		return nil
	}

	var refs ImageArtifactReferences
	if err := json.Unmarshal(data, &refs); err != nil {
		m.log.Warnf("Failed to unmarshal previous references file: %v", err)
		return nil
	}

	return &refs
}

// recordImageArtifactReferences records all image and artifact references from current and desired specs to a file.
// It accumulates references: reads existing file (if any), adds new references from current specs, and writes back.
// References are only removed when they are successfully pruned (see removePrunedReferencesFromFile).
// current and desired are the device specs to extract references from.
func (m *manager) recordImageArtifactReferences(ctx context.Context, current, desired *v1beta1.Device) error {
	// Read existing references file (if it exists) to accumulate with new references
	existingRefs := lo.FromPtr(m.readPreviousReferences())

	// Start with existing references
	existingReferences := existingRefs.References

	// Extract all image references from the provided specs using getImageReferencesFromSpecs
	// This ensures we collect exactly the same references that the prefetch manager sees
	// We do NOT categorize here - categorization happens during pruning when we can check existence
	var allRefs []ImageRef

	// Process both current and desired specs in a loop
	specs := []struct {
		device *v1beta1.Device
		name   string
	}{
		{current, "current"},
		{desired, "desired"},
	}

	for _, s := range specs {
		if s.device == nil {
			continue
		}

		specRefs, err := m.getImageReferencesFromSpecs(ctx, s.device)
		if err != nil {
			m.log.Warnf("Failed to get image references from %s spec for recording: %v", s.name, err)
		} else {
			allRefs = append(allRefs, specRefs...)
		}
	}

	allRefs = lo.Uniq(allRefs)

	// Accumulate all references without categorization
	existingReferences = append(existingReferences, allRefs...)

	// Build final accumulated references (single list, no categorization)
	refs := ImageArtifactReferences{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		References: lo.Uniq(existingReferences),
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling image/artifact references: %w", err)
	}

	// Write to file
	filePath := filepath.Join(m.dataDir, ReferencesFileName)
	if err := m.readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing image/artifact references to file: %w", err)
	}

	m.log.Debugf("Recorded image/artifact references to %s (accumulated: %d references)", filePath, len(refs.References))
	return nil
}

// removePrunedReferencesFromFile removes successfully pruned images and artifacts from the references file.
// This ensures the accumulated file only contains items that still exist or haven't been pruned yet.
func (m *manager) removePrunedReferencesFromFile(removedImages []ImageRef, removedArtifacts []ImageRef) error {
	allRemoved := append(removedImages, removedArtifacts...)
	if len(allRemoved) == 0 {
		return nil // Nothing to remove
	}

	// Read existing references file
	existingRefs := m.readPreviousReferences()
	if existingRefs == nil {
		// File doesn't exist - nothing to remove
		return nil
	}

	// Filter out removed items from the single References list
	filteredReferences := lo.Without(lo.Uniq(existingRefs.References), lo.Uniq(allRemoved)...)

	// Build updated references
	refs := ImageArtifactReferences{
		Timestamp:  time.Now().UTC().Format(time.RFC3339),
		References: filteredReferences,
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(refs, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling updated image/artifact references: %w", err)
	}

	// Write to file
	filePath := filepath.Join(m.dataDir, ReferencesFileName)
	if err := m.readWriter.WriteFile(filePath, jsonData, fileio.DefaultFilePermissions); err != nil {
		return fmt.Errorf("writing updated image/artifact references to file: %w", err)
	}
	m.log.Debugf("Removed %d references from references file (remaining: %d references)", len(allRemoved), len(filteredReferences))
	return nil
}

// EligibleItems holds lists of eligible items for pruning, organized by type.
type EligibleItems struct {
	Images    []ImageRef // Podman images eligible for removal
	Artifacts []ImageRef // Podman artifacts eligible for removal
	CRI       []ImageRef // CRI images eligible for removal
	Helm      []ImageRef // Helm charts eligible for removal
}

// determineEligibleImages determines which images, artifacts, and charts are eligible for pruning.
// It only prunes items that were previously referenced (according to the references file)
// but are no longer referenced in the current specs.
// An item is only eligible when ALL reference types have been released (e.g., if an image
// is referenced by both podman and CRI, it's only eligible when both references are dropped).
// Returns separate lists by type for appropriate cleanup.
func (m *manager) determineEligibleImages(ctx context.Context) (*EligibleItems, error) {
	m.log.Debug("Determining eligible images and artifacts for pruning")

	// Read previous references from file
	previousRefs := m.readPreviousReferences()
	if previousRefs == nil {
		// No previous references file - this is the first run, so nothing to prune
		m.log.Debug("No previous references file found - skipping pruning on first run")
		return &EligibleItems{
			Images:    []ImageRef{},
			Artifacts: []ImageRef{},
			CRI:       []ImageRef{},
			Helm:      []ImageRef{},
		}, nil
	}

	// Get current required references from specs (includes application images, OS images, and artifacts)
	var currentRequiredRefs []ImageRef

	// Process both current and desired specs in a loop
	specTypes := []struct {
		specType spec.Type
		name     string
		required bool
	}{
		{spec.Current, "current", true},
		{spec.Desired, "desired", false},
	}

	for _, st := range specTypes {
		device, err := m.specManager.Read(st.specType)
		if err != nil {
			if st.required {
				return nil, fmt.Errorf("reading %s spec: %w", st.name, err)
			}
			// Desired spec may not exist - this is acceptable
			m.log.Debugf("%s spec not available: %v", st.name, err)
			continue
		}

		if device == nil {
			continue
		}

		specRefs, err := m.getImageReferencesFromSpecs(ctx, device)
		if err != nil {
			if st.required {
				return nil, fmt.Errorf("getting image references from %s spec: %w", st.name, err)
			}
			// Log warning but don't fail - desired spec extraction is best-effort
			m.log.Warnf("Failed to get image references from %s spec: %v", st.name, err)
			continue
		}

		currentRequiredRefs = append(currentRequiredRefs, specRefs...)
	}

	// Build a set of current references keyed by (Image, Type)
	currentRefSet := make(map[string]struct{})
	for _, ref := range currentRequiredRefs {
		key := refKey(ref.Image, ref.Type)
		currentRefSet[key] = struct{}{}
	}

	// Build a set of images that still have at least one reference
	imagesWithRefs := make(map[string]struct{})
	for _, ref := range currentRequiredRefs {
		imagesWithRefs[ref.Image] = struct{}{}
	}

	// Get previous references
	previousReferences := previousRefs.References

	// Find references that were previously recorded but are no longer in current specs
	// A reference is eligible if its (Image, Type) tuple is not in current refs
	var eligibleRefs []ImageRef
	for _, ref := range previousReferences {
		key := refKey(ref.Image, ref.Type)
		if _, exists := currentRefSet[key]; !exists {
			// This specific (Image, Type) reference has been dropped
			// But we only want to delete if ALL references to this image are gone
			if _, hasOtherRef := imagesWithRefs[ref.Image]; !hasOtherRef {
				eligibleRefs = append(eligibleRefs, ref)
			}
		}
	}

	eligibleRefs = lo.Uniq(eligibleRefs)

	// Categorize eligible references by type and verify existence
	var eligibleImages []ImageRef
	var eligibleArtifacts []ImageRef
	var eligibleCRI []ImageRef
	var eligibleHelm []ImageRef

	for _, ref := range eligibleRefs {
		switch ref.Type {
		case RefTypePodman:
			podmanClient, err := m.podmanClientFactory(ref.Owner)
			if err != nil {
				return nil, fmt.Errorf("constructing podman client: %w", err)
			}
			// Image providers can reference either images or artifacts - check both
			if podmanClient.ImageExists(ctx, ref.Image) {
				eligibleImages = append(eligibleImages, ref)
			} else if podmanClient.ArtifactExists(ctx, ref.Image) {
				eligibleArtifacts = append(eligibleArtifacts, ref)
			} else {
				m.log.Debugf("Skipping podman reference %s - doesn't exist as image or artifact", ref.Image)
			}

		case RefTypeCRI:
			if m.clients.CRI().ImageExists(ctx, ref.Image) {
				eligibleCRI = append(eligibleCRI, ref)
			} else {
				m.log.Debugf("Skipping CRI reference %s - doesn't exist", ref.Image)
			}

		case RefTypeArtifact:
			podmanClient, err := m.podmanClientFactory(ref.Owner)
			if err != nil {
				return nil, fmt.Errorf("constructing podman client: %w", err)
			}
			if podmanClient.ArtifactExists(ctx, ref.Image) {
				eligibleArtifacts = append(eligibleArtifacts, ref)
			} else {
				m.log.Debugf("Skipping artifact reference %s - doesn't exist", ref.Image)
			}

		case RefTypeHelm:
			resolved, err := m.clients.Helm().IsResolved(ref.Image)
			if err != nil {
				m.log.Debugf("Skipping helm reference %s - error checking: %v", ref.Image, err)
				continue
			}
			if resolved {
				eligibleHelm = append(eligibleHelm, ref)
			} else {
				m.log.Debugf("Skipping helm reference %s - chart not found", ref.Image)
			}

		default:
			m.log.Debugf("Skipping reference %s - unknown type: %s", ref.Image, ref.Type)
		}
	}

	totalEligible := len(eligibleImages) + len(eligibleArtifacts) + len(eligibleCRI) + len(eligibleHelm)
	if totalEligible == 0 {
		m.log.Debug("No previously referenced items have lost their references - nothing to prune")
	} else {
		m.log.Debugf("Found eligible for pruning: %d images, %d artifacts, %d CRI images, %d helm charts",
			len(eligibleImages), len(eligibleArtifacts), len(eligibleCRI), len(eligibleHelm))
	}
	return &EligibleItems{
		Images:    eligibleImages,
		Artifacts: eligibleArtifacts,
		CRI:       eligibleCRI,
		Helm:      eligibleHelm,
	}, nil
}

// refKey creates a unique key for an (Image, Type) tuple.
func refKey(image, refType string) string {
	return image + "|" + refType
}

// extractReferencesFromSpec extracts all image and artifact reference strings from a device spec.
// This mirrors the logic in CollectBaseOCITargets but only returns reference strings, not OCIPullTarget objects.
func (m *manager) extractReferencesFromSpec(ctx context.Context, deviceSpec *v1beta1.DeviceSpec) ([]ImageRef, error) {
	if deviceSpec == nil {
		return nil, nil
	}

	var refs []ImageRef

	// Extract from applications
	if deviceSpec.Applications != nil {
		for _, appSpec := range lo.FromPtr(deviceSpec.Applications) {
			appRefs, err := m.extractReferencesFromApplication(ctx, &appSpec)
			if err != nil {
				appName, _ := provider.ResolveImageAppName(&appSpec)
				return nil, fmt.Errorf("extracting references from application %s: %w", appName, err)
			}
			refs = append(refs, appRefs...)
		}
	}

	// Extract from embedded applications
	embeddedRefs, err := m.extractEmbeddedReferences(ctx)
	if err != nil {
		// Log warning but don't fail - embedded extraction is best-effort
		m.log.Warnf("Failed to extract embedded application references: %v", err)
	} else {
		refs = append(refs, embeddedRefs...)
	}

	return lo.Uniq(refs), nil
}

// extractReferencesFromApplication extracts image and artifact reference strings from a single application spec.
func (m *manager) extractReferencesFromApplication(_ context.Context, appSpec *v1beta1.ApplicationProviderSpec) ([]ImageRef, error) {
	var refs []ImageRef

	owner, err := provider.ResolveUser(appSpec)
	if err != nil {
		return nil, fmt.Errorf("resolving user: %w", err)
	}

	appType, err := (*appSpec).GetAppType()
	if err != nil {
		return nil, fmt.Errorf("%w: image: %w", errors.ErrGettingProviderSpec, err)
	}

	switch appType {
	case v1beta1.AppTypeContainer:
		containerApp, err := (*appSpec).AsContainerApplication()
		if err != nil {
			return nil, fmt.Errorf("getting container application: %w", err)
		}
		refs = append(refs, ImageRef{Image: containerApp.Image, Owner: owner, Type: RefTypePodman})
		if containerApp.Volumes != nil {
			volRefs, err := m.extractVolumeReferences(*containerApp.Volumes)
			if err != nil {
				return nil, fmt.Errorf("extracting volume references: %w", err)
			}
			setOwnerOnRefs(volRefs, owner)
			refs = append(refs, volRefs...)
		}

	case v1beta1.AppTypeCompose:
		composeApp, err := (*appSpec).AsComposeApplication()
		if err != nil {
			return nil, fmt.Errorf("%w: inline: %w", errors.ErrGettingProviderSpec, err)
		}
		providerType, err := composeApp.Type()
		if err != nil {
			return nil, fmt.Errorf("getting compose provider type: %w", err)
		}
		if providerType == v1beta1.ImageApplicationProviderType {
			imageSpec, err := composeApp.AsImageApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting compose image spec: %w", err)
			}
			refs = append(refs, ImageRef{Image: imageSpec.Image, Owner: owner, Type: RefTypePodman})
		} else {
			inlineSpec, err := composeApp.AsInlineApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting compose inline spec: %w", err)
			}
			composeRefs, err := m.extractComposeReferences(inlineSpec.Inline)
			if err != nil {
				return nil, fmt.Errorf("extracting compose references: %w", err)
			}
			setOwnerOnRefs(composeRefs, owner)
			refs = append(refs, composeRefs...)
		}
		if composeApp.Volumes != nil {
			volRefs, err := m.extractVolumeReferences(*composeApp.Volumes)
			if err != nil {
				return nil, fmt.Errorf("extracting volume references: %w", err)
			}
			setOwnerOnRefs(volRefs, owner)
			refs = append(refs, volRefs...)
		}

	case v1beta1.AppTypeQuadlet:
		quadletApp, err := (*appSpec).AsQuadletApplication()
		if err != nil {
			return nil, fmt.Errorf("getting quadlet application: %w", err)
		}
		providerType, err := quadletApp.Type()
		if err != nil {
			return nil, fmt.Errorf("getting quadlet provider type: %w", err)
		}
		if providerType == v1beta1.ImageApplicationProviderType {
			imageSpec, err := quadletApp.AsImageApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting quadlet image spec: %w", err)
			}
			refs = append(refs, ImageRef{Image: imageSpec.Image, Owner: owner, Type: RefTypePodman})
		} else {
			inlineSpec, err := quadletApp.AsInlineApplicationProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting quadlet inline spec: %w", err)
			}
			quadletRefs, err := m.extractQuadletReferences(inlineSpec.Inline)
			if err != nil {
				return nil, fmt.Errorf("extracting quadlet references: %w", err)
			}
			setOwnerOnRefs(quadletRefs, owner)
			refs = append(refs, quadletRefs...)
		}
		if quadletApp.Volumes != nil {
			volRefs, err := m.extractVolumeReferences(*quadletApp.Volumes)
			if err != nil {
				return nil, fmt.Errorf("extracting volume references: %w", err)
			}
			setOwnerOnRefs(volRefs, owner)
			refs = append(refs, volRefs...)
		}

	case v1beta1.AppTypeHelm:
		helmApp, err := (*appSpec).AsHelmApplication()
		if err != nil {
			return nil, fmt.Errorf("getting helm application: %w", err)
		}
		refs = append(refs, ImageRef{Image: helmApp.Image, Owner: owner, Type: RefTypeHelm})

	default:
		return nil, fmt.Errorf("%w: %s", errors.ErrUnsupportedAppType, appType)
	}

	return refs, nil
}

// extractComposeReferences extracts image reference strings from Compose inline content.
func (m *manager) extractComposeReferences(contents []v1beta1.ApplicationContent) ([]ImageRef, error) {
	spec, err := client.ParseComposeFromSpec(contents)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errors.ErrParsingComposeSpec, err)
	}

	var refs []ImageRef
	for _, svc := range spec.Services {
		if svc.Image != "" {
			refs = append(refs, ImageRef{Image: svc.Image, Type: RefTypePodman})
		}
	}

	return refs, nil
}

// extractImagesFromQuadletReferences extracts image reference strings from a map of quadlet references.
// Filters out quadlet file references (e.g., "base.image") - only includes actual OCI image references.
func (m *manager) extractImagesFromQuadletReferences(quadlets map[string]*common.QuadletReferences) []ImageRef {
	var refs []ImageRef
	for _, quad := range quadlets {
		// Extract images from service/container quadlets (only if it's an OCI image, not a quadlet file reference)
		if quad.Image != nil && !quadlet.IsImageReference(*quad.Image) {
			refs = append(refs, ImageRef{Image: *quad.Image, Type: RefTypePodman})
		}

		// Extract mount images (only if they're OCI images, not quadlet file references)
		for _, mountImage := range quad.MountImages {
			if !quadlet.IsImageReference(mountImage) {
				refs = append(refs, ImageRef{Image: mountImage, Type: RefTypePodman})
			}
		}
	}

	return lo.Uniq(refs)
}

// extractQuadletReferences extracts image reference strings from Quadlet inline content.
// Filters out quadlet file references (e.g., "base.image") - only includes actual OCI image references.
func (m *manager) extractQuadletReferences(contents []v1beta1.ApplicationContent) ([]ImageRef, error) {
	quadlets, err := client.ParseQuadletReferencesFromSpec(contents)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errors.ErrParsingQuadletSpec, err)
	}

	return m.extractImagesFromQuadletReferences(quadlets), nil
}

// extractVolumeReferences extracts image/artifact reference strings from application volumes.
// ImageVolume types are artifacts, ImageMountVolume types could be images or artifacts (use auto-detect).
func (m *manager) extractVolumeReferences(volumes []v1beta1.ApplicationVolume) ([]ImageRef, error) {
	var refs []ImageRef

	for _, vol := range volumes {
		volType, err := vol.Type()
		if err != nil {
			return nil, fmt.Errorf("determining volume type: %w", err)
		}

		switch volType {
		case v1beta1.ImageApplicationVolumeProviderType:
			provider, err := vol.AsImageVolumeProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("%w: image volume: %w", errors.ErrGettingProviderSpec, err)
			}
			refs = append(refs, ImageRef{Image: provider.Image.Reference, Type: RefTypeArtifact})

		case v1beta1.ImageMountApplicationVolumeProviderType:
			provider, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("%w: image mount volume: %w", errors.ErrGettingProviderSpec, err)
			}
			// ImageMount volumes could be images or artifacts - type is determined at pruning time
			refs = append(refs, ImageRef{Image: provider.Image.Reference, Type: RefTypePodman})

		case v1beta1.MountApplicationVolumeProviderType:
			// Mount volumes don't have images
			continue

		default:
			// Skip unsupported volume types
			continue
		}
	}

	return lo.Uniq(refs), nil
}

// extractEmbeddedReferences extracts image reference strings from embedded applications in the filesystem.
func (m *manager) extractEmbeddedReferences(ctx context.Context) ([]ImageRef, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var allRefs []ImageRef

	// Extract from embedded compose applications
	composeRefs, err := m.extractEmbeddedComposeReferences(ctx)
	if err != nil {
		m.log.Warnf("Failed to extract embedded compose references: %v", err)
	} else {
		allRefs = append(allRefs, composeRefs...)
	}

	// Extract from embedded quadlet applications
	quadletRefs, err := m.extractEmbeddedQuadletReferences(ctx)
	if err != nil {
		m.log.Warnf("Failed to extract embedded quadlet references: %v", err)
	} else {
		allRefs = append(allRefs, quadletRefs...)
	}

	return lo.Uniq(allRefs), nil
}

// extractEmbeddedComposeReferences extracts image reference strings from embedded compose applications.
func (m *manager) extractEmbeddedComposeReferences(ctx context.Context) ([]ImageRef, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var refs []ImageRef

	elements, err := m.readWriter.ReadDir(lifecycle.EmbeddedComposeAppPath)
	if err != nil {
		// Directory doesn't exist or can't be read - this is expected if no embedded apps
		return nil, nil
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		name := element.Name()
		appPath := filepath.Join(lifecycle.EmbeddedComposeAppPath, name)

		// Search for compose files
		suffixPatterns := []string{"*.yml", "*.yaml"}
		var composeFound bool
		for _, pattern := range suffixPatterns {
			files, err := filepath.Glob(m.readWriter.PathFor(filepath.Join(appPath, pattern)))
			if err != nil {
				continue
			}
			if len(files) > 0 {
				composeFound = true
				break
			}
		}

		if !composeFound {
			continue
		}

		// Parse compose spec to extract images
		spec, err := client.ParseComposeSpecFromDir(m.readWriter, appPath)
		if err != nil {
			// Skip apps that can't be parsed
			m.log.Debugf("Skipping embedded compose app %s: failed to parse: %v", name, err)
			continue
		}

		// Extract images from services
		for _, svc := range spec.Services {
			if svc.Image != "" {
				refs = append(refs, ImageRef{Image: svc.Image, Type: RefTypePodman})
			}
		}
	}

	return lo.Uniq(refs), nil
}

// extractEmbeddedQuadletReferences extracts image reference strings from embedded quadlet applications.
func (m *manager) extractEmbeddedQuadletReferences(ctx context.Context) ([]ImageRef, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var refs []ImageRef

	elements, err := m.readWriter.ReadDir(lifecycle.EmbeddedQuadletAppPath)
	if err != nil {
		// Directory doesn't exist or can't be read - this is expected if no embedded apps
		return nil, nil
	}

	for _, element := range elements {
		if !element.IsDir() {
			continue
		}

		name := element.Name()
		appPath := filepath.Join(lifecycle.EmbeddedQuadletAppPath, name)

		// Parse quadlet references from directory
		quadletRefs, err := client.ParseQuadletReferencesFromDir(m.readWriter, appPath)
		if err != nil {
			// Skip apps that can't be parsed
			m.log.Debugf("Skipping embedded quadlet app %s: failed to parse: %v", name, err)
			continue
		}

		// Extract images from quadlet references
		// Filter out quadlet file references (e.g., "base.image") - only include actual OCI image references
		appRefs := m.extractImagesFromQuadletReferences(quadletRefs)
		refs = append(refs, appRefs...)
	}

	return lo.Uniq(refs), nil
}

// extractNestedTargetsFromSpec extracts nested OCI targets (images and artifacts) from image-based applications.
// This ensures that artifacts referenced inside images (e.g., in Compose files) are preserved during pruning.
// Returns a list of image/artifact references found in nested targets.
// Errors during extraction are logged but don't block collection (best-effort for pruning).
func (m *manager) extractNestedTargetsFromSpec(ctx context.Context, device *v1beta1.Device) ([]ImageRef, error) {
	if device == nil || device.Spec == nil || device.Spec.Applications == nil {
		return nil, nil
	}

	var allReferences []ImageRef
	appDataToCleanup := []*provider.AppData{}

	// Process each application in the spec
	for _, appSpec := range lo.FromPtr(device.Spec.Applications) {
		appName, _ := provider.ResolveImageAppName(&appSpec)

		// Only image-based apps have nested targets extracted from parent images
		needsExtraction, err := provider.AppNeedsNestedExtraction(&appSpec)
		if err != nil {
			m.log.Debugf("Skipping app %s: failed to check nested extraction: %v", appName, err)
			continue
		}

		if !needsExtraction {
			continue
		}

		imageName, err := provider.ResolveImageRef(&appSpec)
		if err != nil {
			m.log.Debugf("Skipping app %s: failed to resolve image ref: %v", appName, err)
			continue
		}

		user, err := provider.ResolveUser(&appSpec)
		if err != nil {
			m.log.Debugf("Skipping app %s: failed to resolve user: %v", appName, err)
			continue
		}

		appType, err := appSpec.GetAppType()
		if err != nil {
			m.log.Debugf("Skipping app %s: failed to get app type: %v", appName, err)
			continue
		}

		// Check if the image/artifact/chart exists locally (required for extraction)
		var exists bool
		if appType == v1beta1.AppTypeHelm {
			exists, _ = m.clients.Helm().IsResolved(imageName)
		} else {
			podmanClient, err := m.podmanClientFactory(user)
			if err != nil {
				m.log.Errorf("Skipping app %s: failed to create podman client: %v", appName, err)
				continue
			}
			exists = podmanClient.ImageExists(ctx, imageName) || podmanClient.ArtifactExists(ctx, imageName)
		}
		if !exists {
			// Image/chart not available locally - skip nested extraction (best-effort for pruning)
			m.log.Debugf("Skipping nested extraction for app %s: %s not available locally", appName, imageName)
			continue
		}

		// Extract nested targets from the image
		// Pass nil for pullSecret since we're only extracting from already-pulled images
		appData, err := provider.ExtractNestedTargetsFromImage(
			ctx,
			m.log,
			m.podmanClientFactory,
			m.clients,
			m.rwFactory,
			&appSpec,
			nil, // pullSecret not needed for extraction from local images
		)
		if err != nil {
			// Log warning but continue - nested extraction is best-effort for pruning
			m.log.Debugf("Failed to extract nested targets from app %s (%s): %v", appName, imageName, err)
			continue
		}

		if appData == nil {
			continue
		}

		// Determine the type for nested references based on app type
		// Helm apps use CRI for workload images, other apps use podman
		nestedRefType := RefTypePodman
		if appType == v1beta1.AppTypeHelm {
			nestedRefType = RefTypeCRI
		}

		// Collect reference strings from extracted targets
		for _, target := range appData.Targets {
			if target.Reference != "" {
				allReferences = append(allReferences, ImageRef{Image: target.Reference, Owner: user, Type: nestedRefType})
			}
		}

		// Track AppData for cleanup
		appDataToCleanup = append(appDataToCleanup, appData)
	}

	// Clean up extracted AppData (temporary files, etc.)
	for _, appData := range appDataToCleanup {
		if err := appData.Cleanup(); err != nil {
			m.log.Debugf("Failed to cleanup AppData: %v", err)
		}
	}

	// Return unique references
	return lo.Uniq(allReferences), nil
}

// validateCapability verifies that capability is maintained after pruning operations.
// It checks that current and desired application images and OS images still exist.
func (m *manager) validateCapability(ctx context.Context) error {
	m.log.Debug("Validating capability after pruning")

	// Read current spec
	currentDevice, err := m.specManager.Read(spec.Current)
	if err != nil {
		return fmt.Errorf("reading current spec for validation: %w", err)
	}

	// Read desired spec (may not exist)
	desiredDevice, err := m.specManager.Read(spec.Desired)
	if err != nil {
		m.log.Debugf("Desired spec not available for validation: %v", err)
		// Desired spec may not exist - this is acceptable
		desiredDevice = nil
	}

	var missingImages []ImageRef

	// Validate current application images
	if currentDevice != nil && currentDevice.Spec != nil {
		// Extract image/artifact references directly from the spec
		var currentImages []ImageRef
		specRefs, err := m.extractReferencesFromSpec(ctx, currentDevice.Spec)
		if err != nil {
			return fmt.Errorf("extracting current images for validation: %w", err)
		}
		currentImages = append(currentImages, specRefs...)
		for _, img := range currentImages {
			exists := m.checkRefExists(ctx, img)
			if !exists {
				missingImages = append(missingImages, img)
				m.log.Warnf("Current application image missing after pruning: %s", img)
			}
		}

		// Validate current OS image
		if currentDevice.Spec.Os != nil && currentDevice.Spec.Os.Image != "" {
			osImage := currentDevice.Spec.Os.Image
			exists := m.rootPodmanClient.ImageExists(ctx, osImage)
			if !exists {
				missingImages = append(missingImages, ImageRef{Image: osImage})
				m.log.Warnf("Current OS image missing after pruning: %s", osImage)
			}
		}
	}

	// Validate desired application images
	if desiredDevice != nil && desiredDevice.Spec != nil {
		// Extract image/artifact references directly from the spec
		var desiredImages []ImageRef
		specRefs, err := m.extractReferencesFromSpec(ctx, desiredDevice.Spec)
		if err != nil {
			return fmt.Errorf("extracting desired images for validation: %w", err)
		}
		desiredImages = append(desiredImages, specRefs...)
		for _, ref := range desiredImages {
			exists := m.checkRefExists(ctx, ref)
			if !exists {
				missingImages = append(missingImages, ref)
				m.log.Warnf("Desired application image missing after pruning: %s", ref)
			}
		}

		// Validate desired OS image
		if desiredDevice.Spec.Os != nil && desiredDevice.Spec.Os.Image != "" {
			osImage := desiredDevice.Spec.Os.Image
			exists := m.rootPodmanClient.ImageExists(ctx, osImage)
			if !exists {
				missingImages = append(missingImages, ImageRef{Image: osImage})
				m.log.Warnf("Desired OS image missing after pruning: %s", osImage)
			}
		}
	}

	if len(missingImages) > 0 {
		return fmt.Errorf("capability compromised - missing images: %v", missingImages)
	}

	m.log.Debug("Capability validated successfully")
	return nil
}

func (m *manager) checkRefExists(ctx context.Context, ref ImageRef) bool {
	switch ref.Type {
	case RefTypeHelm:
		exists, _ := m.clients.Helm().IsResolved(ref.Image)
		return exists
	case RefTypeCRI:
		return m.clients.CRI().ImageExists(ctx, ref.Image)
	case RefTypeArtifact:
		podmanClient, err := m.podmanClientFactory(ref.Owner)
		if err != nil {
			m.log.Errorf("Failed to construct podman client for artifact check: %v", err)
			return false
		}
		return podmanClient.ArtifactExists(ctx, ref.Image)
	default:
		podmanClient, err := m.podmanClientFactory(ref.Owner)
		if err != nil {
			m.log.Errorf("Failed to construct podman client for image check: %v", err)
			return false
		}
		return podmanClient.ImageExists(ctx, ref.Image) || podmanClient.ArtifactExists(ctx, ref.Image)
	}
}

// removeEligibleImages removes the list of eligible images from Podman storage.
// It returns the count of successfully removed images, the list of successfully removed image references, and any error encountered.
// Errors during individual removals are logged but don't stop the process.
func (m *manager) removeEligibleImages(ctx context.Context, eligibleImages []ImageRef) (int, []ImageRef, error) {
	var removedCount int
	var removedRefs []ImageRef
	var removalErrors []error

	for _, ref := range eligibleImages {
		podmanClient, err := m.podmanClientFactory(ref.Owner)
		if err != nil {
			return 0, nil, fmt.Errorf("constructing podman client: %w", err)
		}
		// Check if image exists before attempting removal
		imageExists := podmanClient.ImageExists(ctx, ref.Image)

		if imageExists {
			if err := podmanClient.RemoveImage(ctx, ref.Image); err != nil {
				m.log.Warnf("Failed to remove image %s: %v", ref.Image, err)
				removalErrors = append(removalErrors, fmt.Errorf("failed to remove image %s: %w", ref.Image, err))
				continue
			}
			removedCount++
			m.log.Debugf("Removed image: %s", ref.Image)
		}
		removedRefs = append(removedRefs, ref)
	}

	// Return error only if all removals failed
	if len(removalErrors) == len(eligibleImages) && len(eligibleImages) > 0 {
		return removedCount, removedRefs, fmt.Errorf("all image removals failed: %d errors", len(removalErrors))
	}

	// Log summary if there were any failures
	if len(removalErrors) > 0 {
		m.log.Warnf("Image pruning completed with %d failures out of %d attempts", len(removalErrors), len(eligibleImages))
	}

	return removedCount, removedRefs, nil
}

// removeEligibleArtifacts removes the list of eligible artifacts from Podman storage.
// It returns the count of successfully removed artifacts, the list of successfully removed artifact references, and any error encountered.
// Errors during individual removals are logged but don't stop the process.
func (m *manager) removeEligibleArtifacts(ctx context.Context, eligibleArtifacts []ImageRef) (int, []ImageRef, error) {
	var removedCount int
	var removedRefs []ImageRef
	var removalErrors []error

	for _, ref := range eligibleArtifacts {
		podmanClient, err := m.podmanClientFactory(ref.Owner)
		if err != nil {
			return 0, nil, fmt.Errorf("constructing podman client: %w", err)
		}
		// Check if artifact exists before attempting removal
		artifactExists := podmanClient.ArtifactExists(ctx, ref.Image)

		if artifactExists {
			if err := podmanClient.RemoveArtifact(ctx, ref.Image); err != nil {
				m.log.Warnf("Failed to remove artifact %s: %v", ref, err)
				removalErrors = append(removalErrors, fmt.Errorf("failed to remove artifact %s: %w", ref, err))
				continue
			}
			removedCount++
			m.log.Debugf("Removed artifact: %s", ref)
		}
		removedRefs = append(removedRefs, ref)
	}

	// Return error only if all removals failed
	if len(removalErrors) == len(eligibleArtifacts) && len(eligibleArtifacts) > 0 {
		return removedCount, removedRefs, fmt.Errorf("all artifact removals failed: %d errors", len(removalErrors))
	}

	// Log summary if there were any failures
	if len(removalErrors) > 0 {
		m.log.Warnf("Artifact pruning completed with %d failures out of %d attempts", len(removalErrors), len(eligibleArtifacts))
	}

	return removedCount, removedRefs, nil
}

// removeEligibleCRIImages removes the list of eligible CRI images.
// It returns the count of successfully removed images, the list of successfully removed image references, and any error encountered.
// Errors during individual removals are logged but don't stop the process.
func (m *manager) removeEligibleCRIImages(ctx context.Context, eligibleImages []ImageRef) (int, []ImageRef, error) {
	var removedCount int
	var removedRefs []ImageRef
	var removalErrors []error

	criClient := m.clients.CRI()

	for _, ref := range eligibleImages {
		if criClient.ImageExists(ctx, ref.Image) {
			if err := criClient.RemoveImage(ctx, ref.Image); err != nil {
				m.log.Warnf("Failed to remove CRI image %s: %v", ref.Image, err)
				removalErrors = append(removalErrors, fmt.Errorf("failed to remove CRI image %s: %w", ref.Image, err))
				continue
			}
			removedCount++
			m.log.Debugf("Removed CRI image: %s", ref.Image)
		}
		removedRefs = append(removedRefs, ref)
	}

	// Return error only if all removals failed
	if len(removalErrors) == len(eligibleImages) && len(eligibleImages) > 0 {
		return removedCount, removedRefs, fmt.Errorf("all CRI image removals failed: %d errors", len(removalErrors))
	}

	// Log summary if there were any failures
	if len(removalErrors) > 0 {
		m.log.Warnf("CRI image pruning completed with %d failures out of %d attempts", len(removalErrors), len(eligibleImages))
	}

	return removedCount, removedRefs, nil
}

// removeEligibleHelmCharts removes the list of eligible Helm charts from the cache.
// It returns the count of successfully removed charts, the list of successfully removed chart references, and any error encountered.
// Errors during individual removals are logged but don't stop the process.
func (m *manager) removeEligibleHelmCharts(eligibleCharts []ImageRef) (int, []ImageRef, error) {
	var removedCount int
	var removedRefs []ImageRef
	var removalErrors []error

	helmClient := m.clients.Helm()

	for _, ref := range eligibleCharts {
		resolved, err := helmClient.IsResolved(ref.Image)
		if err != nil {
			m.log.Warnf("Failed to check Helm chart %s: %v", ref.Image, err)
			removalErrors = append(removalErrors, fmt.Errorf("failed to check helm chart %s: %w", ref.Image, err))
			continue
		}

		if resolved {
			if err := helmClient.RemoveChart(ref.Image); err != nil {
				m.log.Warnf("Failed to remove Helm chart %s: %v", ref.Image, err)
				removalErrors = append(removalErrors, fmt.Errorf("failed to remove helm chart %s: %w", ref.Image, err))
				continue
			}
			removedCount++
			m.log.Debugf("Removed Helm chart: %s", ref.Image)
		}
		removedRefs = append(removedRefs, ref)
	}

	// Return error only if all removals failed
	if len(removalErrors) == len(eligibleCharts) && len(eligibleCharts) > 0 {
		return removedCount, removedRefs, fmt.Errorf("all Helm chart removals failed: %d errors", len(removalErrors))
	}

	// Log summary if there were any failures
	if len(removalErrors) > 0 {
		m.log.Warnf("Helm chart pruning completed with %d failures out of %d attempts", len(removalErrors), len(eligibleCharts))
	}

	return removedCount, removedRefs, nil
}
