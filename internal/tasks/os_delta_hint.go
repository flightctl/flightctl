package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appspec"
	"github.com/flightctl/flightctl/internal/chartutil"
	"github.com/flightctl/flightctl/internal/config"
	deltamodel "github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/oci"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type generationLookup interface {
	GetDeltaGeneration(ctx context.Context, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*deltamodel.DeltaGeneration, error)
}

const (
	deltaGenerationMissingMemoTTL = time.Minute
	helmDeltaHintRenderTimeout    = 15 * time.Second
	helmDeltaHintRenderReserve    = 30 * time.Second
)

func helmDeltaHintRenderBudget(remaining time.Duration, hasDeadline bool) (time.Duration, bool) {
	if !hasDeadline {
		return helmDeltaHintRenderTimeout, true
	}
	if remaining <= helmDeltaHintRenderReserve {
		return 0, false
	}

	timeout := min(helmDeltaHintRenderTimeout, remaining-helmDeltaHintRenderReserve)
	return timeout, timeout > 0
}

type deltaGenerationLookupMemo struct {
	Missing           bool    `json:"missing,omitempty"`
	Status            string  `json:"status,omitempty"`
	DeltaRef          *string `json:"deltaRef,omitempty"`
	SizeBytes         *int64  `json:"sizeBytes,omitempty"`
	ExpiresAtUnixNano int64   `json:"expiresAtUnixNano,omitempty"`
}

func FormatIECBytes(n int64) string {
	if n <= 0 {
		return "0 KiB"
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	val := float64(n) / 1024
	unit := 0
	for unit < len(units)-1 && val >= 1024 {
		val /= 1024
		unit++
	}
	// Round to one decimal place, dropping a unit when the value would
	// otherwise round below 0.1 of that unit.
	rounded := math.Round(val*10) / 10
	if rounded < 0.1 && unit > 0 {
		val *= 1024
		unit--
		rounded = math.Round(val*10) / 10
	}
	if rounded < 1 && unit == 0 {
		return "1 KiB"
	}
	if rounded == math.Trunc(rounded) {
		return fmt.Sprintf("%d %s", int64(rounded), units[unit])
	}
	return fmt.Sprintf("%.1f %s", rounded, units[unit])
}

func ImageRepositoryFromRef(imageRef string) (string, error) {
	named, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return "", err
	}
	return named.Name(), nil
}

func deltaWriteSpec(cfg *config.Config) *domain.OciRepoSpec {
	if cfg == nil || cfg.DeltaGeneration == nil || cfg.DeltaGeneration.DefaultRepository == nil {
		return nil
	}
	defaultRepository, err := cfg.DeltaGeneration.DefaultRepository.OciRepoSpec()
	if err != nil {
		return nil
	}
	return defaultRepository
}

func (t *DeviceRenderLogic) resolveImageDigestPair(
	ctx context.Context,
	orgId uuid.UUID,
	imageRef string,
	sourceDigest string,
	fallbackPlatform *ocispec.Platform,
) (oci.ImageDigestPair, error) {
	spec := deltaWriteSpec(t.cfg)
	return oci.CachedImageDigestPair(
		ctx, t.log, t.kvStore, orgId, imageRef, sourceDigest, fallbackPlatform, t.cfg.EffectiveImageDigestCacheTTL(),
		func(ctx context.Context) (string, error) {
			return oci.InspectImageDigest(ctx, imageRef, spec)
		},
		func(ctx context.Context, resolvedImage string) (oci.ImageDigestPair, error) {
			return oci.InspectImageDigestPair(ctx, resolvedImage, sourceDigest, spec, fallbackPlatform)
		},
	)
}

func hintFromGeneration(gen *deltamodel.DeltaGeneration) (deltaImage *string, deltaSizeIEC *string) {
	if gen == nil || gen.Status != deltamodel.DeltaGenerationSucceeded || gen.DeltaRef == nil || *gen.DeltaRef == "" {
		return nil, nil
	}
	deltaImage = gen.DeltaRef
	if gen.SizeBytes != nil && *gen.SizeBytes >= 0 {
		deltaSizeIEC = lo.ToPtr(FormatIECBytes(*gen.SizeBytes))
	}
	return deltaImage, deltaSizeIEC
}

