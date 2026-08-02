package certificatesigningrequest

import (
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.CertificateSigningRequest) {
	if existing.Metadata.Generation == nil {
		if certificateSigningRequestHasSameSpec(existing, next) {
			next.Metadata.Generation = nil
			return
		}
		next.Metadata.Generation = lo.ToPtr(int64(1))
		return
	}
	nextGen := *existing.Metadata.Generation
	if !certificateSigningRequestHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func certificateSigningRequestHasSameSpec(a, b *domain.CertificateSigningRequest) bool {
	return reflect.DeepEqual(a.Spec, b.Spec)
}
