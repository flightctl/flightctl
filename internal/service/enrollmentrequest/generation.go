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
	nextGen := lo.FromPtr(existing.Metadata.Generation)
	if !enrollmentRequestHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func enrollmentRequestHasSameSpec(a, b *domain.EnrollmentRequest) bool {
	return reflect.DeepEqual(a.Spec, b.Spec)
}