func (t *deviceRenderState) resolveOSDeltaHint(ctx context.Context, device *domain.Device, rendered RenderedSpec) *deviceservice.RenderedOSHints {
	if rendered.OsImage == "" {
		return nil
	}
	repo, err := ImageRepositoryFromRef(rendered.OsImage)
	if err != nil {
		return nil
	}
	src := ""
	if device != nil && device.Status != nil {
		src = device.Status.Os.ImageDigest
	}
	if src == "" {
		t.log.Infof("os delta hint skipped device=%s/%s reason=empty-source-digest osImage=%q repo=%s",
			t.orgId, t.event.InvolvedObject.Name, rendered.OsImage, repo)
		return nil
	}
	pair, err := t.resolveImageDigestPair(ctx, t.orgId, rendered.OsImage, src, oci.DeviceImagePlatform(device))
	if err != nil {
		t.log.Infof("os delta hint skipped device=%s/%s reason=inspect-source-target-platform-digests osImage=%q err=%v",
			t.orgId, t.event.InvolvedObject.Name, rendered.OsImage, err)
		return nil
	}
	src = pair.SourceDigest
	tgt := pair.TargetDigest
	if src == "" || tgt == "" {
		t.log.Infof("os delta hint skipped device=%s/%s reason=empty-target-digest osImage=%q repo=%s",
			t.orgId, t.event.InvolvedObject.Name, rendered.OsImage, repo)
		return nil
	}
	key := deltastore.GenerationKey{
		OrgID:           t.orgId,
		ImageRepository: repo,
		SourceDigest:    src,
		TargetDigest:    tgt,
	}
	t.log.Infof("os delta hint query device=%s/%s repo=%s sourceDigest=%s targetDigest=%s osImage=%s",
		t.orgId, t.event.InvolvedObject.Name, repo, src, tgt, rendered.OsImage)
	gen, err := lookupOSDeltaGeneration(ctx, t.kvStore, t.deltaLookup, key, deltastore.WithStatus(deltamodel.DeltaGenerationSucceeded))
	if err != nil {
		t.log.Warnf("os delta hint lookup failed device=%s/%s repo=%s sourceDigest=%s targetDigest=%s: %v",
			t.orgId, t.event.InvolvedObject.Name, repo, src, tgt, err)
		return nil
	}
	img, deltaSize := hintFromGeneration(gen)
	if gen == nil {
		t.log.Infof("os delta hint miss device=%s/%s repo=%s sourceDigest=%s targetDigest=%s",
			t.orgId, t.event.InvolvedObject.Name, repo, src, tgt)
	} else {
		deltaRef := ""
		if gen.DeltaRef != nil {
			deltaRef = *gen.DeltaRef
		}
		t.log.Infof("os delta hint hit device=%s/%s repo=%s sourceDigest=%s targetDigest=%s status=%s deltaRef=%s sizeBytes=%v",
			t.orgId, t.event.InvolvedObject.Name, repo, src, tgt, gen.Status, deltaRef, gen.SizeBytes)
	}
	if img == nil {
		return nil
	}
	return &deviceservice.RenderedOSHints{DeltaImage: img, DeltaSize: deltaSize}
}

func deltaGenerationHintKey(key deltastore.GenerationKey) string {
	return (&kvstore.DeltaGenerationHintKey{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
	}).ComposeKey()
}

func deltaGenerationLookupMemoKey(key deltastore.GenerationKey) string {
	return fmt.Sprintf("deltaHintMemo/%s/%s/%s/%s/", key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest)
}

func lookupOSDeltaGeneration(ctx context.Context, kv kvstore.KVStore, store generationLookup, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*deltamodel.DeltaGeneration, error) {
	// Check the delta-worker-owned success hint before the device-render memo,
	// so a published success takes precedence over a stale cached database miss.
	if gen, found := lookupOSDeltaGenerationMemo(ctx, kv, key); found {
		return gen, nil
	}

	gen, err := store.GetDeltaGeneration(ctx, key, opts...)
	if err != nil && !errors.Is(err, flterrors.ErrResourceNotFound) {
		return nil, err
	}
	if errors.Is(err, flterrors.ErrResourceNotFound) {
		// Recheck the worker-owned key after the DB read. A generation may have
		// succeeded and been cached while this lookup was in flight.
		cached, found, cacheErr := lookupWorkerGenerationHint(ctx, kv, key)
		if found {
			return cached, nil
		}
		if cacheErr != nil {
			return nil, cacheErr
		}

		writeMissingGenerationMemo(ctx, kv, key)
		// Prefer a newly published worker hint for this render too.
		cached, found, _ = lookupWorkerGenerationHint(ctx, kv, key)
		if found {
			return cached, nil
		}
		return nil, nil
	}
	if gen != nil && gen.Status == deltamodel.DeltaGenerationSucceeded && gen.DeltaRef != nil && *gen.DeltaRef != "" {
		writeSuccessfulGenerationMemo(ctx, kv, key, gen)
	}
	return gen, nil
}

func lookupOSDeltaGenerationMemo(ctx context.Context, kv kvstore.KVStore, key deltastore.GenerationKey) (*deltamodel.DeltaGeneration, bool) {
	gen, found, err := lookupWorkerGenerationHint(ctx, kv, key)
	if err != nil {
		return nil, false
	}
	if found {
		return gen, true
	}

	gen, missing, found := lookupGenerationMemo(ctx, kv, key)
	if !found {
		return nil, false
	}
	if !missing {
		return gen, true
	}

	// The delta worker may have populated its key while the memo was read.
	gen, found, err = lookupWorkerGenerationHint(ctx, kv, key)
	if err != nil {
		return nil, false
	}
	if found {
		return gen, true
	}
	return nil, true
}

