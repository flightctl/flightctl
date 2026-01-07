package imagepruning

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
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
	podmanClient *client.Podman
	specManager  spec.Manager
	readWriter   fileio.ReadWriter
	log          *log.PrefixLogger
	config       atomic.Pointer[ImagePruningConfig]
	dataDir      string
	prunePending atomic.Bool
}

// New creates a new pruning manager instance.
//
// Dependencies:
//   - podmanClient: Podman client for image/artifact operations
//   - specManager: Spec manager for reading current and desired specs
//   - readWriter: File I/O interface for any file operations
//   - log: Logger for structured logging
//   - config: Pruning configuration (enabled flag, etc.)
//   - dataDir: Directory where data files are stored
func New(
	podmanClient *client.Podman,
	specManager spec.Manager,
	readWriter fileio.ReadWriter,
	log *log.PrefixLogger,
	config ImagePruningConfig,
	dataDir string,
) Manager {
	ret := &manager{
		podmanClient: podmanClient,
		specManager:  specManager,
		readWriter:   readWriter,
		log:          log,
		dataDir:      dataDir,
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

	totalEligible := len(eligible.Images) + len(eligible.Artifacts)

	var removedImages int
	var removedImageRefs []string
	var removedArtifacts int
	var removedArtifactRefs []string

	if totalEligible > 0 {
		m.log.Infof("Starting pruning of %d eligible images and %d eligible artifacts", len(eligible.Images), len(eligible.Artifacts))

		// Remove eligible images and artifacts separately, tracking which ones were successfully removed
		var err error
		removedImages, removedImageRefs, err = m.removeEligibleImages(ctx, eligible.Images)
		if err != nil {
			m.log.Warnf("Error during image removal: %v", err)
			// Continue with artifact removal even if image removal failed
		}

		removedArtifacts, removedArtifactRefs, err = m.removeEligibleArtifacts(ctx, eligible.Artifacts)
		if err != nil {
			m.log.Warnf("Error during artifact removal: %v", err)
			// Continue with validation even if some removals failed
		}

		m.log.Infof("Pruning complete: removed %d of %d eligible images, %d of %d eligible artifacts", removedImages, len(eligible.Images), removedArtifacts, len(eligible.Artifacts))
	} else {
		m.log.Debug("No images or artifacts eligible for pruning")
	}

	// Remove successfully pruned items from the references file
	// This ensures the accumulated file only contains items that still exist or haven't been pruned yet
	if len(removedImageRefs) > 0 || len(removedArtifactRefs) > 0 {
		if err := m.removePrunedReferencesFromFile(removedImageRefs, removedArtifactRefs); err != nil {
			m.log.Warnf("Failed to remove pruned references from file: %v", err)
			// Don't block reconciliation on file update errors
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
func (m *manager) getImageReferencesFromSpecs(ctx context.Context, device *v1beta1.Device) ([]string, error) {
	if device == nil || device.Spec == nil {
		return nil, nil
	}

	var images []string

	// Extract image/artifact references directly from the spec
	// This mirrors the logic in CollectBaseOCITargets but only extracts reference strings
	specRefs, err := m.extractReferencesFromSpec(ctx, device.Spec)
	if err != nil {
		return nil, fmt.Errorf("extracting references from spec: %w", err)
	}
	images = append(images, specRefs...)

	// Extract OS image (not included in extractReferencesFromSpec)
	if device.Spec.Os != nil && device.Spec.Os.Image != "" {
		images = append(images, device.Spec.Os.Image)
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
	Timestamp  string   `json:"timestamp"`
	References []string `json:"references"` // Single list of all references (no categorization)
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
	var allRefs []string

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
func (m *manager) removePrunedReferencesFromFile(removedImages []string, removedArtifacts []string) error {
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

// EligibleItems holds separate lists of eligible images and artifacts for pruning.
type EligibleItems struct {
	Images    []string
	Artifacts []string
}

// determineEligibleImages determines which images and artifacts are eligible for pruning.
// It only prunes items that were previously referenced (according to the references file)
// but are no longer referenced in the current specs.
// OS images are included and can be pruned if they lose their references.
// Returns separate lists for images and artifacts.
func (m *manager) determineEligibleImages(ctx context.Context) (*EligibleItems, error) {
	m.log.Debug("Determining eligible images and artifacts for pruning")

	// Read previous references from file
	previousRefs := m.readPreviousReferences()
	if previousRefs == nil {
		// No previous references file - this is the first run, so nothing to prune
		m.log.Debug("No previous references file found - skipping pruning on first run")
		return &EligibleItems{
			Images:    []string{},
			Artifacts: []string{},
		}, nil
	}

	// Get current required references from specs (includes application images, OS images, and artifacts)
	// Note: We don't need to list all images/artifacts upfront anymore.
	// Categorization happens per-reference during pruning when we check existence.
	var currentRequiredRefs []string

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

	currentRequiredRefs = lo.Uniq(currentRequiredRefs)

	// Get previous references (single list, no categorization)
	previousReferences := previousRefs.References

	// Find references that were previously recorded but are no longer in current specs
	eligibleReferences := lo.Without(lo.Uniq(previousReferences), currentRequiredRefs...)

	// Categorize eligible references during pruning (when we can check existence)
	// Only include references that we successfully categorize
	var eligibleImages []string
	var eligibleArtifacts []string

	for _, ref := range eligibleReferences {
		// Check if it exists as an image
		if m.podmanClient.ImageExists(ctx, ref) {
			eligibleImages = append(eligibleImages, ref)
			continue
		}
		// Check if it exists as an artifact
		if m.podmanClient.ArtifactExists(ctx, ref) {
			eligibleArtifacts = append(eligibleArtifacts, ref)
			continue
		}
		// If it doesn't exist as either, skip it (can't prune what doesn't exist)
		// We don't remove it from the references file as it might be currently being downloaded
		m.log.Debugf("Skipping reference %s - doesn't exist as image or artifact (might be downloading)", ref)
	}

	if len(eligibleImages) == 0 && len(eligibleArtifacts) == 0 {
		m.log.Debug("No previously referenced items have lost their references - nothing to prune")
	} else {
		m.log.Debugf("Found %d eligible images and %d eligible artifacts for pruning (previously referenced but no longer)", len(eligibleImages), len(eligibleArtifacts))
	}
	return &EligibleItems{
		Images:    eligibleImages,
		Artifacts: eligibleArtifacts,
	}, nil
}

// extractReferencesFromSpec extracts all image and artifact reference strings from a device spec.
// This mirrors the logic in CollectBaseOCITargets but only returns reference strings, not OCIPullTarget objects.
func (m *manager) extractReferencesFromSpec(ctx context.Context, deviceSpec *v1beta1.DeviceSpec) ([]string, error) {
	if deviceSpec == nil {
		return nil, nil
	}

	var refs []string

	// Extract from applications
	if deviceSpec.Applications != nil {
		for _, appSpec := range lo.FromPtr(deviceSpec.Applications) {
			appRefs, err := m.extractReferencesFromApplication(ctx, &appSpec)
			if err != nil {
				return nil, fmt.Errorf("extracting references from application %s: %w", lo.FromPtr(appSpec.Name), err)
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
func (m *manager) extractReferencesFromApplication(ctx context.Context, appSpec *v1beta1.ApplicationProviderSpec) ([]string, error) {
	var refs []string

	providerType, err := appSpec.Type()
	if err != nil {
		return nil, fmt.Errorf("determining provider type: %w", err)
	}

	switch providerType {
	case v1beta1.ImageApplicationProviderType:
		imageSpec, err := appSpec.AsImageApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting image provider spec: %w", err)
		}
		refs = append(refs, imageSpec.Image)

		// Extract volume references
		if imageSpec.Volumes != nil {
			volRefs, err := m.extractVolumeReferences(*imageSpec.Volumes)
			if err != nil {
				return nil, fmt.Errorf("extracting volume references: %w", err)
			}
			refs = append(refs, volRefs...)
		}

	case v1beta1.InlineApplicationProviderType:
		inlineSpec, err := appSpec.AsInlineApplicationProviderSpec()
		if err != nil {
			return nil, fmt.Errorf("getting inline provider spec: %w", err)
		}

		// Extract references from inline content based on app type
		switch appSpec.AppType {
		case v1beta1.AppTypeCompose:
			composeRefs, err := m.extractComposeReferences(inlineSpec.Inline)
			if err != nil {
				return nil, fmt.Errorf("extracting compose references: %w", err)
			}
			refs = append(refs, composeRefs...)

		case v1beta1.AppTypeQuadlet:
			quadletRefs, err := m.extractQuadletReferences(inlineSpec.Inline)
			if err != nil {
				return nil, fmt.Errorf("extracting quadlet references: %w", err)
			}
			refs = append(refs, quadletRefs...)

		default:
			return nil, fmt.Errorf("unsupported app type for inline provider: %s", appSpec.AppType)
		}

		// Extract volume references
		if inlineSpec.Volumes != nil {
			volRefs, err := m.extractVolumeReferences(*inlineSpec.Volumes)
			if err != nil {
				return nil, fmt.Errorf("extracting volume references: %w", err)
			}
			refs = append(refs, volRefs...)
		}

	default:
		return nil, fmt.Errorf("unsupported application provider type: %s", providerType)
	}

	return refs, nil
}

// extractComposeReferences extracts image reference strings from Compose inline content.
func (m *manager) extractComposeReferences(contents []v1beta1.ApplicationContent) ([]string, error) {
	spec, err := client.ParseComposeFromSpec(contents)
	if err != nil {
		return nil, fmt.Errorf("parsing compose spec: %w", err)
	}

	var refs []string
	for _, svc := range spec.Services {
		if svc.Image != "" {
			refs = append(refs, svc.Image)
		}
	}

	return refs, nil
}

// extractImagesFromQuadletReferences extracts image reference strings from a map of quadlet references.
// Filters out quadlet file references (e.g., "base.image") - only includes actual OCI image references.
func (m *manager) extractImagesFromQuadletReferences(quadlets map[string]*common.QuadletReferences) []string {
	var refs []string
	for _, quad := range quadlets {
		// Extract images from service/container quadlets (only if it's an OCI image, not a quadlet file reference)
		if quad.Image != nil && !quadlet.IsImageReference(*quad.Image) {
			refs = append(refs, *quad.Image)
		}

		// Extract mount images (only if they're OCI images, not quadlet file references)
		for _, mountImage := range quad.MountImages {
			if !quadlet.IsImageReference(mountImage) {
				refs = append(refs, mountImage)
			}
		}
	}

	return lo.Uniq(refs)
}

// extractQuadletReferences extracts image reference strings from Quadlet inline content.
// Filters out quadlet file references (e.g., "base.image") - only includes actual OCI image references.
func (m *manager) extractQuadletReferences(contents []v1beta1.ApplicationContent) ([]string, error) {
	quadlets, err := client.ParseQuadletReferencesFromSpec(contents)
	if err != nil {
		return nil, fmt.Errorf("parsing quadlet spec: %w", err)
	}

	return m.extractImagesFromQuadletReferences(quadlets), nil
}

// extractVolumeReferences extracts image/artifact reference strings from application volumes.
func (m *manager) extractVolumeReferences(volumes []v1beta1.ApplicationVolume) ([]string, error) {
	var refs []string

	for _, vol := range volumes {
		volType, err := vol.Type()
		if err != nil {
			return nil, fmt.Errorf("determining volume type: %w", err)
		}

		switch volType {
		case v1beta1.ImageApplicationVolumeProviderType:
			provider, err := vol.AsImageVolumeProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting image volume provider spec: %w", err)
			}
			refs = append(refs, provider.Image.Reference)

		case v1beta1.ImageMountApplicationVolumeProviderType:
			provider, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				return nil, fmt.Errorf("getting image mount volume provider spec: %w", err)
			}
			refs = append(refs, provider.Image.Reference)

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
func (m *manager) extractEmbeddedReferences(ctx context.Context) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var allRefs []string

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
func (m *manager) extractEmbeddedComposeReferences(ctx context.Context) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var refs []string

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
				refs = append(refs, svc.Image)
			}
		}
	}

	return lo.Uniq(refs), nil
}

// extractEmbeddedQuadletReferences extracts image reference strings from embedded quadlet applications.
func (m *manager) extractEmbeddedQuadletReferences(ctx context.Context) ([]string, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	var refs []string

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
func (m *manager) extractNestedTargetsFromSpec(ctx context.Context, device *v1beta1.Device) ([]string, error) {
	if device == nil || device.Spec == nil || device.Spec.Applications == nil {
		return []string{}, nil
	}

	var allReferences []string
	appDataToCleanup := []*provider.AppData{}

	// Process each application in the spec
	for _, appSpec := range lo.FromPtr(device.Spec.Applications) {
		// Only image-based apps have nested targets extracted from parent images
		providerType, err := appSpec.Type()
		if err != nil {
			m.log.Debugf("Skipping app %s: failed to get provider type: %v", lo.FromPtr(appSpec.Name), err)
			continue
		}

		if providerType != v1beta1.ImageApplicationProviderType {
			continue
		}

		imageSpec, err := appSpec.AsImageApplicationProviderSpec()
		if err != nil {
			m.log.Debugf("Skipping app %s: failed to get image spec: %v", lo.FromPtr(appSpec.Name), err)
			continue
		}

		imageRef := imageSpec.Image

		// Check if the image/artifact exists locally (required for extraction)
		exists := m.podmanClient.ImageExists(ctx, imageRef) || m.podmanClient.ArtifactExists(ctx, imageRef)
		if !exists {
			// Image not available locally - skip nested extraction (best-effort for pruning)
			m.log.Debugf("Skipping nested extraction for app %s: image %s not available locally", lo.FromPtr(appSpec.Name), imageRef)
			continue
		}

		// Extract nested targets from the image
		// Pass nil for pullSecret since we're only extracting from already-pulled images
		appData, err := provider.ExtractNestedTargetsFromImage(
			ctx,
			m.log,
			m.podmanClient,
			m.readWriter,
			&appSpec,
			&imageSpec,
			nil, // pullSecret not needed for extraction from local images
		)
		if err != nil {
			// Log warning but continue - nested extraction is best-effort for pruning
			m.log.Debugf("Failed to extract nested targets from app %s (image %s): %v", lo.FromPtr(appSpec.Name), imageRef, err)
			continue
		}

		if appData == nil {
			continue
		}

		// Collect reference strings from extracted targets
		for _, target := range appData.Targets {
			if target.Reference != "" {
				allReferences = append(allReferences, target.Reference)
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

	var missingImages []string

	// Validate current application images
	if currentDevice != nil && currentDevice.Spec != nil {
		// Extract image/artifact references directly from the spec
		var currentImages []string
		specRefs, err := m.extractReferencesFromSpec(ctx, currentDevice.Spec)
		if err != nil {
			return fmt.Errorf("extracting current images for validation: %w", err)
		}
		currentImages = append(currentImages, specRefs...)
		// Add OS image if present (will be validated separately below)
		for _, img := range currentImages {
			exists := m.podmanClient.ImageExists(ctx, img)
			if !exists {
				exists = m.podmanClient.ArtifactExists(ctx, img)
			}
			if !exists {
				missingImages = append(missingImages, img)
				m.log.Warnf("Current application image missing after pruning: %s", img)
			}
		}

		// Validate current OS image
		if currentDevice.Spec.Os != nil && currentDevice.Spec.Os.Image != "" {
			osImage := currentDevice.Spec.Os.Image
			exists := m.podmanClient.ImageExists(ctx, osImage)
			if !exists {
				missingImages = append(missingImages, osImage)
				m.log.Warnf("Current OS image missing after pruning: %s", osImage)
			}
		}
	}

	// Validate desired application images
	if desiredDevice != nil && desiredDevice.Spec != nil {
		// Extract image/artifact references directly from the spec
		var desiredImages []string
		specRefs, err := m.extractReferencesFromSpec(ctx, desiredDevice.Spec)
		if err != nil {
			return fmt.Errorf("extracting desired images for validation: %w", err)
		}
		desiredImages = append(desiredImages, specRefs...)
		for _, img := range desiredImages {
			exists := m.podmanClient.ImageExists(ctx, img)
			if !exists {
				exists = m.podmanClient.ArtifactExists(ctx, img)
			}
			if !exists {
				missingImages = append(missingImages, img)
				m.log.Warnf("Desired application image missing after pruning: %s", img)
			}
		}

		// Validate desired OS image
		if desiredDevice.Spec.Os != nil && desiredDevice.Spec.Os.Image != "" {
			osImage := desiredDevice.Spec.Os.Image
			exists := m.podmanClient.ImageExists(ctx, osImage)
			if !exists {
				missingImages = append(missingImages, osImage)
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

// removeEligibleImages removes the list of eligible images from Podman storage.
// It returns the count of successfully removed images, the list of successfully removed image references, and any error encountered.
// Errors during individual removals are logged but don't stop the process.
func (m *manager) removeEligibleImages(ctx context.Context, eligibleImages []string) (int, []string, error) {
	var removedCount int
	var removedRefs []string
	var removalErrors []error

	for _, imageRef := range eligibleImages {
		// Check if image exists before attempting removal
		imageExists := m.podmanClient.ImageExists(ctx, imageRef)

		if imageExists {
			if err := m.podmanClient.RemoveImage(ctx, imageRef); err != nil {
				m.log.Warnf("Failed to remove image %s: %v", imageRef, err)
				removalErrors = append(removalErrors, fmt.Errorf("failed to remove image %s: %w", imageRef, err))
				continue
			}
			removedCount++
			m.log.Debugf("Removed image: %s", imageRef)
		}
		removedRefs = append(removedRefs, imageRef)
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
func (m *manager) removeEligibleArtifacts(ctx context.Context, eligibleArtifacts []string) (int, []string, error) {
	var removedCount int
	var removedRefs []string
	var removalErrors []error

	for _, artifactRef := range eligibleArtifacts {
		// Check if artifact exists before attempting removal
		artifactExists := m.podmanClient.ArtifactExists(ctx, artifactRef)

		if artifactExists {
			if err := m.podmanClient.RemoveArtifact(ctx, artifactRef); err != nil {
				m.log.Warnf("Failed to remove artifact %s: %v", artifactRef, err)
				removalErrors = append(removalErrors, fmt.Errorf("failed to remove artifact %s: %w", artifactRef, err))
				continue
			}
			removedCount++
			m.log.Debugf("Removed artifact: %s", artifactRef)
		}
		removedRefs = append(removedRefs, artifactRef)
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
