package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store/delta"
	"github.com/flightctl/flightctl/internal/store/model"
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