func lookupWorkerGenerationHint(ctx context.Context, kv kvstore.KVStore, key deltastore.GenerationKey) (*deltamodel.DeltaGeneration, bool, error) {
	cacheKey := deltaGenerationHintKey(key)
	raw, err := kv.Get(ctx, cacheKey)
	if err != nil || len(raw) == 0 {
		return nil, false, err
	}
	var hint kvstore.DeltaGenerationHint
	if err := json.Unmarshal(raw, &hint); err != nil || hint.DeltaRef == "" {
		return nil, false, nil
	}
	return &deltamodel.DeltaGeneration{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
		Status:          deltamodel.DeltaGenerationSucceeded,
		DeltaRef:        lo.ToPtr(hint.DeltaRef),
		SizeBytes:       hint.SizeBytes,
	}, true, nil
}

func lookupGenerationMemo(ctx context.Context, kv kvstore.KVStore, key deltastore.GenerationKey) (*deltamodel.DeltaGeneration, bool, bool) {
	raw, err := kv.Get(ctx, deltaGenerationLookupMemoKey(key))
	if err != nil || len(raw) == 0 {
		return nil, false, false
	}
	var memo deltaGenerationLookupMemo
	if err := json.Unmarshal(raw, &memo); err != nil {
		return nil, false, false
	}
	if memo.Missing {
		if memo.ExpiresAtUnixNano == 0 || time.Now().UnixNano() >= memo.ExpiresAtUnixNano {
			return nil, false, false
		}
		return nil, true, true
	}
	if memo.Status != deltamodel.DeltaGenerationSucceeded || memo.DeltaRef == nil || *memo.DeltaRef == "" {
		return nil, false, false
	}
	return &deltamodel.DeltaGeneration{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
		Status:          memo.Status,
		DeltaRef:        memo.DeltaRef,
		SizeBytes:       memo.SizeBytes,
	}, false, true
}

func writeSuccessfulGenerationMemo(ctx context.Context, kv kvstore.KVStore, key deltastore.GenerationKey, gen *deltamodel.DeltaGeneration) {
	raw, err := json.Marshal(deltaGenerationLookupMemo{
		Status:    gen.Status,
		DeltaRef:  gen.DeltaRef,
		SizeBytes: gen.SizeBytes,
	})
	if err != nil {
		logrus.StandardLogger().WithError(err).Warn("failed marshaling successful delta generation lookup memo")
		return
	}
	if err := kv.Set(ctx, deltaGenerationLookupMemoKey(key), raw, kvstore.DeltaGenerationHintTTL); err != nil {
		logrus.StandardLogger().WithError(err).Warn("failed caching successful delta generation lookup memo")
	}
}

func writeMissingGenerationMemo(ctx context.Context, kv kvstore.KVStore, key deltastore.GenerationKey) {
	raw, err := json.Marshal(deltaGenerationLookupMemo{
		Missing:           true,
		ExpiresAtUnixNano: time.Now().Add(deltaGenerationMissingMemoTTL).UnixNano(),
	})
	if err != nil {
		logrus.StandardLogger().WithError(err).Warn("failed marshaling missing delta generation lookup memo")
		return
	}
	memoKey := deltaGenerationLookupMemoKey(key)
	added, err := kv.SetNX(ctx, memoKey, raw)
	if err != nil {
		logrus.StandardLogger().WithError(err).Warn("failed caching missing delta generation lookup memo")
		return
	}
	if !added {
		return
	}
	if err := kv.SetExpire(ctx, memoKey, deltaGenerationMissingMemoTTL); err != nil {
		// ExpiresAtUnixNano keeps a failed EXPIRE from turning this into a
		// permanent negative memo.
		logrus.StandardLogger().WithError(err).Warn("failed setting missing delta generation lookup memo TTL")
	}
}

// Application delta hints
// ---------------------------------------------------------------------------

// appImagePair is a single (currentDigest → targetImageRef) pair collected from
// a rendered application.
type appImagePair struct {
	imageRef      string
	currentDigest string
}

// ambiguousCurrentDigest marks image references with multiple reported source
// digests. The current nested-hint API cannot associate a hint with a source
// digest, so these references must not be used to select a delta.
const ambiguousCurrentDigest = "\x00ambiguous"

// appDeltaResult holds the resolution outcome for one appImagePair.
type appDeltaResult struct {
	imageRef       string
	targetDigest   string
	deltaRef       *string
	deltaSizeBytes *int64
}

// appDeltaHints is the aggregate result of resolving all image pairs for a
// single application.
type appDeltaHints struct {
	parentDelta  *string
	nestedDeltas []v1beta1.ImageDeltaHint
	deltaSize    *string
}

// appDeltaResolver looks up delta generation records and resolves per-image
// delta hints for rendered applications.
type appDeltaResolver struct {
	log                    logrus.FieldLogger
	orgID                  uuid.UUID
	deltaLookup            generationLookup
	kvStore                kvstore.KVStore
	resolveDigest          func(ctx context.Context, imageRef string) (string, error)
	resolveDigestForSource func(ctx context.Context, imageRef, sourceDigest string) (oci.ImageDigestPair, error)
}

