package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/oci"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const generationMemoTTL = 15 * time.Minute

type generationLookup interface {
	GetGeneration(ctx context.Context, key delta.GenerationKey, opts ...delta.GenerationGetOption) (*model.DeltaGeneration, error)
}

type generationMemo struct {
	Missing   bool    `json:"missing,omitempty"`
	Status    string  `json:"status,omitempty"`
	DeltaRef  *string `json:"deltaRef,omitempty"`
	SizeBytes *int64  `json:"sizeBytes,omitempty"`
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
	// Round to one decimal place.
	rounded := math.Round(val*10) / 10
	if rounded < 0.1 && unit > 0 {
		val *= 1024
		unit--
		rounded = math.Round(val*10) / 10
	}
	// At the lowest unit (KiB), round up sub-unit values to 1.
	if rounded < 1 && unit == 0 {
		return "1 KiB"
	}
	// Whole numbers omit the decimal (e.g. "1 GiB"); fractional values get
	// one decimal place (e.g. "245.3 MiB").
	if rounded == math.Trunc(rounded) {
		return fmt.Sprintf("%d %s", int64(rounded), units[unit])
	}
	return fmt.Sprintf("%.1f %s", rounded, units[unit])
}

func (t DeviceRenderLogic) WithDeltaLookup(lookup delta.Store) DeviceRenderLogic {
	t.deltaLookup = lookup
	return t
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
	spec, err := cfg.DeltaGeneration.DefaultRepository.OciRepoSpec()
	if err != nil {
		return nil
	}
	return oci.SelectWriteTarget(nil, spec)
}

func (t *DeviceRenderLogic) resolveTargetDigest(ctx context.Context, osImage string) (string, error) {
	return oci.CachedImageDigest(ctx, t.kvStore, osImage, func(ctx context.Context) (string, error) {
		return oci.InspectImageDigest(ctx, osImage, deltaWriteSpec(t.cfg))
	})
}

