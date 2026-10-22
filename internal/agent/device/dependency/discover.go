package dependency

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/pkg/log"
)

const criPlatformInspectionBudget = 30 * time.Second

// InspectDeltaHint validates an explicitly hinted delta manifest.
func InspectDeltaHint(
	ctx context.Context,
	reference string,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) (delta.Candidate, error) {
	manifest, err := inspectDeltaManifest(ctx, reference, skopeo, opts...)
	if err != nil {
		return delta.Candidate{}, err
	}
	return delta.FromHintManifest(reference, manifest)
}

// DiscoverDeltaReferrers returns valid delta candidates attached to the
// target's platform manifest. Discovery failures and unusable candidates are
// logged and skipped so callers can fall back to a full image pull.
func DiscoverDeltaReferrers(
	ctx context.Context,
	targetImage string,
	skopeo *client.Skopeo,
	logger *log.PrefixLogger,
	opts ...client.ClientOption,
) []delta.Candidate {
	platformReferrers, err := skopeo.ListPlatformReferrers(ctx, targetImage, opts...)
	if err != nil {
		logger.Infof("DISCOVERY_DELTA event=list-referrers-failed target=%s error=%v", targetImage, err)
		logger.Debugf("OCI delta referrers unavailable for %s: %v", targetImage, err)
		return nil
	}
	repository := delta.Repository(targetImage)
	if repository == "" {
		logger.Infof("DISCOVERY_DELTA event=invalid-target-repository target=%s", targetImage)
		return nil
	}
	logger.Infof("DISCOVERY_DELTA event=referrers-listed target=%s resolvedTargetDigest=%s descriptorCount=%d", targetImage,
		platformReferrers.TargetDigest, len(platformReferrers.Referrers.Manifests))
	candidates := make([]delta.Candidate, 0, len(platformReferrers.Referrers.Manifests))
	for _, referrer := range platformReferrers.Referrers.Manifests {
		logger.Infof("DISCOVERY_DELTA event=referrer target=%s digest=%s artifactType=%s size=%d", targetImage,
			referrer.Digest, referrer.ArtifactType, referrer.Size)
		if !delta.IsDeltaReferrer(referrer) {
			continue
		}
		candidate, err := inspectDeltaReferrer(ctx, repository+"@"+referrer.Digest, referrer.Size, skopeo, opts...)
		if err != nil {
			logger.Infof("DISCOVERY_DELTA event=referrer-rejected target=%s digest=%s error=%v", targetImage, referrer.Digest, err)
			logger.Debugf("Skipping unusable OCI delta %s: %v", referrer.Digest, err)
			continue
		}
		if candidate.TargetDigest != platformReferrers.TargetDigest {
			logger.Infof("DISCOVERY_DELTA event=target-digest-mismatch target=%s digest=%s candidateTargetDigest=%s resolvedTargetDigest=%s",
				targetImage, referrer.Digest, candidate.TargetDigest, platformReferrers.TargetDigest)
			logger.Debugf("Skipping OCI delta %s: target digest does not match the resolved platform digest", referrer.Digest)
			continue
		}
		logger.Infof("DISCOVERY_DELTA event=candidate-accepted target=%s reference=%s sourceDigest=%s targetDigest=%s downloadSize=%d",
			targetImage, candidate.Reference, candidate.SourceDigest, candidate.TargetDigest, candidate.DownloadSize)
		candidates = append(candidates, candidate)
	}
	logger.Infof("DISCOVERY_DELTA event=candidates-validated target=%s count=%d", targetImage, len(candidates))
	return candidates
}

func inspectDeltaReferrer(
	ctx context.Context,
	reference string,
	manifestSize int64,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) (delta.Candidate, error) {
	if manifestSize <= 0 {
		return delta.Candidate{}, fmt.Errorf("delta referrer manifest size is missing or invalid")
	}
	manifest, err := inspectDeltaManifest(ctx, reference, skopeo, opts...)
	if err != nil {
		return delta.Candidate{}, err
	}
	return delta.FromReferrerManifest(reference, manifest, manifestSize)
}

func inspectDeltaManifest(
	ctx context.Context,
	reference string,
	skopeo *client.Skopeo,
	opts ...client.ClientOption,
) (*client.OCIManifest, error) {
	if delta.Repository(reference) == "" {
		return nil, fmt.Errorf("invalid delta reference %q", reference)
	}
	manifest, err := skopeo.InspectManifest(ctx, reference, opts...)
	if err != nil {
		return nil, fmt.Errorf("inspect delta manifest: %w", err)
	}
	return manifest, nil
}

