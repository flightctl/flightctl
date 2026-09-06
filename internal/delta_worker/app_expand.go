package delta_worker

import (
	"context"
	"encoding/json"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
)

// inspectFn resolves an image reference to its content digest.
type inspectFn func(ctx context.Context, orgId uuid.UUID, image string) (string, error)

// expandAppCandidates extracts application image pairs from the rendered spec
// and appends them to the existing (OS) candidates. For each application in
// the rendered spec it extracts parent and nested image references, pairs them
// with the current digest from the device's status.applications[].imageDigests,
// and resolves the new digest via inspect.
func expandAppCandidates(
	ctx context.Context,
	orgId uuid.UUID,
	device *domain.Device,
	rendered tasks.RenderedSpec,
	candidates []DeltaCandidate,
	inspect inspectFn,
) []DeltaCandidate {
	if len(rendered.Applications) == 0 {
		return candidates
	}

	var apps []domain.ApplicationProviderSpec
	if err := json.Unmarshal(rendered.Applications, &apps); err != nil {
		return candidates
	}

	digestIndex := buildDigestIndex(device)

	for i := range apps {
		refs := extractNewImageRefs(&apps[i])
		for _, ref := range refs {
			cand, ok := pairCandidate(ctx, orgId, ref, digestIndex, inspect)
			if ok {
				candidates = append(candidates, cand)
			}
		}
	}
	return candidates
}

// buildDigestIndex builds a lookup from image reference to digest from the
// device's current application statuses.
func buildDigestIndex(device *domain.Device) map[string]string {
	if device == nil || device.Status == nil {
		return nil
	}
	idx := make(map[string]string)
	for _, app := range device.Status.Applications {
		if app.ImageDigests == nil {
			continue
		}
		for _, entry := range *app.ImageDigests {
			if entry.Image != "" && entry.Digest != "" {
				idx[entry.Image] = entry.Digest
			}
		}
	}
	return idx
}

// pairCandidate pairs a new image reference with its current digest and
// resolves the new digest via inspect. Returns false if the pair should be
// skipped (missing current digest, inspect failure, or same digest).
func pairCandidate(
	ctx context.Context,
	orgId uuid.UUID,
	newImageRef string,
	digestIndex map[string]string,
	inspect inspectFn,
) (DeltaCandidate, bool) {
	currentDigest, ok := digestIndex[newImageRef]
	if !ok || currentDigest == "" {
		return DeltaCandidate{}, false
	}

	repo, err := imageRepository(newImageRef)
	if err != nil {
		return DeltaCandidate{}, false
	}

	newDigest, err := inspect(ctx, orgId, newImageRef)
	if err != nil || newDigest == "" {
		return DeltaCandidate{}, false
	}

	if currentDigest == newDigest {
		return DeltaCandidate{}, false
	}

	return DeltaCandidate{
		ImageRepository: repo,
		CurrentDigest:   currentDigest,
		NewDigest:       newDigest,
	}, true
}

// extractNewImageRefs extracts all image references from a rendered application
// spec. These are the "new" images the device will pull after the update.
func extractNewImageRefs(app *domain.ApplicationProviderSpec) []string {
	appType, err := (*app).GetAppType()
	if err != nil {
		return nil
	}

	var refs []string
	switch appType {
	case domain.AppTypeContainer:
		refs = extractContainerRefs(app)
	case domain.AppTypeCompose:
		refs = extractComposeRefs(app)
	case domain.AppTypeQuadlet:
		refs = extractQuadletRefs(app)
	case domain.AppTypeHelm:
		refs = extractHelmRefs(app)
	}

	refs = append(refs, extractVolumeImageRefs(app)...)
	return deduplicateStrings(refs)
}

// extractContainerRefs extracts the parent image from a container application.
func extractContainerRefs(app *domain.ApplicationProviderSpec) []string {
	container, err := (*app).AsContainerApplication()
	if err != nil {
		return nil
	}
	imageSpec, err := container.AsImageApplicationProviderSpec()
	if err != nil || imageSpec.Image == "" {
		return nil
	}
	return []string{imageSpec.Image}
}