func (r *appDeltaResolver) resolveImagePair(ctx context.Context, pair appImagePair) *appDeltaResult {
	if pair.imageRef == "" {
		return nil
	}
	result := &appDeltaResult{imageRef: pair.imageRef}
	if pair.currentDigest == "" || pair.currentDigest == ambiguousCurrentDigest {
		return result
	}
	repo, err := ImageRepositoryFromRef(pair.imageRef)
	if err != nil {
		r.log.Infof("app delta hint: failed parsing repo from %q: %v", pair.imageRef, err)
		return result
	}
	sourceDigest := pair.currentDigest
	var targetDigest string
	if r.resolveDigestForSource != nil {
		resolvedPair, err := r.resolveDigestForSource(ctx, pair.imageRef, pair.currentDigest)
		if err != nil {
			r.log.Infof("app delta hint: failed resolving source-platform digests for %q: %v", pair.imageRef, err)
			return result
		}
		if resolvedPair.SourceDigest == "" || resolvedPair.TargetDigest == "" {
			r.log.Infof("app delta hint: source-platform digest resolution returned an empty digest for %q", pair.imageRef)
			return result
		}
		sourceDigest, targetDigest = resolvedPair.SourceDigest, resolvedPair.TargetDigest
	} else {
		var err error
		targetDigest, err = r.resolveDigest(ctx, pair.imageRef)
		if err != nil || targetDigest == "" {
			r.log.Infof("app delta hint: failed resolving target digest for %q: %v", pair.imageRef, err)
			return result
		}
	}
	if targetDigest == "" || sourceDigest == "" {
		r.log.Infof("app delta hint: failed resolving source-platform digests for %q", pair.imageRef)
		return result
	}
	result.targetDigest = targetDigest
	if sourceDigest == targetDigest {
		return result
	}
	key := deltastore.GenerationKey{
		OrgID:           r.orgID,
		ImageRepository: repo,
		SourceDigest:    sourceDigest,
		TargetDigest:    targetDigest,
	}
	gen, err := lookupOSDeltaGeneration(ctx, r.kvStore, r.deltaLookup, key)
	if err != nil {
		r.log.Infof("app delta hint: lookup failed repo=%s src=%s tgt=%s: %v",
			repo, sourceDigest, targetDigest, err)
		return result
	}
	if gen != nil && gen.Status == deltamodel.DeltaGenerationSucceeded && gen.DeltaRef != nil && *gen.DeltaRef != "" {
		result.deltaRef = gen.DeltaRef
		if gen.SizeBytes != nil && *gen.SizeBytes >= 0 {
			result.deltaSizeBytes = gen.SizeBytes
		}
	}
	return result
}

func (r *appDeltaResolver) resolveApp(ctx context.Context, parent *appImagePair, nested []appImagePair) *appDeltaHints {
	var parentResult *appDeltaResult
	var nestedResults []*appDeltaResult

	if parent != nil {
		parentResult = r.resolveImagePair(ctx, *parent)
	}
	seenImages := make(map[string]struct{}, len(nested))
	for i := range nested {
		if nested[i].imageRef == "" || (parent != nil && nested[i].imageRef == parent.imageRef) {
			continue
		}
		if _, ok := seenImages[nested[i].imageRef]; ok {
			continue
		}
		seenImages[nested[i].imageRef] = struct{}{}
		if result := r.resolveImagePair(ctx, nested[i]); result != nil {
			nestedResults = append(nestedResults, result)
		}
	}
	if parent == nil && len(nested) == 0 {
		return nil
	}

	hints := &appDeltaHints{}
	var deltaSizeBytes int64
	allDeltaSizesKnown := true
	hasGeneratedDelta := false
	addDeltaSize := func(result *appDeltaResult) {
		if result == nil || result.deltaRef == nil || *result.deltaRef == "" {
			return
		}
		hasGeneratedDelta = true
		if result.deltaSizeBytes == nil || *result.deltaSizeBytes < 0 {
			allDeltaSizesKnown = false
			return
		}
		if deltaSizeBytes > math.MaxInt64-*result.deltaSizeBytes {
			allDeltaSizesKnown = false
			return
		}
		deltaSizeBytes += *result.deltaSizeBytes
	}

	if parentResult != nil {
		if parentResult.deltaRef != nil {
			hints.parentDelta = parentResult.deltaRef
		}
		addDeltaSize(parentResult)
	}
	seenTargetImages := make(map[string]struct{}, len(nestedResults))
	for _, nr := range nestedResults {
		if nr.deltaRef != nil {
			if _, ok := seenTargetImages[nr.imageRef]; !ok {
				seenTargetImages[nr.imageRef] = struct{}{}
				hints.nestedDeltas = append(hints.nestedDeltas, v1beta1.ImageDeltaHint{
					TargetImage:  nr.imageRef,
					TargetDigest: nr.targetDigest,
					DeltaImage:   *nr.deltaRef,
				})
			}
		}
		addDeltaSize(nr)
	}
	if hasGeneratedDelta && allDeltaSizesKnown {
		hints.deltaSize = lo.ToPtr(FormatIECBytes(deltaSizeBytes))
	}
	if hints.parentDelta == nil && len(hints.nestedDeltas) == 0 {
		return nil
	}
	return hints
}

