package resourcesync

import (
	"reflect"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func setGenerationOnCreate(meta *domain.ObjectMeta) {
	meta.Generation = lo.ToPtr(int64(1))
}

func setGenerationOnUpdate(existing, next *domain.ResourceSync) {
	nextGen := lo.FromPtr(existing.Metadata.Generation)
	if !resourceSyncHasSameSpec(existing, next) {
		nextGen++
	}
	next.Metadata.Generation = lo.ToPtr(nextGen)
}

func resourceSyncHasSameSpec(a, b *domain.ResourceSync) bool {
	return reflect.DeepEqual(a.Spec, b.Spec)
}