func hintFromGeneration(gen *model.DeltaGeneration, fallbackSize *int64) (deltaImage *string, sizeIEC *string) {
	var sizeBytes *int64
	if gen != nil && gen.SizeBytes != nil {
		sizeBytes = gen.SizeBytes
	} else {
		sizeBytes = fallbackSize
	}
	if sizeBytes != nil {
		sizeIEC = lo.ToPtr(FormatIECBytes(*sizeBytes))
	}
	if gen != nil && gen.Status == model.DeltaGenerationSucceeded && gen.DeltaRef != nil && *gen.DeltaRef != "" {
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
	if err != nil || t.deltaLookup == nil {
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
	key := delta.GenerationKey{
		OrgID:           t.orgId,
		ImageRepository: repo,
		SourceDigest:    src,
		TargetDigest:    tgt,
	}
	t.log.Infof("os delta hint query device=%s/%s repo=%s sourceDigest=%s targetDigest=%s osImage=%s",
		t.orgId, t.event.InvolvedObject.Name, repo, src, tgt, rendered.OsImage)
	gen, err := lookupCachedGeneration(ctx, t.kvStore, t.deltaLookup, key, "", delta.WithStatus(model.DeltaGenerationSucceeded))
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

func generationMemoKey(key delta.GenerationKey, ref string) string {
	return fmt.Sprintf("deltaHint/%s/%s/%s/%s/%s", key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest, ref)
}

func lookupCachedGeneration(ctx context.Context, kv kvstore.KVStore, store generationLookup, key delta.GenerationKey, ref string, opts ...delta.GenerationGetOption) (*model.DeltaGeneration, error) {
	if kv != nil {
		raw, err := kv.Get(ctx, generationMemoKey(key, ref))
		if err == nil && len(raw) > 0 {
			var memo generationMemo
			if err := json.Unmarshal(raw, &memo); err == nil {
				return generationFromMemo(key, memo), nil
			}
		}
	}

	if store == nil {
		return nil, nil
	}
	gen, err := store.GetGeneration(ctx, key, opts...)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			_ = writeGenerationMemo(ctx, kv, key, ref, generationMemo{Missing: true})
			return nil, nil
		}
		return nil, err
	}
	if gen != nil {
		_ = writeGenerationMemo(ctx, kv, key, ref, generationMemo{
			Status:    gen.Status,
			DeltaRef:  gen.DeltaRef,
			SizeBytes: gen.SizeBytes,
		})
	}
	return gen, nil
}

func generationFromMemo(key delta.GenerationKey, memo generationMemo) *model.DeltaGeneration {
	if memo.Missing {
		return nil
	}
	return &model.DeltaGeneration{
		OrgID:           key.OrgID,
		ImageRepository: key.ImageRepository,
		SourceDigest:    key.SourceDigest,
		TargetDigest:    key.TargetDigest,
		Status:          memo.Status,
		DeltaRef:        memo.DeltaRef,
		SizeBytes:       memo.SizeBytes,
	}
}

func writeGenerationMemo(ctx context.Context, kv kvstore.KVStore, key delta.GenerationKey, ref string, memo generationMemo) error {
	if kv == nil {
		return nil
	}
	raw, err := json.Marshal(memo)
	if err != nil {
		return err
	}
	cacheKey := generationMemoKey(key, ref)
	if _, err := kv.SetNX(ctx, cacheKey, raw); err != nil {
		return err
	}
	return kv.SetExpire(ctx, cacheKey, generationMemoTTL)
}

// ---------------------------------------------------------------------------
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
	key := delta.GenerationKey{
		OrgID:           r.orgID,
		ImageRepository: repo,
		SourceDigest:    pair.currentDigest,
		TargetDigest:    targetDigest,
	}
	gen, err := lookupCachedGeneration(ctx, r.kvStore, r.deltaLookup, key, "")
	if err != nil {
		r.log.Infof("app delta hint: lookup failed repo=%s src=%s tgt=%s: %v",
			repo, pair.currentDigest, targetDigest, err)
		return nil
	}
	result := &appDeltaResult{targetDigest: targetDigest}
	if gen != nil {
		result.sizeBytes = gen.SizeBytes
		if gen.Status == model.DeltaGenerationSucceeded && gen.DeltaRef != nil && *gen.DeltaRef != "" {
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
	for i := range nested {
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
	for _, nr := range nestedResults {
		if nr.deltaRef != nil {
			hints.nestedDeltas = append(hints.nestedDeltas, v1beta1.ImageDeltaHint{
				TargetDigest: nr.targetDigest,
				DeltaImage:   *nr.deltaRef,
			})
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
	if err != nil || imgSpec.Image == "" {
		return nil, nil
	}
	parent := &appImagePair{
		imageRef:      imgSpec.Image,
		currentDigest: currentDigests[imgSpec.Image],
	}
	nested := collectVolumePairs(app.Volumes, currentDigests)
	return parent, nested
}

func collectComposeAppPairs(app v1beta1.ComposeApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	if err == nil && imgSpec.Image != "" {
		parent := &appImagePair{
			imageRef:      imgSpec.Image,
			currentDigest: currentDigests[imgSpec.Image],
		}
		nested := collectVolumePairs(app.Volumes, currentDigests)
		return parent, nested
	}
	nested := collectVolumePairs(app.Volumes, currentDigests)
	return nil, nested
}

func collectQuadletAppPairs(app v1beta1.QuadletApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	if err == nil && imgSpec.Image != "" {
		parent := &appImagePair{
			imageRef:      imgSpec.Image,
			currentDigest: currentDigests[imgSpec.Image],
		}
		nested := collectVolumePairs(app.Volumes, currentDigests)
		return parent, nested
	}
	nested := collectVolumePairs(app.Volumes, currentDigests)
	return nil, nested
}

func collectHelmAppPairs(app v1beta1.HelmApplication, currentDigests map[string]string) (*appImagePair, []appImagePair) {
	imgSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil || imgSpec.Image == "" {
		return nil, nil
	}
	parent := &appImagePair{
		imageRef:      imgSpec.Image,
		currentDigest: currentDigests[imgSpec.Image],
	}
	return parent, nil
}

func collectVolumePairs(volumes *[]v1beta1.ApplicationVolume, currentDigests map[string]string) []appImagePair {
	if volumes == nil {
		return nil
	}
	var pairs []appImagePair
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
		pairs = append(pairs, appImagePair{
			imageRef:      imageRef,
			currentDigest: currentDigests[imageRef],
		})
	}
	return pairs
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
// hints for each one, writes deltaImage/deltaImages into the application's
// ImageSpec, and returns a map of app-name → IEC size string.
func (t *DeviceRenderLogic) resolveAppDeltaHints(ctx context.Context, device *domain.Device, apps []domain.ApplicationProviderSpec) map[string]*string {
	if t.deltaLookup == nil || device == nil {
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
		imgSpec, err := container.AsImageApplicationProviderSpec()
		if err != nil || imgSpec.Image == "" {
			return
		}
		imgSpec = applyDeltaHintsToImageSpec(imgSpec, hints)
		if err := container.FromImageApplicationProviderSpec(imgSpec); err != nil {
			return
		}
		_ = app.MergeContainerApplication(container)
	case domain.AppTypeCompose:
		compose, err := app.AsComposeApplication()
		if err != nil {
			return
		}
		imgSpec, err := compose.AsImageApplicationProviderSpec()
		if err != nil || imgSpec.Image == "" {
			return
		}
		imgSpec = applyDeltaHintsToImageSpec(imgSpec, hints)
		if err := compose.FromImageApplicationProviderSpec(imgSpec); err != nil {
			return
		}
		_ = app.MergeComposeApplication(compose)
	case domain.AppTypeQuadlet:
		quadlet, err := app.AsQuadletApplication()
		if err != nil {
			return
		}
		imgSpec, err := quadlet.AsImageApplicationProviderSpec()
		if err != nil || imgSpec.Image == "" {
			return
		}
		imgSpec = applyDeltaHintsToImageSpec(imgSpec, hints)
		if err := quadlet.FromImageApplicationProviderSpec(imgSpec); err != nil {
			return
		}
		_ = app.MergeQuadletApplication(quadlet)
	case domain.AppTypeHelm:
		helm, err := app.AsHelmApplication()
		if err != nil {
			return
		}
		imgSpec, err := helm.AsImageApplicationProviderSpec()
		if err != nil || imgSpec.Image == "" {
			return
		}
		imgSpec = applyDeltaHintsToImageSpec(imgSpec, hints)
		if err := helm.FromImageApplicationProviderSpec(imgSpec); err != nil {
			return
		}
		_ = app.MergeHelmApplication(helm)
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
		return util.DefaultIfNil(a.Name, "")
	case domain.AppTypeCompose:
		a, err := app.AsComposeApplication()
		if err != nil {
			return ""
		}
		return util.DefaultIfNil(a.Name, "")
	case domain.AppTypeQuadlet:
		a, err := app.AsQuadletApplication()
		if err != nil {
			return ""
		}
		return util.DefaultIfNil(a.Name, "")
	case domain.AppTypeHelm:
		a, err := app.AsHelmApplication()
		if err != nil {
			return ""
		}
		return util.DefaultIfNil(a.Name, "")
	default:
		return ""
	}
}