func collectCurrentDigests(device *domain.Device, appName string) map[string]string {
	if device == nil || device.Status == nil {
		return nil
	}
	for _, appStatus := range device.Status.Applications {
		if appStatus.Name != appName {
			continue
		}
		if appStatus.ImageDigests == nil {
			return nil
		}
		m := make(map[string]string, len(*appStatus.ImageDigests))
		for _, d := range *appStatus.ImageDigests {
			if d.Image == "" {
				continue
			}
			previous, found := m[d.Image]
			if !found {
				m[d.Image] = d.Digest
				continue
			}
			if previous == ambiguousCurrentDigest || d.Digest == "" || d.Digest == previous {
				continue
			}
			if previous == "" {
				m[d.Image] = d.Digest
				continue
			}
			m[d.Image] = ambiguousCurrentDigest
		}
		return m
	}
	return nil
}

func collectContainerAppPairs(app v1beta1.ContainerApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	var parent *appImagePair
	if err == nil {
		parent = newAppImagePair(imgSpec.Image, currentDigests)
	}
	nested := collectVolumePairs(app.Volumes, currentDigests)
	return parent, nested
}

func collectComposeAppPairs(app v1beta1.ComposeApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	var parent *appImagePair
	if err == nil {
		parent = newAppImagePair(imgSpec.Image, currentDigests)
	}
	nested := collectVolumePairs(app.Volumes, currentDigests)
	if inlineSpec, err := app.AsInlineApplicationProviderSpec(); err == nil {
		nested = append(nested, collectComposeInlinePairs(inlineSpec.Inline, currentDigests)...)
	}
	return parent, nested
}

func collectQuadletAppPairs(app v1beta1.QuadletApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	var parent *appImagePair
	if err == nil {
		parent = newAppImagePair(imgSpec.Image, currentDigests)
	}
	nested := collectVolumePairs(app.Volumes, currentDigests)
	if inlineSpec, err := app.AsInlineApplicationProviderSpec(); err == nil {
		nested = append(nested, collectQuadletInlinePairs(inlineSpec.Inline, currentDigests)...)
	}
	return parent, nested
}

func collectHelmAppPairs(app v1beta1.HelmApplication, currentDigests map[string]string, targetImageRefs []string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, nil
	}
	var parent *appImagePair
	if imgSpec.Image != "" && currentDigests[imgSpec.Image] != "" && currentDigests[imgSpec.Image] != ambiguousCurrentDigest {
		// A Helm chart isn't a container image, so only use its own reported
		// digest if a future provider starts reporting one. Do not pair it with
		// a same-repository workload digest.
		parent = &appImagePair{imageRef: imgSpec.Image, currentDigest: currentDigests[imgSpec.Image]}
	}
	// Target workload references come from rendering the target chart with the
	// target values. If rendering could not be completed, use the currently
	// reported references as a best-effort fallback for unchanged workloads.
	if targetImageRefs == nil {
		for imageRef := range currentDigests {
			if imageRef != imgSpec.Image {
				targetImageRefs = append(targetImageRefs, imageRef)
			}
		}
	}
	imagePairs := collectImagePairs(targetImageRefs, currentDigests)
	nested := make([]appImagePair, 0, len(imagePairs))
	for _, imagePair := range imagePairs {
		if imagePair.currentDigest == "" || imagePair.currentDigest == ambiguousCurrentDigest {
			continue
		}
		nested = append(nested, imagePair)
	}
	return parent, nested
}

func collectVmAppPairs(app v1beta1.VmApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	// Inline VM apps are expanded to Quadlet apps before this hint pass runs.
	// This also handles the image-backed VM provider if that render path is used.
	imgSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, nil
	}
	return newAppImagePair(imgSpec.Image, currentDigests), nil
}

func newAppImagePair(imageRef string, currentDigests map[string]string) *appImagePair {
	if imageRef == "" {
		return nil
	}
	currentDigest := currentDigests[imageRef]
	if currentDigest == "" {
		// Image references commonly change tags between application versions.
		// If the exact target reference has no status entry, use a reported
		// source digest from the same repository; delta generation pairs images
		// by repository and digest, not by tag.
		targetRepository, err := ImageRepositoryFromRef(imageRef)
		if err == nil {
			currentRefs := make([]string, 0, len(currentDigests))
			for currentRef := range currentDigests {
				currentRefs = append(currentRefs, currentRef)
			}
			sort.Strings(currentRefs)
			var repositoryDigest string
			ambiguous := false
			for _, currentRef := range currentRefs {
				currentRepository, err := ImageRepositoryFromRef(currentRef)
				if err == nil && currentRepository == targetRepository && currentDigests[currentRef] != "" {
					if currentDigests[currentRef] == ambiguousCurrentDigest {
						ambiguous = true
						break
					}
					if repositoryDigest == "" {
						repositoryDigest = currentDigests[currentRef]
					} else if repositoryDigest != currentDigests[currentRef] {
						ambiguous = true
						break
					}
				}
			}
			if !ambiguous {
				currentDigest = repositoryDigest
			}
		}
	}
	return &appImagePair{
		imageRef:      imageRef,
		currentDigest: currentDigest,
	}
}

