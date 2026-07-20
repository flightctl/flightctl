package service

import (
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/samber/lo"
)

// setGenerationOnCreate sets the initial generation for a newly created
// ImageBuilder API resource. Mirrors internal/service/catalog/generation.go —
// generation decisions live in the service, not the store.
func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

// incrementGenerationOnSpecChange increments generation by 1 when specChanged
// is true, otherwise carries the existing generation forward unchanged.
func incrementGenerationOnSpecChange(existingGeneration *int64, specChanged bool) *int64 {
	next := lo.FromPtr(existingGeneration)
	if specChanged {
		next++
	}
	return lo.ToPtr(next)
}
