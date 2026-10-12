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
	if hint != "" {
		candidate, err := inspectPodmanDelta(ctx, hint, 0, podman, skopeo, opts...)
		if err != nil {
			return "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		if !candidate.SourceAvailable {
			return "", fmt.Errorf("delta source image is unavailable in the owning Podman store")
		}
		return candidate.Reference, nil
	}
	index, err := skopeo.ListReferrers(ctx, targetImage, opts...)
	if err != nil {
		m.log.Debugf("application delta referrers unavailable for %s: %v", targetImage, err)
		return "", nil
	}
	repo := imageRepository(targetImage)
	if repo == "" {
		return "", nil
	}
	candidates := make([]delta.Candidate, 0, len(index.Manifests))
	for _, ref := range index.Manifests {
		if !delta.IsDeltaReferrer(ref) {
			continue
		}
		candidate, err := inspectPodmanDelta(ctx, repo+"@"+ref.Digest, ref.Size, podman, skopeo, opts...)
		if err != nil {
			m.log.Debugf("Skipping unusable application delta %s: %v", ref.Digest, err)
			continue
		}
		candidates = append(candidates, candidate)
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

func inspectPodmanDelta(ctx context.Context, reference string, manifestSize int64, podman *client.Podman, skopeo *client.Skopeo, opts ...client.ClientOption) (delta.Candidate, error) {
	candidate, err := inspectApplicationDelta(ctx, reference, manifestSize, skopeo, opts...)
	if err != nil {
		return delta.Candidate{}, err
	}
	candidate.SourceAvailable, err = podmanDeltaSourceAvailable(ctx, podman, candidate.SourceDigest)
	if err != nil {
		return delta.Candidate{}, fmt.Errorf("check delta source image: %w", err)
	}
	return candidate, nil
}

// A registry manifest digest is not a Podman image ID. Inspect stored images to
// match their content or repository digests, including sources in other repositories.
func podmanDeltaSourceAvailable(ctx context.Context, podman *client.Podman, source string) (bool, error) {
	images, err := podman.ListImages(ctx)
	if err != nil {
		return false, err
	}
	var inspectErr error
	for _, image := range images {
		digests, err := podman.ImageDigests(ctx, image)
		if err != nil {
			inspectErr = errors.Join(inspectErr, err)
			continue
		}
		for _, storedDigest := range digests {
			if applicationImageDigest(storedDigest) == source {
				return true, nil
			}
		}
	}
	return false, inspectErr
}

func inspectCRIDelta(ctx context.Context, reference string, manifestSize int64, cri *client.CRI, skopeo *client.Skopeo, opts ...client.ClientOption) (delta.Candidate, string, error) {
	candidate, err := inspectApplicationDelta(ctx, reference, manifestSize, skopeo, opts...)
	if err != nil {
		return delta.Candidate{}, "", err
	}
	sourceRef, err := cri.ImageReferenceByDigest(ctx, candidate.SourceDigest, opts...)
	if err != nil {
		return delta.Candidate{}, "", fmt.Errorf("check delta source image: %w", err)
	}
	candidate.SourceAvailable = sourceRef != ""
	return candidate, sourceRef, nil
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
	if hint != "" {
		candidate, sourceRef, err := inspectCRIDelta(ctx, hint, 0, cri, skopeo, opts...)
		if err != nil {
			return delta.Candidate{}, "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		if !candidate.SourceAvailable {
			return delta.Candidate{}, "", fmt.Errorf("delta source image is unavailable in the owning CRI store")
		}
		return candidate, sourceRef, nil
	}
	index, err := skopeo.ListReferrers(ctx, targetImage, opts...)
	if err != nil {
		m.log.Debugf("application delta referrers unavailable for %s: %v", targetImage, err)
		return delta.Candidate{}, "", nil
	}
	repo := imageRepository(targetImage)
	if repo == "" {
		return delta.Candidate{}, "", nil
	}
	candidates := make([]delta.Candidate, 0, len(index.Manifests))
	sources := make(map[string]string)
	for _, ref := range index.Manifests {
		if !delta.IsDeltaReferrer(ref) {
			continue
		}
		candidate, sourceRef, err := inspectCRIDelta(ctx, repo+"@"+ref.Digest, ref.Size, cri, skopeo, opts...)
		if err != nil {
			m.log.Debugf("Skipping unusable application delta %s: %v", ref.Digest, err)
			continue
		}
		candidates = append(candidates, candidate)
		sources[candidate.Reference] = sourceRef
	}
	selected := delta.Select(candidates)
	for _, candidate := range candidates {
		if candidate.Reference == selected {
			return candidate, sources[selected], nil
		}
	}
	return delta.Candidate{}, "", nil
}