func collectComposeInlinePairs(contents []v1beta1.ApplicationContent, currentDigests map[string]string) []appImagePair {
	composeSpec, err := appspec.ParseComposeFromSpec(contents)
	if err != nil || composeSpec == nil {
		return nil
	}
	var imageRefs []string
	for _, service := range composeSpec.Services {
		imageRefs = append(imageRefs, service.Image)
	}
	return collectImagePairs(imageRefs, currentDigests)
}

func collectQuadletInlinePairs(contents []v1beta1.ApplicationContent, currentDigests map[string]string) []appImagePair {
	imageRefs, err := appspec.ParseQuadletImageReferencesFromSpec(contents)
	if err != nil {
		return nil
	}
	return collectImagePairs(imageRefs, currentDigests)
}

func collectImagePairs(imageRefs []string, currentDigests map[string]string) []appImagePair {
	if len(imageRefs) == 0 {
		return nil
	}
	sort.Strings(imageRefs)
	pairs := make([]appImagePair, 0, len(imageRefs))
	seen := make(map[string]struct{}, len(imageRefs))
	for _, imageRef := range imageRefs {
		if imageRef == "" {
			continue
		}
		if _, ok := seen[imageRef]; ok {
			continue
		}
		seen[imageRef] = struct{}{}
		if pair := newAppImagePair(imageRef, currentDigests); pair != nil {
			pairs = append(pairs, *pair)
		}
	}
	return pairs
}

func collectVolumePairs(volumes *[]v1beta1.ApplicationVolume, currentDigests map[string]string) []appImagePair {
	if volumes == nil {
		return nil
	}
	var imageRefs []string
	for _, vol := range *volumes {
		volType, err := vol.Type()
		if err != nil {
			continue
		}
		var imageRef string
		switch volType {
		case v1beta1.ImageApplicationVolumeProviderType:
			provider, err := vol.AsImageVolumeProviderSpec()
			if err != nil {
				continue
			}
			imageRef = provider.Image.Reference
		case v1beta1.ImageMountApplicationVolumeProviderType:
			provider, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				continue
			}
			imageRef = provider.Image.Reference
		default:
			continue
		}
		if imageRef == "" {
			continue
		}
		imageRefs = append(imageRefs, imageRef)
	}
	return collectImagePairs(imageRefs, currentDigests)
}

func applyDeltaHintsToImageSpec(spec v1beta1.ImageSpec, hints *appDeltaHints) v1beta1.ImageSpec {
	if hints == nil {
		return spec
	}
	if hints.parentDelta != nil {
		spec.DeltaImage = hints.parentDelta
	}
	if len(hints.nestedDeltas) > 0 {
		spec.DeltaImages = &hints.nestedDeltas
	}
	return spec
}

// resolveRenderedAppDeltaHints resolves per-device hints against the agent's
// current application image digests. RenderSpec remains device-independent;
// RenderDevice calls this after rendering the target applications.
func (t *deviceRenderState) resolveRenderedAppDeltaHints(ctx context.Context, device *domain.Device, rendered *RenderedSpec) error {
	if device == nil || len(rendered.Applications) == 0 {
		return nil
	}

	var applications []domain.ApplicationProviderSpec
	if err := json.Unmarshal(rendered.Applications, &applications); err != nil {
		return fmt.Errorf("decode rendered applications: %w", err)
	}

	appDeltaSizes, err := t.resolveAppDeltaHints(ctx, device, applications, rendered.Config)
	if err != nil {
		return fmt.Errorf("failed resolving application delta hints: %w", err)
	}
	renderedApplications, err := json.Marshal(applications)
	if err != nil {
		return fmt.Errorf("encode rendered applications with delta hints: %w", err)
	}

	rendered.Applications = renderedApplications
	rendered.appDeltaSizes = appDeltaSizes
	return nil
}

