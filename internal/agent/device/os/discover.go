package os

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/util/validation"
)

const (
	ociDeltaArtifactType     = delta.ArtifactType
	ociDeltaSourceAnnotation = delta.SourceAnnotation
)

func (m *manager) discoverOSDelta(ctx context.Context, desired *v1beta1.DeviceSpec, bootcStatus *Status, optsFn dependency.ClientOptsFn) string {
	if desired.Os.DeltaImage != nil && *desired.Os.DeltaImage != "" {
		hint := *desired.Os.DeltaImage
		candidate, err := m.inspectOSDelta(ctx, hint, 0, bootcStatus, optsFn)
		if err != nil {
			m.setFallbackReason(fmt.Sprintf("delta hint is unusable: %v", err))
			return ""
		}
		matches, err := m.osTargetPlatformDigestMatches(ctx, desired.Os.Image, candidate.TargetDigest, optsFn)
		if err != nil {
			m.setFallbackReason(fmt.Sprintf("delta hint is unusable: inspect delta target digest: %v", err))
			return ""
		}
		if !matches {
			m.setFallbackReason("delta hint is unusable: delta target digest does not match the desired OS image")
			return ""
		}
		if !candidate.SourceAvailable {
			m.setFallbackReason("delta source image is unavailable in bootc storage")
			return ""
		}
		return candidate.Reference
	}
	platformReferrers, err := m.skopeo.ListPlatformReferrers(ctx, desired.Os.Image, optsFn()...)
	if err != nil {
		m.log.Debugf("OS delta referrers unavailable: %v", err)
		return ""
	}
	index := platformReferrers.Referrers
	repo := imageRepository(desired.Os.Image)
	if repo == "" {
		return ""
	}
	candidates := make([]delta.Candidate, 0, len(index.Manifests))
	for _, ref := range index.Manifests {
		if !delta.IsDeltaReferrer(ref) {
			continue
		}
		candidate, err := m.inspectOSDeltaManifest(ctx, repo+"@"+ref.Digest, ref.Size, optsFn)
		if err != nil {
			m.log.Debugf("Skipping unusable OS delta %s: %v", ref.Digest, err)
			continue
		}
		if candidate.TargetDigest != platformReferrers.TargetDigest {
			m.log.Debugf("Skipping OS delta %s: target digest does not match the resolved platform digest", ref.Digest)
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return ""
	}
	wanted := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		wanted[candidate.SourceDigest] = struct{}{}
	}
	sources, err := m.osDeltaSourceDigests(ctx, bootcStatus, wanted)
	if err != nil {
		m.log.Debugf("OS delta sources unavailable: %v", err)
		return ""
	}
	for i := range candidates {
		_, candidates[i].SourceAvailable = sources[candidates[i].SourceDigest]
	}
	return delta.Select(candidates)
}

func (m *manager) inspectOSDelta(ctx context.Context, reference string, manifestSize int64, bootcStatus *Status, optsFn dependency.ClientOptsFn) (delta.Candidate, error) {
	candidate, err := m.inspectOSDeltaManifest(ctx, reference, manifestSize, optsFn)
	if err != nil {
		return delta.Candidate{}, err
	}
	candidate.SourceAvailable, err = m.osDeltaSourceAvailable(ctx, bootcStatus, candidate.SourceDigest)
	if err != nil {
		return delta.Candidate{}, fmt.Errorf("check delta source image: %w", err)
	}
	return candidate, nil
}

func (m *manager) inspectOSDeltaManifest(ctx context.Context, reference string, manifestSize int64, optsFn dependency.ClientOptsFn) (delta.Candidate, error) {
	if imageRepository(reference) == "" {
		return delta.Candidate{}, fmt.Errorf("invalid delta reference %q", reference)
	}
	manifest, err := m.skopeo.InspectManifest(ctx, reference, optsFn()...)
	if err != nil {
		return delta.Candidate{}, fmt.Errorf("inspect delta manifest: %w", err)
	}
	candidate, err := delta.FromManifest(reference, manifest, manifestSize)
	if err != nil {
		return delta.Candidate{}, err
	}
	return candidate, nil
}

func imageRepository(image string) string {
	matches := validation.OciImageReferenceRegexp.FindStringSubmatch(image)
	if len(matches) == 0 {
		return ""
	}
	return matches[1]
}