// discoverPodmanDelta validates hinted candidates or ranks locally usable referrers.
// The clients belong to the target's resolved storage owner.
func (m *prefetchManager) discoverPodmanDelta(ctx context.Context, targetImage, hint string, podman *client.Podman, skopeo *client.Skopeo, opts ...client.ClientOption) (delta.Candidate, error) {
	if skopeo == nil {
		if hint != "" {
			return delta.Candidate{}, fmt.Errorf("Skopeo client is unavailable for delta inspection")
		}
		return delta.Candidate{}, nil
	}
	targetValidator := applicationDeltaTargetValidator{image: targetImage, skopeo: skopeo, opts: opts}
	if hint != "" {
		candidate, err := InspectDeltaHint(ctx, hint, skopeo, opts...)
		if err != nil {
			return delta.Candidate{}, fmt.Errorf("delta hint is unusable: %w", err)
		}
		if err := targetValidator.validate(ctx, candidate); err != nil {
			return delta.Candidate{}, fmt.Errorf("delta hint is unusable: %w", err)
		}
		sources, err := m.podmanDeltaSourceDigests(ctx, podman)
		if err != nil {
			return delta.Candidate{}, fmt.Errorf("check delta source image: %w", err)
		}
		_, candidate.SourceAvailable = sources[candidate.SourceDigest]
		if !candidate.SourceAvailable {
			return delta.Candidate{}, fmt.Errorf("delta source image is unavailable in the owning Podman store")
		}
		return candidate, nil
	}
	candidates := DiscoverDeltaReferrers(ctx, targetImage, skopeo, m.log, opts...)
	if len(candidates) == 0 {
		return delta.Candidate{}, nil
	}
	sources, err := m.podmanDeltaSourceDigests(ctx, podman)
	if err != nil {
		m.log.Debugf("Could not inspect Podman images for delta sources: %v", err)
		return delta.Candidate{}, nil
	}
	for i := range candidates {
		_, candidates[i].SourceAvailable = sources[candidates[i].SourceDigest]
	}
	return delta.Select(candidates), nil
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
	sources := make(map[string]struct{})
	addSourceDigest := func(storedDigest string) {
		if source := applicationImageDigest(storedDigest); source != "" {
			sources[source] = struct{}{}
		}
	}
	digests, listErr := podman.ListImageDigests(ctx)
	if listErr == nil {
		for _, storedDigest := range digests {
			addSourceDigest(storedDigest)
		}
		return sources, nil
	}
	m.log.Debugf("Could not list Podman image digests: %v; falling back to individual inspections", listErr)

	images, err := podman.ListImages(ctx)
	if err != nil {
		return nil, errors.Join(listErr, err)
	}

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
			addSourceDigest(storedDigest)
		}
	}
	if len(images) > 0 && inspectFailures == len(images) {
		return nil, errors.Join(listErr, inspectErr)
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
		candidate, err := InspectDeltaHint(ctx, hint, skopeo, opts...)
		if err != nil {
			return delta.Candidate{}, "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		if err := targetValidator.validate(ctx, candidate); err != nil {
			return delta.Candidate{}, "", fmt.Errorf("delta hint is unusable: %w", err)
		}
		sourceRefs, err := m.criSourceReferences(ctx, cri, skopeo, []string{candidate.SourceDigest}, opts...)
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
	candidates := DiscoverDeltaReferrers(ctx, targetImage, skopeo, m.log, opts...)
	if len(candidates) == 0 {
		m.log.Infof("DISCOVERY_DELTA event=no-candidate target=%s", targetImage)
		return delta.Candidate{}, "", nil
	}
	m.log.Infof("DISCOVERY_DELTA event=checking-cri-sources target=%s candidateCount=%d", targetImage, len(candidates))
	sourceDigests := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		sourceDigests = append(sourceDigests, candidate.SourceDigest)
	}
	sourceRefs, err := m.criSourceReferences(ctx, cri, skopeo, sourceDigests, opts...)
	if err != nil {
		m.log.Infof("DISCOVERY_DELTA event=cri-source-inventory-failed target=%s error=%v", targetImage, err)
		m.log.Debugf("Could not list CRI images for delta sources: %v", err)
		return delta.Candidate{}, "", nil
	}
	for i := range candidates {
		sourceRef := sourceRefs[candidates[i].SourceDigest]
		candidates[i].SourceAvailable = sourceRef != ""
		m.log.Infof("DISCOVERY_DELTA event=source-availability target=%s reference=%s sourceDigest=%s available=%t runtimeReference=%s",
			targetImage, candidates[i].Reference, candidates[i].SourceDigest, candidates[i].SourceAvailable, sourceRef)
	}
	selected := delta.Select(candidates)
	selectedSourceRef := sourceRefs[selected.SourceDigest]
	if selected.Reference == "" {
		m.log.Infof("DISCOVERY_DELTA event=no-usable-candidate target=%s", targetImage)
	} else {
		m.log.Infof("DISCOVERY_DELTA event=selected target=%s reference=%s sourceDigest=%s targetDigest=%s downloadSize=%d runtimeReference=%s",
			targetImage, selected.Reference, selected.SourceDigest, selected.TargetDigest, selected.DownloadSize, selectedSourceRef)
	}
	return selected, selectedSourceRef, nil
}