// resolveAppDeltaHints iterates over rendered applications, resolves delta
// hints for each one, writes parent and nested hints into the image or inline
// provider, and returns a map of app-name → control-plane delta-size string.
func (t *deviceRenderState) resolveAppDeltaHints(ctx context.Context, device *domain.Device, apps []domain.ApplicationProviderSpec, renderedConfig []byte) (map[string]*string, error) {
	if device == nil {
		return nil, nil
	}
	platform := oci.DeviceImagePlatform(device)
	resolver := &appDeltaResolver{
		log:         t.log,
		orgID:       t.orgId,
		deltaLookup: t.deltaLookup,
		kvStore:     t.kvStore,
		resolveDigestForSource: func(ctx context.Context, imageRef, sourceDigest string) (oci.ImageDigestPair, error) {
			return t.resolveImageDigestPair(ctx, t.orgId, imageRef, sourceDigest, platform)
		},
	}

	var appDeltaSizes map[string]*string
	for i := range apps {
		app := &apps[i]
		appType, err := app.GetAppType()
		if err != nil {
			return nil, fmt.Errorf("get type for application at index %d: %w", i, err)
		}
		appName := appNameFromProvider(app)
		if appName == "" {
			continue
		}
		currentDigests := collectCurrentDigests(device, appName)
		if len(currentDigests) == 0 {
			continue
		}

		var parent *appImagePair
		var nested []appImagePair

		switch appType {
		case domain.AppTypeContainer:
			container, err := app.AsContainerApplication()
			if err != nil {
				return nil, fmt.Errorf("parse container application %q: %w", appName, err)
			}
			parent, nested = collectContainerAppPairs(container, currentDigests)
		case domain.AppTypeCompose:
			compose, err := app.AsComposeApplication()
			if err != nil {
				return nil, fmt.Errorf("parse compose application %q: %w", appName, err)
			}
			parent, nested = collectComposeAppPairs(compose, currentDigests)
		case domain.AppTypeQuadlet:
			quadlet, err := app.AsQuadletApplication()
			if err != nil {
				return nil, fmt.Errorf("parse quadlet application %q: %w", appName, err)
			}
			parent, nested = collectQuadletAppPairs(quadlet, currentDigests)
		case domain.AppTypeHelm:
			helm, err := app.AsHelmApplication()
			if err != nil {
				return nil, fmt.Errorf("parse helm application %q: %w", appName, err)
			}
			var targetImageRefs []string
			deadline, hasDeadline := ctx.Deadline()
			remaining := time.Duration(0)
			if hasDeadline {
				remaining = time.Until(deadline)
			}
			renderTimeout, canRender := helmDeltaHintRenderBudget(remaining, hasDeadline)
			if !canRender {
				// An empty result prevents collectHelmAppPairs from falling back to
				// potentially stale workload references reported for the old release.
				targetImageRefs = []string{}
				t.log.WithFields(logrus.Fields{
					"device":      t.event.InvolvedObject.Name,
					"application": appName,
				}).Warn("skipping target Helm chart render for delta hints because insufficient device render time remains")
			} else {
				helmRenderCtx, cancel := context.WithTimeout(ctx, renderTimeout)
				imageRefs, renderErr := t.DeviceRenderLogic.RenderHelmImageRefs(helmRenderCtx, t.orgId, device, helm, renderedConfig)
				cancel()
				if renderErr != nil {
					targetImageRefs = []string{}
					t.log.WithError(renderErr).WithFields(logrus.Fields{
						"device":      t.event.InvolvedObject.Name,
						"application": appName,
					}).Warn("failed to render target Helm chart images for delta hints; skipping hints for this Helm application")
				} else {
					targetImageRefs = imageRefs
				}
			}
			parent, nested = collectHelmAppPairs(helm, currentDigests, targetImageRefs)
		case domain.AppTypeVm:
			vm, err := app.AsVmApplication()
			if err != nil {
				return nil, fmt.Errorf("parse VM application %q: %w", appName, err)
			}
			parent, nested = collectVmAppPairs(vm, currentDigests)
		default:
			continue
		}

		hints := resolver.resolveApp(ctx, parent, nested)
		if hints == nil {
			continue
		}
		if err := applyHintsToApp(app, appType, hints); err != nil {
			return nil, fmt.Errorf("apply delta hints to application %q: %w", appName, err)
		}
		if hints.deltaSize != nil {
			if appDeltaSizes == nil {
				appDeltaSizes = make(map[string]*string)
			}
			appDeltaSizes[appName] = hints.deltaSize
		}
	}
	return appDeltaSizes, nil
}

func applyHintsToApp(app *domain.ApplicationProviderSpec, appType domain.AppType, hints *appDeltaHints) error {
	if hints == nil || (hints.parentDelta == nil && len(hints.nestedDeltas) == 0) {
		return nil
	}
	switch appType {
	case domain.AppTypeContainer:
		container, err := app.AsContainerApplication()
		if err != nil {
			return fmt.Errorf("parse container application: %w", err)
		}
		if err := applyHintsToApplicationProvider(&container, hints); err != nil {
			return fmt.Errorf("apply container provider hints: %w", err)
		}
		if err := app.MergeContainerApplication(container); err != nil {
			return fmt.Errorf("merge container application: %w", err)
		}
	case domain.AppTypeCompose:
		compose, err := app.AsComposeApplication()
		if err != nil {
			return fmt.Errorf("parse compose application: %w", err)
		}
		if err := applyHintsToApplicationProvider(&compose, hints); err != nil {
			return fmt.Errorf("apply compose provider hints: %w", err)
		}
		if err := app.MergeComposeApplication(compose); err != nil {
			return fmt.Errorf("merge compose application: %w", err)
		}
	case domain.AppTypeQuadlet:
		quadlet, err := app.AsQuadletApplication()
		if err != nil {
			return fmt.Errorf("parse quadlet application: %w", err)
		}
		if err := applyHintsToApplicationProvider(&quadlet, hints); err != nil {
			return fmt.Errorf("apply quadlet provider hints: %w", err)
		}
		if err := app.MergeQuadletApplication(quadlet); err != nil {
			return fmt.Errorf("merge quadlet application: %w", err)
		}
	case domain.AppTypeHelm:
		helm, err := app.AsHelmApplication()
		if err != nil {
			return fmt.Errorf("parse helm application: %w", err)
		}
		if err := applyHintsToApplicationProvider(&helm, hints); err != nil {
			return fmt.Errorf("apply helm provider hints: %w", err)
		}
		if err := app.MergeHelmApplication(helm); err != nil {
			return fmt.Errorf("merge helm application: %w", err)
		}
	case domain.AppTypeVm:
		vm, err := app.AsVmApplication()
		if err != nil {
			return fmt.Errorf("parse VM application: %w", err)
		}
		if err := applyHintsToApplicationProvider(&vm, hints); err != nil {
			return fmt.Errorf("apply VM provider hints: %w", err)
		}
		if err := app.MergeVmApplication(vm); err != nil {
			return fmt.Errorf("merge VM application: %w", err)
		}
	default:
		return fmt.Errorf("unsupported application type %q", appType)
	}
	return nil
}

