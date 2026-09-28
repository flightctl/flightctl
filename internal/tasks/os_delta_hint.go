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
	agentclient "github.com/flightctl/flightctl/internal/agent/client"
	apphelm "github.com/flightctl/flightctl/internal/agent/device/applications/helm"
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
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type generationLookup interface {
	GetDeltaGeneration(ctx context.Context, key deltastore.GenerationKey, opts ...deltastore.GenerationGetOption) (*deltamodel.DeltaGeneration, error)
}

const deltaGenerationMissingMemoTTL = time.Minute

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

func (t *DeviceRenderLogic) resolveTargetDigest(ctx context.Context, osImage string) (string, error) {
	return oci.CachedImageDigest(ctx, t.kvStore, t.orgId, osImage, func(ctx context.Context) (string, error) {
		return oci.InspectImageDigest(ctx, osImage, deltaWriteSpec(t.cfg))
	})
}

func hintFromGeneration(gen *deltamodel.DeltaGeneration, fallbackSize *int64) (deltaImage *string, sizeIEC *string) {
	var sizeBytes *int64
	if gen != nil && gen.SizeBytes != nil {
		sizeBytes = gen.SizeBytes
	} else {
		sizeBytes = fallbackSize
	}
	if sizeBytes != nil {
		sizeIEC = lo.ToPtr(FormatIECBytes(*sizeBytes))
	}
	if gen != nil && gen.Status == deltamodel.DeltaGenerationSucceeded && gen.DeltaRef != nil && *gen.DeltaRef != "" {
		deltaImage = gen.DeltaRef
	}
	return deltaImage, sizeIEC
}