func (m *prefetchManager) criSourceReferences(ctx context.Context, cri *client.CRI, skopeo *client.Skopeo, sourceDigests []string, opts ...client.ClientOption) (map[string]string, error) {
	wanted := make(map[string]struct{}, len(sourceDigests))
	for _, sourceDigest := range sourceDigests {
		if sourceDigest != "" {
			wanted[sourceDigest] = struct{}{}
		}
	}
	sourceRefs := make(map[string]string, len(wanted))
	if len(wanted) == 0 {
		return sourceRefs, nil
	}
	repositoryRefs, err := cri.ImageReferences(ctx, opts...)
	if err != nil {
		return nil, err
	}
	for repositoryDigest, reference := range repositoryRefs {
		if _, ok := wanted[repositoryDigest]; ok {
			sourceRefs[repositoryDigest] = reference
		}
	}
	if len(sourceRefs) == len(wanted) || len(sourceRefs) == len(repositoryRefs) {
		return sourceRefs, nil
	}
	runtimeInfo, runtimeErr := cri.RuntimeInfo(ctx, opts...)
	if runtimeErr != nil {
		m.log.Debugf("Could not identify the CRI runtime for delta sources: %v", runtimeErr)
	}
	remoteCtx, cancel := context.WithTimeout(ctx, m.criRemoteInspectionBudget())
	defer cancel()
	repositoryDigests := make([]string, 0, len(repositoryRefs))
	for repositoryDigest := range repositoryRefs {
		repositoryDigests = append(repositoryDigests, repositoryDigest)
	}
	sort.Strings(repositoryDigests)
	for _, repositoryDigest := range repositoryDigests {
		repositoryRef := repositoryRefs[repositoryDigest]
		if _, ok := wanted[repositoryDigest]; ok {
			continue
		}
		platformDigest := ""
		if runtimeErr == nil && runtimeInfo.Name == client.CRIRuntimeContainerd {
			localDigest, err := cri.ContainerdImagePlatformDigest(ctx, runtimeInfo.Endpoint, repositoryDigest, opts...)
			if err == nil {
				platformDigest = localDigest
			} else {
				m.log.Debugf("Could not inspect local containerd image %q for delta sources: %v", repositoryRef, err)
			}
		}
		if platformDigest == "" {
			if remoteCtx.Err() != nil {
				m.log.Debugf("Skipped remote inspection of CRI image %q for delta sources: %v", repositoryRef, remoteCtx.Err())
				continue
			}
			remoteDigest, err := skopeo.InspectPlatformDigest(remoteCtx, repositoryRef, opts...)
			if err != nil {
				m.log.Debugf("Could not inspect CRI image %q for delta sources: %v", repositoryRef, err)
				continue
			}
			platformDigest = remoteDigest
		}
		if _, ok := wanted[platformDigest]; ok {
			if selected := sourceRefs[platformDigest]; selected == "" || repositoryRef < selected {
				sourceRefs[platformDigest] = repositoryRef
			}
			if len(sourceRefs) == len(wanted) {
				return sourceRefs, nil
			}
		}
	}
	return sourceRefs, nil
}

func (m *prefetchManager) criRemoteInspectionBudget() time.Duration {
	if m.pullTimeout > 0 && m.pullTimeout < criPlatformInspectionBudget {
		return m.pullTimeout
	}
	return criPlatformInspectionBudget
}
