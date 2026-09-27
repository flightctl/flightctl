package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/config"
	deltamodel "github.com/flightctl/flightctl/internal/delta_worker/model"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/oci"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
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
	rounded := int64(val + 0.5)
	if rounded == 0 && unit > 0 {
		val *= 1024
		unit--
		rounded = int64(val + 0.5)
	}
	if rounded == 0 {
		rounded = 1
	}
	return fmt.Sprintf("%d %s", rounded, units[unit])
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
