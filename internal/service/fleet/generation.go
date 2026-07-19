package fleet

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.Fleet) {
	nextGen := lo.FromPtr(existing.Metadata.Generation)
	if !fleetHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func fleetHasSameSpec(a, b *domain.Fleet) bool {
	return domain.FleetSpecsAreEqual(a.Spec, b.Spec)
}