func (t *DeviceRenderLogic) resolveOSDeltaHint(ctx context.Context, device *domain.Device, rendered RenderedSpec) *deviceservice.RenderedOSHints {
	if rendered.OsImage == "" {
		return nil
	}
	var fallback *int64
	if t.osManifestSize != nil {
		fallback, _ = t.osManifestSize(ctx, rendered.OsImage)
	}
	repo, err := ImageRepositoryFromRef(rendered.OsImage)
	if err != nil {
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
	}
	src := ""
	if device != nil && device.Status != nil {
		src = device.Status.Os.ImageDigest
	}
	if src == "" {
		t.log.Infof("os delta hint skipped device=%s/%s reason=empty-source-digest osImage=%q repo=%s",
			t.orgId, t.event.InvolvedObject.Name, rendered.OsImage, repo)
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
	}
	tgt, err := t.resolveTargetDigest(ctx, rendered.OsImage)
	if err != nil {
		t.log.Infof("os delta hint skipped device=%s/%s reason=inspect-target-digest osImage=%q err=%v",
			t.orgId, t.event.InvolvedObject.Name, rendered.OsImage, err)
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
	}
	if tgt == "" {
		t.log.Infof("os delta hint skipped device=%s/%s reason=empty-target-digest osImage=%q repo=%s",
			t.orgId, t.event.InvolvedObject.Name, rendered.OsImage, repo)
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
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
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
	}
	img, size := hintFromGeneration(gen, fallback)
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
	if img == nil && size == nil {
		return nil
	}
	return &deviceservice.RenderedOSHints{DeltaImage: img, UpdatedSize: size}
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
			return nil, nil
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

// appDeltaResult holds the resolution outcome for one appImagePair.
type appDeltaResult struct {
	imageRef     string
	targetDigest string
	deltaRef     *string
	sizeBytes    *int64
}

// appDeltaHints is the aggregate result of resolving all image pairs for a
// single application.
type appDeltaHints struct {
	parentDelta  *string
	nestedDeltas []v1beta1.ImageDeltaHint
	totalSize    *string
}

// appDeltaResolver looks up delta generation records and resolves per-image
// delta hints for rendered applications.
type appDeltaResolver struct {
	log           logrus.FieldLogger
	orgID         uuid.UUID
	deltaLookup   generationLookup
	kvStore       kvstore.KVStore
	resolveDigest func(ctx context.Context, imageRef string) (string, error)
}

func (r *appDeltaResolver) resolveImagePair(ctx context.Context, pair appImagePair) *appDeltaResult {
	if pair.currentDigest == "" || pair.imageRef == "" {
		return nil
	}
	repo, err := ImageRepositoryFromRef(pair.imageRef)
	if err != nil {
		r.log.Infof("app delta hint: failed parsing repo from %q: %v", pair.imageRef, err)
		return nil
	}
	targetDigest, err := r.resolveDigest(ctx, pair.imageRef)
	if err != nil || targetDigest == "" {
		r.log.Infof("app delta hint: failed resolving target digest for %q: %v", pair.imageRef, err)
		return nil
	}
	if pair.currentDigest == targetDigest {
		return nil
	}
	key := deltastore.GenerationKey{
		OrgID:           r.orgID,
		ImageRepository: repo,
		SourceDigest:    pair.currentDigest,
		TargetDigest:    targetDigest,
	}
	gen, err := lookupOSDeltaGeneration(ctx, r.kvStore, r.deltaLookup, key)
	if err != nil {
		r.log.Infof("app delta hint: lookup failed repo=%s src=%s tgt=%s: %v",
			repo, pair.currentDigest, targetDigest, err)
		return nil
	}
	result := &appDeltaResult{imageRef: pair.imageRef, targetDigest: targetDigest}
	if gen != nil {
		result.sizeBytes = gen.SizeBytes
		if gen.Status == deltamodel.DeltaGenerationSucceeded && gen.DeltaRef != nil && *gen.DeltaRef != "" {
			result.deltaRef = gen.DeltaRef
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
	if parentResult == nil && len(nestedResults) == 0 {
		return nil
	}

	hints := &appDeltaHints{}
	var totalBytes int64
	haveSizeData := false

	if parentResult != nil {
		if parentResult.deltaRef != nil {
			hints.parentDelta = parentResult.deltaRef
		}
		if parentResult.sizeBytes != nil {
			totalBytes += *parentResult.sizeBytes
			haveSizeData = true
		}
	}
	seenTargetImages := make(map[string]struct{}, len(nestedResults))
	for _, nr := range nestedResults {
		if nr.deltaRef != nil {
			if _, ok := seenTargetImages[nr.imageRef]; !ok {
				seenTargetImages[nr.imageRef] = struct{}{}
				hints.nestedDeltas = append(hints.nestedDeltas, v1beta1.ImageDeltaHint{
					TargetImage:  lo.ToPtr(nr.imageRef),
					TargetDigest: nr.targetDigest,
					DeltaImage:   *nr.deltaRef,
				})
			}
		}
		if nr.sizeBytes != nil {
			totalBytes += *nr.sizeBytes
			haveSizeData = true
		}
	}
	if haveSizeData {
		hints.totalSize = lo.ToPtr(FormatIECBytes(totalBytes))
	}
	if hints.parentDelta == nil && len(hints.nestedDeltas) == 0 && hints.totalSize == nil {
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
			m[d.Image] = d.Digest
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

func collectHelmAppPairs(app v1beta1.HelmApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, nil
	}
	var parent *appImagePair
	if imgSpec.Image != "" {
		// A Helm chart isn't a container image, so only use its own reported
		// digest if a future provider starts reporting one. Do not pair it with
		// a same-repository workload digest.
		parent = &appImagePair{imageRef: imgSpec.Image, currentDigest: currentDigests[imgSpec.Image]}
	}
	// Helm workload images are only known after the chart is rendered on the
	// device. Reuse currently reported workload references as possible targets;
	// the agent will apply a hint only when the rendered chart still uses that
	// exact reference.
	var workloadImageRefs []string
	for imageRef := range currentDigests {
		if imageRef != imgSpec.Image {
			workloadImageRefs = append(workloadImageRefs, imageRef)
		}
	}
	return parent, collectImagePairs(workloadImageRefs, currentDigests)
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
	composeSpec, err := agentclient.ParseComposeFromSpec(contents)
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
	imageRefs, err := agentclient.ParseQuadletImageReferencesFromSpec(contents)
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

// resolveAppDeltaHints iterates over rendered applications, resolves delta
// hints for each one, writes parent and nested hints into the image or inline
// provider, and returns a map of app-name → IEC size string.
func (t *DeviceRenderLogic) resolveAppDeltaHints(ctx context.Context, device *domain.Device, apps []domain.ApplicationProviderSpec) map[string]*string {
	if device == nil {
		return nil
	}
	resolver := &appDeltaResolver{
		log:         t.log,
		orgID:       t.orgId,
		deltaLookup: t.deltaLookup,
		kvStore:     t.kvStore,
		resolveDigest: func(ctx context.Context, imageRef string) (string, error) {
			return t.resolveTargetDigest(ctx, imageRef)
		},
	}

	var appSizes map[string]*string
	for i := range apps {
		app := &apps[i]
		appType, err := app.GetAppType()
		if err != nil {
			continue
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
				continue
			}
			parent, nested = collectContainerAppPairs(container, currentDigests)
		case domain.AppTypeCompose:
			compose, err := app.AsComposeApplication()
			if err != nil {
				continue
			}
			parent, nested = collectComposeAppPairs(compose, currentDigests)
		case domain.AppTypeQuadlet:
			quadlet, err := app.AsQuadletApplication()
			if err != nil {
				continue
			}
			parent, nested = collectQuadletAppPairs(quadlet, currentDigests)
		case domain.AppTypeHelm:
			helm, err := app.AsHelmApplication()
			if err != nil {
				continue
			}
			parent, nested = collectHelmAppPairs(helm, currentDigests)
		case domain.AppTypeVm:
			vm, err := app.AsVmApplication()
			if err != nil {
				continue
			}
			parent, nested = collectVmAppPairs(vm, currentDigests)
		default:
			continue
		}

		hints := resolver.resolveApp(ctx, parent, nested)
		if hints == nil {
			continue
		}
		applyHintsToApp(app, appType, hints)
		if hints.totalSize != nil {
			if appSizes == nil {
				appSizes = make(map[string]*string)
			}
			appSizes[appName] = hints.totalSize
		}
	}
	return appSizes
}

func applyHintsToApp(app *domain.ApplicationProviderSpec, appType domain.AppType, hints *appDeltaHints) {
	if hints == nil {
		return
	}
	switch appType {
	case domain.AppTypeContainer:
		container, err := app.AsContainerApplication()
		if err != nil {
			return
		}
		applyHintsToApplicationProvider(&container, hints)
		_ = app.MergeContainerApplication(container)
	case domain.AppTypeCompose:
		compose, err := app.AsComposeApplication()
		if err != nil {
			return
		}
		applyHintsToApplicationProvider(&compose, hints)
		_ = app.MergeComposeApplication(compose)
	case domain.AppTypeQuadlet:
		quadlet, err := app.AsQuadletApplication()
		if err != nil {
			return
		}
		applyHintsToApplicationProvider(&quadlet, hints)
		_ = app.MergeQuadletApplication(quadlet)
	case domain.AppTypeHelm:
		helm, err := app.AsHelmApplication()
		if err != nil {
			return
		}
		applyHintsToApplicationProvider(&helm, hints)
		_ = app.MergeHelmApplication(helm)
	case domain.AppTypeVm:
		vm, err := app.AsVmApplication()
		if err != nil {
			return
		}
		applyHintsToApplicationProvider(&vm, hints)
		_ = app.MergeVmApplication(vm)
	}
}

type imageHintProvider interface {
	AsImageApplicationProviderSpec() (v1beta1.ImageApplicationProviderSpec, error)
	FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec) error
}

type inlineHintProvider interface {
	AsInlineApplicationProviderSpec() (v1beta1.InlineApplicationProviderSpec, error)
	FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec) error
}

func applyHintsToApplicationProvider(provider any, hints *appDeltaHints) {
	providerWithType, ok := provider.(interface {
		Type() v1beta1.ApplicationProviderType
	})
	if !ok || hints == nil {
		return
	}
	switch providerWithType.Type() {
	case v1beta1.ImageApplicationProviderType:
		imageProvider, ok := provider.(imageHintProvider)
		if !ok {
			return
		}
		imageSpec, err := imageProvider.AsImageApplicationProviderSpec()
		if err != nil {
			return
		}
		imageSpec = applyDeltaHintsToImageSpec(imageSpec, hints)
		_ = imageProvider.FromImageApplicationProviderSpec(imageSpec)
	case v1beta1.InlineApplicationProviderType:
		inlineProvider, ok := provider.(inlineHintProvider)
		if !ok || len(hints.nestedDeltas) == 0 {
			return
		}
		inlineSpec, err := inlineProvider.AsInlineApplicationProviderSpec()
		if err != nil {
			return
		}
		inlineSpec.DeltaImages = &hints.nestedDeltas
		_ = inlineProvider.FromInlineApplicationProviderSpec(inlineSpec)
	}
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
		name, err := apphelm.SanitizeReleaseName(imageSpec.Image)
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
