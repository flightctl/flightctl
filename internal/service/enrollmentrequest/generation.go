package enrollmentrequest

import (
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.EnrollmentRequest) {
	if existing.Metadata.Generation == nil {
		if enrollmentRequestHasSameSpec(existing, next) {
			next.Metadata.Generation = nil
			return
		}
		next.Metadata.Generation = lo.ToPtr(int64(1))
		return
	}
	nextGen := *existing.Metadata.Generation
	if !enrollmentRequestHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func enrollmentRequestHasSameSpec(a, b *domain.EnrollmentRequest) bool {
	return reflect.DeepEqual(a.Spec, b.Spec)
}
