package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/samber/lo"
)

const generationMemoTTL = 15 * time.Minute

type generationLookup interface {
	GetGeneration(ctx context.Context, key delta.GenerationKey) (*model.DeltaGeneration, error)
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

func digestFromImageRef(imageRef string) string {
	named, err := reference.ParseNormalizedNamed(imageRef)
	if err != nil {
		return ""
	}
	digested, ok := named.(reference.Digested)
	if !ok {
		return ""
	}
	return digested.Digest().String()
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
	tgt := digestFromImageRef(rendered.OsImage)
	if src == "" || tgt == "" {
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
	}
	gen, err := lookupCachedGeneration(ctx, t.kvStore, t.deltaLookup, delta.GenerationKey{
		OrgID:           t.orgId,
		ImageRepository: repo,
		SourceDigest:    src,
		TargetDigest:    tgt,
	})
	if err != nil {
		t.log.Warnf("delta generation lookup failed for device %s/%s: %v", t.orgId, t.event.InvolvedObject.Name, err)
		_, size := hintFromGeneration(nil, fallback)
		if size == nil {
			return nil
		}
		return &deviceservice.RenderedOSHints{UpdatedSize: size}
	}
	img, size := hintFromGeneration(gen, fallback)
	if img == nil && size == nil {
		return nil
	}
	return &deviceservice.RenderedOSHints{DeltaImage: img, UpdatedSize: size}
}

func generationMemoKey(key delta.GenerationKey) string {
	return fmt.Sprintf("deltaHint/%s/%s/%s/%s", key.OrgID, key.ImageRepository, key.SourceDigest, key.TargetDigest)
}

func lookupCachedGeneration(ctx context.Context, kv kvstore.KVStore, store generationLookup, key delta.GenerationKey) (*model.DeltaGeneration, error) {
	if kv != nil {
		raw, err := kv.Get(ctx, generationMemoKey(key))
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
	gen, err := store.GetGeneration(ctx, key)
	if err != nil {
		if errors.Is(err, flterrors.ErrResourceNotFound) {
			_ = writeGenerationMemo(ctx, kv, key, generationMemo{Missing: true})
			return nil, nil
		}
		return nil, err
	}
	if gen != nil {
		_ = writeGenerationMemo(ctx, kv, key, generationMemo{
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

func writeGenerationMemo(ctx context.Context, kv kvstore.KVStore, key delta.GenerationKey, memo generationMemo) error {
	if kv == nil {
		return nil
	}
	raw, err := json.Marshal(memo)
	if err != nil {
		return err
	}
	cacheKey := generationMemoKey(key)
	if _, err := kv.SetNX(ctx, cacheKey, raw); err != nil {
		return err
	}
	return kv.SetExpire(ctx, cacheKey, generationMemoTTL)
}
