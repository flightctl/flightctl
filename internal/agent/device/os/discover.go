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
		candidate, err := m.inspectOSDelta(ctx, hint, desired.Os.Image, 0, bootcStatus, optsFn)
		if err != nil {
			m.setFallbackReason(fmt.Sprintf("delta hint is unusable: %v", err))
			return ""
		}
		if !candidate.SourceAvailable {
			m.setFallbackReason("delta source image is unavailable in bootc storage")
			return ""
		}
		return candidate.Reference
	}
	index, err := m.skopeo.ListReferrers(ctx, desired.Os.Image, optsFn()...)
	if err != nil {
		m.log.Debugf("OS delta referrers unavailable: %v", err)
		return ""
	}
	repo := imageRepository(desired.Os.Image)
	if repo == "" {
		return ""
	}
	candidates := make([]delta.Candidate, 0, len(index.Manifests))
	for _, ref := range index.Manifests {
		if !delta.IsDeltaReferrer(ref) {
			continue
		}
		candidate, err := m.inspectOSDelta(ctx, repo+"@"+ref.Digest, desired.Os.Image, ref.Size, bootcStatus, optsFn)
		if err != nil {
			m.log.Debugf("Skipping unusable OS delta %s: %v", ref.Digest, err)
			continue
		}
		candidates = append(candidates, candidate)
	}
	return delta.Select(candidates)
}

func (m *manager) inspectOSDelta(ctx context.Context, reference, targetImage string, manifestSize int64, bootcStatus *Status, optsFn dependency.ClientOptsFn) (delta.Candidate, error) {
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
	if target := manifest.Annotations["io.github.containers.delta.target"]; target != "" &&
		!m.osTargetDigestMatches(ctx, targetImage, []string{target}, optsFn) {
		return delta.Candidate{}, fmt.Errorf("delta target digest does not match the desired OS image")
	}
	candidate.SourceAvailable, err = m.osDeltaSourceAvailable(ctx, bootcStatus, candidate.SourceDigest)
	if err != nil {
		return delta.Candidate{}, fmt.Errorf("check delta source image: %w", err)
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
