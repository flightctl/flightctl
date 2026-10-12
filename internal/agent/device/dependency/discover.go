package dependency

import (
	"context"
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
)

// discoverPodmanDelta validates hinted candidates or ranks locally usable referrers.
// The clients belong to the target's resolved storage owner.
func (m *prefetchManager) discoverPodmanDelta(ctx context.Context, targetImage, hint string, podman *client.Podman, skopeo *client.Skopeo, opts ...client.ClientOption) (string, error) {
	if skopeo == nil {
		if hint != "" {
			return "", fmt.Errorf("Skopeo client is unavailable for delta inspection")
		}
		return "", nil
	}
	targetValidator := applicationDeltaTargetValidator{image: targetImage, skopeo: skopeo, opts: opts}
	if hint != "" {
		candidate, err := inspectApplicationDelta(ctx, hint, 0, skopeo, opts...)
		if err != nil {
			return "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		if err := targetValidator.validate(ctx, candidate); err != nil {
			return "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		sources, err := m.podmanDeltaSourceDigests(ctx, podman)
		if err != nil {
			return "", fmt.Errorf("check delta source image: %w", err)
		}
		_, candidate.SourceAvailable = sources[candidate.SourceDigest]
		if !candidate.SourceAvailable {
			return "", fmt.Errorf("delta source image is unavailable in the owning Podman store")
		}
		return candidate.Reference, nil
	}
	platformReferrers, err := skopeo.ListPlatformReferrers(ctx, targetImage, opts...)
	if err != nil {
		m.log.Debugf("application delta referrers unavailable for %s: %v", targetImage, err)
		return "", nil
	}
	index := platformReferrers.Referrers
	repo := imageRepository(targetImage)
	if repo == "" {
		return "", nil
	}
	candidates := make([]delta.Candidate, 0, len(index.Manifests))
	for _, ref := range index.Manifests {
		if !delta.IsDeltaReferrer(ref) {
			continue
		}
		candidate, err := inspectApplicationDelta(ctx, repo+"@"+ref.Digest, ref.Size, skopeo, opts...)
		if err != nil {
			m.log.Debugf("Skipping unusable application delta %s: %v", ref.Digest, err)
			continue
		}
		if candidate.TargetDigest != platformReferrers.TargetDigest {
			m.log.Debugf("Skipping application delta %s: target digest does not match the resolved platform digest", ref.Digest)
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return "", nil
	}
	sources, err := m.podmanDeltaSourceDigests(ctx, podman)
	if err != nil {
		m.log.Debugf("Could not inspect Podman images for delta sources: %v", err)
		return "", nil
	}
	for i := range candidates {
		_, candidates[i].SourceAvailable = sources[candidates[i].SourceDigest]
	}
	return delta.Select(candidates), nil
}

func inspectApplicationDelta(ctx context.Context, reference string, manifestSize int64, skopeo *client.Skopeo, opts ...client.ClientOption) (delta.Candidate, error) {
	if err := validateApplicationDeltaCandidate(reference); err != nil {
		return delta.Candidate{}, err
	}
	manifest, err := skopeo.InspectManifest(ctx, reference, opts...)
	if err != nil {
		return delta.Candidate{}, fmt.Errorf("inspect delta manifest: %w", err)
	}
	return delta.FromManifest(reference, manifest, manifestSize)
}

type applicationDeltaTargetValidator struct {
	image     string
	skopeo    *client.Skopeo
	opts      []client.ClientOption
	inspected bool
	digest    string
	err       error
}

func (v *applicationDeltaTargetValidator) validate(ctx context.Context, candidate delta.Candidate) error {
	if candidate.TargetDigest == "" {
		return nil
	}
	if !v.inspected {
		v.digest, v.err = v.skopeo.InspectPlatformDigest(ctx, v.image, v.opts...)
		v.inspected = true
	}
	if v.err != nil {
		return fmt.Errorf("inspect desired application image digest: %w", v.err)
	}
	if candidate.TargetDigest != v.digest {
		return fmt.Errorf("delta target digest does not match the desired application image")
	}
	return nil
}

// A registry manifest digest is not a Podman image ID. Inspect stored images to
// match their content or repository digests, including sources in other repositories.
func (m *prefetchManager) podmanDeltaSourceDigests(ctx context.Context, podman *client.Podman) (map[string]struct{}, error) {
	images, err := podman.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	sources := make(map[string]struct{})
	var inspectErr error
	inspectFailures := 0
	for _, image := range images {
		digests, err := podman.ImageDigests(ctx, image)
		if err != nil {
			inspectFailures++
			m.log.Debugf("Could not inspect Podman image %q for delta sources: %v", image, err)
			inspectErr = errors.Join(inspectErr, err)
			continue
		}
		for _, storedDigest := range digests {
			if source := applicationImageDigest(storedDigest); source != "" {
				sources[source] = struct{}{}
			}
		}
	}
	if len(images) > 0 && inspectFailures == len(images) {
		return nil, inspectErr
	}
	return sources, nil
}

// discoverCRIDelta checks sources in the configured runtime, independently of
// application metadata or images present in Podman storage.
func (m *prefetchManager) discoverCRIDelta(ctx context.Context, targetImage, hint string, cri *client.CRI, skopeo *client.Skopeo, opts ...client.ClientOption) (delta.Candidate, string, error) {
	if skopeo == nil {
		if hint != "" {
			return delta.Candidate{}, "", fmt.Errorf("Skopeo client is unavailable for delta inspection")
		}
		return delta.Candidate{}, "", nil
	}
	targetValidator := applicationDeltaTargetValidator{image: targetImage, skopeo: skopeo, opts: opts}
	if hint != "" {
		candidate, err := inspectApplicationDelta(ctx, hint, 0, skopeo, opts...)
		if err != nil {
			return delta.Candidate{}, "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		if err := targetValidator.validate(ctx, candidate); err != nil {
			return delta.Candidate{}, "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		sourceRefs, err := cri.ImageReferencesByDigest(ctx, []string{candidate.SourceDigest}, opts...)
		if err != nil {
			return delta.Candidate{}, "", fmt.Errorf("check delta source image: %w", err)
		}
		sourceRef := sourceRefs[candidate.SourceDigest]
		candidate.SourceAvailable = sourceRef != ""
		if !candidate.SourceAvailable {
			return delta.Candidate{}, "", fmt.Errorf("delta source image is unavailable in the owning CRI store")
		}
		return candidate, sourceRef, nil
	}
	platformReferrers, err := skopeo.ListPlatformReferrers(ctx, targetImage, opts...)
	if err != nil {
		m.log.Debugf("application delta referrers unavailable for %s: %v", targetImage, err)
		return delta.Candidate{}, "", nil
	}
	index := platformReferrers.Referrers
	repo := imageRepository(targetImage)
	if repo == "" {
		return delta.Candidate{}, "", nil
	}
	candidates := make([]delta.Candidate, 0, len(index.Manifests))
	for _, ref := range index.Manifests {
		if !delta.IsDeltaReferrer(ref) {
			continue
		}
		candidate, err := inspectApplicationDelta(ctx, repo+"@"+ref.Digest, ref.Size, skopeo, opts...)
		if err != nil {
			m.log.Debugf("Skipping unusable application delta %s: %v", ref.Digest, err)
			continue
		}
		if candidate.TargetDigest != platformReferrers.TargetDigest {
			m.log.Debugf("Skipping application delta %s: target digest does not match the resolved platform digest", ref.Digest)
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return delta.Candidate{}, "", nil
	}
	sourceDigests := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		sourceDigests = append(sourceDigests, candidate.SourceDigest)
	}
	sourceRefs, err := cri.ImageReferencesByDigest(ctx, sourceDigests, opts...)
	if err != nil {
		m.log.Debugf("Could not list CRI images for delta sources: %v", err)
		return delta.Candidate{}, "", nil
	}
	for i := range candidates {
		sourceRef := sourceRefs[candidates[i].SourceDigest]
		candidates[i].SourceAvailable = sourceRef != ""
	}
	selected := delta.Select(candidates)
	for _, candidate := range candidates {
		if candidate.Reference == selected {
			return candidate, sourceRefs[candidate.SourceDigest], nil
		}
	}
	return delta.Candidate{}, "", nil
}