// extractComposeRefs extracts image references from a compose application.
// For image-based compose apps, the parent artifact image is included.
// For inline compose apps, service images are parsed from the compose YAML.
func extractComposeRefs(app *domain.ApplicationProviderSpec) []string {
	compose, err := (*app).AsComposeApplication()
	if err != nil {
		return nil
	}

	var refs []string

	// Try image-based first (parent artifact).
	imageSpec, err := compose.AsImageApplicationProviderSpec()
	if err == nil && imageSpec.Image != "" {
		refs = append(refs, imageSpec.Image)
		// For image-based compose, nested service images require pulling and
		// extracting the artifact — that is deferred to the worker.
		return refs
	}

	// Inline compose: parse service images from the compose YAML content.
	inline, err := compose.AsInlineApplicationProviderSpec()
	if err != nil {
		return refs
	}
	refs = append(refs, parseComposeServiceImages(inline.Inline)...)
	return refs
}

// extractQuadletRefs extracts image references from a quadlet application.
func extractQuadletRefs(app *domain.ApplicationProviderSpec) []string {
	quadlet, err := (*app).AsQuadletApplication()
	if err != nil {
		return nil
	}

	var refs []string

	// Try image-based first (parent artifact).
	imageSpec, err := quadlet.AsImageApplicationProviderSpec()
	if err == nil && imageSpec.Image != "" {
		refs = append(refs, imageSpec.Image)
		return refs
	}

	// Inline quadlet: parse Image= from quadlet unit files.
	inline, err := quadlet.AsInlineApplicationProviderSpec()
	if err != nil {
		return refs
	}
	refs = append(refs, parseQuadletImageRefs(inline.Inline)...)
	return refs
}

// extractHelmRefs extracts image references from a helm application.
// For now, only the chart image itself is included. Extracting images
// from helm template output requires running helm on the worker, which
// has security constraints (§4.5) and is deferred.
func extractHelmRefs(app *domain.ApplicationProviderSpec) []string {
	helm, err := (*app).AsHelmApplication()
	if err != nil {
		return nil
	}
	imageSpec, err := helm.AsImageApplicationProviderSpec()
	if err != nil || imageSpec.Image == "" {
		return nil
	}
	return []string{imageSpec.Image}
}

// extractVolumeImageRefs extracts OCI image references from application volumes.
func extractVolumeImageRefs(app *domain.ApplicationProviderSpec) []string {
	// Volumes are on the typed app specs. Try each type.
	var volumes *[]v1beta1.ApplicationVolume

	if c, err := (*app).AsContainerApplication(); err == nil {
		volumes = c.Volumes
	} else if comp, err := (*app).AsComposeApplication(); err == nil {
		volumes = comp.Volumes
	} else if q, err := (*app).AsQuadletApplication(); err == nil {
		volumes = q.Volumes
	}

	if volumes == nil {
		return nil
	}

	var refs []string
	for _, vol := range *volumes {
		imgVol, err := vol.AsImageVolumeProviderSpec()
		if err != nil {
			continue
		}
		if imgVol.Image.Reference != "" {
			refs = append(refs, imgVol.Image.Reference)
		}
	}
	return refs
}

// parseComposeServiceImages parses compose YAML content and returns all service
// image references.
func parseComposeServiceImages(contents []v1beta1.ApplicationContent) []string {
	spec, err := client.ParseComposeFromSpec(contents)
	if err != nil || spec == nil {
		return nil
	}
	var images []string
	for _, svc := range spec.Services {
		if svc.Image != "" {
			images = append(images, svc.Image)
		}
	}
	return images
}

// parseQuadletImageRefs parses quadlet unit files and returns all Image=
// references.
func parseQuadletImageRefs(contents []v1beta1.ApplicationContent) []string {
	quads, err := client.ParseQuadletReferencesFromSpec(contents)
	if err != nil {
		return nil
	}
	var images []string
	for _, quad := range quads {
		images = append(images, quadletImages(quad)...)
	}
	return images
}

// quadletImages returns all image references from a parsed quadlet.
func quadletImages(quad *common.QuadletReferences) []string {
	if quad == nil {
		return nil
	}
	var images []string
	if quad.Image != nil && *quad.Image != "" {
		images = append(images, *quad.Image)
	}
	for _, img := range quad.MountImages {
		if img != "" {
			images = append(images, img)
		}
	}
	return images
}

func deduplicateStrings(s []string) []string {
	if len(s) <= 1 {
		return s
	}
	seen := make(map[string]struct{}, len(s))
	out := make([]string, 0, len(s))
	for _, v := range s {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
