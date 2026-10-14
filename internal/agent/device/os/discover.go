package os

import (
	"context"
	"fmt"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
)

const ociDeltaSourceAnnotation = delta.SourceAnnotation

func (m *manager) discoverOSDelta(ctx context.Context, targetKey string, desired *v1beta1.DeviceSpec, bootcStatus *Status, optsFn dependency.ClientOptsFn) delta.Candidate {
	if desired.Os.DeltaImage != nil && *desired.Os.DeltaImage != "" {
		hint := *desired.Os.DeltaImage
		candidate, err := m.inspectOSDelta(ctx, hint, bootcStatus, optsFn)
		if err != nil {
			m.setFallbackReason(targetKey, fmt.Sprintf("delta hint is unusable: %v", err))
			return delta.Candidate{}
		}
		matches, err := m.osTargetPlatformDigestMatches(ctx, desired.Os.Image, candidate.TargetDigest, optsFn)
		if err != nil {
			m.setFallbackReason(targetKey, fmt.Sprintf("delta hint is unusable: inspect delta target digest: %v", err))
			return delta.Candidate{}
		}
		if !matches {
			m.setFallbackReason(targetKey, "delta hint is unusable: delta target digest does not match the desired OS image")
			return delta.Candidate{}
		}
		if !candidate.SourceAvailable {
			m.setFallbackReason(targetKey, "delta source image is unavailable in bootc storage")
			return delta.Candidate{}
		}
		return candidate
	}
	candidates := dependency.DiscoverDeltaReferrers(ctx, desired.Os.Image, m.skopeo, m.log, optsFn()...)
	if len(candidates) == 0 {
		return delta.Candidate{}
	}
	wanted := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		wanted[candidate.SourceDigest] = struct{}{}
	}
	sources, err := m.osDeltaSourceDigests(ctx, bootcStatus, wanted)
	if err != nil {
		m.log.Debugf("OS delta sources unavailable: %v", err)
	}
	for i := range candidates {
		_, candidates[i].SourceAvailable = sources[candidates[i].SourceDigest]
	}
	return delta.Select(candidates)
}

func (m *manager) inspectOSDelta(ctx context.Context, reference string, bootcStatus *Status, optsFn dependency.ClientOptsFn) (delta.Candidate, error) {
	candidate, err := dependency.InspectDeltaHint(ctx, reference, m.skopeo, optsFn()...)
	if err != nil {
		return delta.Candidate{}, err
	}
	candidate.SourceAvailable, err = m.osDeltaSourceAvailable(ctx, bootcStatus, candidate.SourceDigest)
	if err != nil {
		return delta.Candidate{}, fmt.Errorf("check delta source image: %w", err)
	}
	return candidate, nil
}
