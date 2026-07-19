package repository

import (
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.Repository) {
	nextGen := lo.FromPtr(existing.Metadata.Generation)
	if !repositoryHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func repositoryHasSameSpec(a, b *domain.Repository) bool {
	return reflect.DeepEqual(a.Spec, b.Spec)
}
