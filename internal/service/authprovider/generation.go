package authprovider

import (
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.AuthProvider) {
	nextGen := lo.FromPtr(existing.Metadata.Generation)
	if !authProviderHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func authProviderHasSameSpec(a, b *domain.AuthProvider) bool {
	return reflect.DeepEqual(a.Spec, b.Spec)
}
