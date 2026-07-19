package device

import (
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.Device) {
	if existing.Metadata.Generation == nil {
		if deviceHasSameSpec(existing, next) {
			next.Metadata.Generation = nil
			return
		}
		next.Metadata.Generation = lo.ToPtr(int64(1))
		return
	}
	nextGen := *existing.Metadata.Generation
	if !deviceHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func deviceHasSameSpec(a, b *domain.Device) bool {
	if a.Spec == nil && b.Spec == nil {
		return true
	}
	if a.Spec == nil || b.Spec == nil {
		return false
	}
	return domain.DeviceSpecsAreEqual(*a.Spec, *b.Spec)
}
