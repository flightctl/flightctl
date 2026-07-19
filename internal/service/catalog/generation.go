package catalog

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.Catalog) {
	nextGen := lo.FromPtr(existing.Metadata.Generation)
	if !catalogHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func catalogHasSameSpec(a, b *domain.Catalog) bool {
	return domain.CatalogSpecsAreEqual(a.Spec, b.Spec)
}