type imageHintProvider interface {
	AsImageApplicationProviderSpec() (v1beta1.ImageApplicationProviderSpec, error)
	FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec) error
}

type inlineHintProvider interface {
	AsInlineApplicationProviderSpec() (v1beta1.InlineApplicationProviderSpec, error)
	FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec) error
}

func applyHintsToApplicationProvider(provider any, hints *appDeltaHints) error {
	if hints == nil || (hints.parentDelta == nil && len(hints.nestedDeltas) == 0) {
		return nil
	}
	providerWithType, ok := provider.(interface {
		Type() v1beta1.ApplicationProviderType
	})
	if !ok {
		return fmt.Errorf("provider %T does not expose a provider type", provider)
	}
	providerType := providerWithType.Type()
	switch providerType {
	case v1beta1.ImageApplicationProviderType:
		imageProvider, ok := provider.(imageHintProvider)
		if !ok {
			return fmt.Errorf("image provider %T does not support image hint conversion", provider)
		}
		imageSpec, err := imageProvider.AsImageApplicationProviderSpec()
		if err != nil {
			return fmt.Errorf("read image provider spec: %w", err)
		}
		imageSpec = applyDeltaHintsToImageSpec(imageSpec, hints)
		if err := imageProvider.FromImageApplicationProviderSpec(imageSpec); err != nil {
			return fmt.Errorf("write image provider spec: %w", err)
		}
	case v1beta1.InlineApplicationProviderType:
		if hints.parentDelta != nil {
			return fmt.Errorf("inline provider cannot store a parent delta hint")
		}
		inlineProvider, ok := provider.(inlineHintProvider)
		if !ok {
			return fmt.Errorf("inline provider %T does not support inline hint conversion", provider)
		}
		inlineSpec, err := inlineProvider.AsInlineApplicationProviderSpec()
		if err != nil {
			return fmt.Errorf("read inline provider spec: %w", err)
		}
		inlineSpec.DeltaImages = &hints.nestedDeltas
		if err := inlineProvider.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
			return fmt.Errorf("write inline provider spec: %w", err)
		}
	default:
		return fmt.Errorf("application provider type %q does not support delta hints", providerType)
	}
	return nil
}

func appNameFromProvider(app *domain.ApplicationProviderSpec) string {
	appType, err := app.GetAppType()
	if err != nil {
		return ""
	}
	switch appType {
	case domain.AppTypeContainer:
		a, err := app.AsContainerApplication()
		if err != nil {
			return ""
		}
		if name := util.DefaultIfNil(a.Name, ""); name != "" {
			return name
		}
		imageSpec, err := a.AsImageApplicationProviderSpec()
		if err == nil {
			return imageSpec.Image
		}
		return ""
	case domain.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return ""
		}
		if name := util.DefaultIfNil(a.Name, ""); name != "" {
			return name
		}
		imageSpec, err := a.AsImageApplicationProviderSpec()
		if err == nil {
			return imageSpec.Image
		}
		return ""
	case domain.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return ""
		}
		if name := util.DefaultIfNil(a.Name, ""); name != "" {
			return name
		}
		imageSpec, err := a.AsImageApplicationProviderSpec()
		if err == nil {
			return imageSpec.Image
		}
		return ""
	case domain.AppTypeHelm:
		a, err := app.AsHelmApplication()
		if err != nil {
			return ""
		}
		if name := util.DefaultIfNil(a.Name, ""); name != "" {
			return name
		}
		imageSpec, err := a.AsImageApplicationProviderSpec()
		if err != nil {
			return ""
		}
		name, err := chartutil.SanitizeReleaseName(imageSpec.Image)
		if err != nil {
			return ""
		}
		return name
	case domain.AppTypeVm:
		a, err := app.AsVmApplication()
		if err != nil {
			return ""
		}
		return util.DefaultIfNil(a.Name, "")
	default:
		return ""
	}
}
