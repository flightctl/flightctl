package common

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
)

func TestExtractAppCatalogItemRef(t *testing.T) {
	tests := []struct {
		name    string
		app     func() domain.ApplicationProviderSpec
		wantRef bool
	}{
		{
			name: "When the container app uses an image provider it should return nil",
			app: func() domain.ApplicationProviderSpec {
				container := domain.ContainerApplication{
					Name:    lo.ToPtr("test-app"),
					AppType: domain.AppTypeContainer,
				}
				_ = container.FromImageApplicationProviderSpec(domain.ImageApplicationProviderSpec{
					Image: "quay.io/test/image:latest",
				})
				var spec domain.ApplicationProviderSpec
				_ = spec.FromContainerApplication(container)
				return spec
			},
			wantRef: false,
		},
		{
			name: "When the container app uses a catalog item ref it should return the ref",
			app: func() domain.ApplicationProviderSpec {
				container := domain.ContainerApplication{
					Name:    lo.ToPtr("test-app"),
					AppType: domain.AppTypeContainer,
				}
				_ = container.FromCatalogItemRefApplicationProviderSpec(domain.CatalogItemRefApplicationProviderSpec{
					CatalogItemRef: domain.CatalogItemRefSpec{
						Catalog: "my-catalog",
						Item:    "my-item",
						Version: "1.0.0",
					},
				})
				var spec domain.ApplicationProviderSpec
				_ = spec.FromContainerApplication(container)
				return spec
			},
			wantRef: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := tt.app()
			ref, _ := ExtractAppCatalogItemRef(&app)
			if tt.wantRef && ref == nil {
				t.Error("expected catalog item ref but got nil")
			}
			if !tt.wantRef && ref != nil {
				t.Errorf("expected nil but got catalog item ref: %+v", ref)
			}
			if tt.wantRef && ref != nil {
				if ref.Catalog != "my-catalog" {
					t.Errorf("expected catalog 'my-catalog', got %q", ref.Catalog)
				}
			}
		})
	}
}

func TestCollectCatalogItemRefs(t *testing.T) {
	t.Run("When no applications use catalog refs it should return empty", func(t *testing.T) {
		container := domain.ContainerApplication{
			Name:    lo.ToPtr("test-app"),
			AppType: domain.AppTypeContainer,
		}
		_ = container.FromImageApplicationProviderSpec(domain.ImageApplicationProviderSpec{
			Image: "quay.io/test/image:latest",
		})
		var appSpec domain.ApplicationProviderSpec
		_ = appSpec.FromContainerApplication(container)

		spec := &domain.DeviceSpec{
			Applications: &[]domain.ApplicationProviderSpec{appSpec},
		}

		refs := CollectCatalogItemRefs(spec)
		if len(refs) != 0 {
			t.Errorf("expected 0 refs, got %d: %+v", len(refs), refs)
		}
	})
}
