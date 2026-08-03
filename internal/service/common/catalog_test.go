package common

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	"github.com/google/uuid"
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

type fakeCatalogStore struct {
	catalogstore.Store
	items map[string]*domain.CatalogItem
}

func (s *fakeCatalogStore) GetItem(_ context.Context, _ uuid.UUID, catalogName string, itemName string) (*domain.CatalogItem, error) {
	key := catalogName + "/" + itemName
	item, ok := s.items[key]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return item, nil
}

func makeCatalogRefApp(catalog, item, version string, name *string, envVars *map[string]string) domain.ApplicationProviderSpec {
	container := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    name,
		EnvVars: envVars,
	}
	_ = container.FromCatalogItemRefApplicationProviderSpec(domain.CatalogItemRefApplicationProviderSpec{
		CatalogItemRef: domain.CatalogItemRefSpec{
			Catalog: catalog,
			Item:    item,
			Version: version,
		},
	})
	var spec domain.ApplicationProviderSpec
	_ = spec.FromContainerApplication(container)
	return spec
}

func TestGetEffectiveConfigSchema(t *testing.T) {
	schema1 := &map[string]interface{}{"type": "object"}
	schema2 := &map[string]interface{}{"type": "object", "required": []interface{}{"envVars"}}

	tests := []struct {
		name    string
		item    *domain.CatalogItem
		version *domain.CatalogItemVersion
		wantNil bool
		want    *map[string]interface{}
	}{
		{
			name:    "When version has configSchema it should return version schema",
			item:    &domain.CatalogItem{Spec: domain.CatalogItemSpec{}},
			version: &domain.CatalogItemVersion{ConfigSchema: schema1},
			want:    schema1,
		},
		{
			name: "When version has no configSchema but defaults does it should return defaults schema",
			item: &domain.CatalogItem{Spec: domain.CatalogItemSpec{
				Defaults: &domain.CatalogItemConfigurable{ConfigSchema: schema2},
			}},
			version: &domain.CatalogItemVersion{},
			want:    schema2,
		},
		{
			name: "When version has configSchema and defaults does too it should return version schema",
			item: &domain.CatalogItem{Spec: domain.CatalogItemSpec{
				Defaults: &domain.CatalogItemConfigurable{ConfigSchema: schema2},
			}},
			version: &domain.CatalogItemVersion{ConfigSchema: schema1},
			want:    schema1,
		},
		{
			name:    "When neither version nor defaults has configSchema it should return nil",
			item:    &domain.CatalogItem{Spec: domain.CatalogItemSpec{}},
			version: &domain.CatalogItemVersion{},
			wantNil: true,
		},
		{
			name:    "When version is nil and defaults has no configSchema it should return nil",
			item:    &domain.CatalogItem{Spec: domain.CatalogItemSpec{}},
			version: nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getEffectiveConfigSchema(tt.item, tt.version)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %v", got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestValidateDataAgainstConfigSchema(t *testing.T) {
	tests := []struct {
		name     string
		schema   *map[string]interface{}
		data     interface{}
		wantErrs bool
		errCount int
	}{
		{
			name:   "When schema is nil it should return no errors",
			schema: nil,
			data:   map[string]interface{}{"key": "value"},
		},
		{
			name:   "When data conforms to schema it should return no errors",
			schema: &map[string]interface{}{"type": "object"},
			data:   map[string]interface{}{"key": "value"},
		},
		{
			name:     "When data type mismatches schema it should return errors",
			schema:   &map[string]interface{}{"type": "string"},
			data:     map[string]interface{}{"key": "value"},
			wantErrs: true,
		},
		{
			name: "When data is missing required properties it should return errors",
			schema: &map[string]interface{}{
				"type":       "object",
				"required":   []interface{}{"envVars"},
				"properties": map[string]interface{}{"envVars": map[string]interface{}{"type": "object"}},
			},
			data:     map[string]interface{}{"appType": "container"},
			wantErrs: true,
		},
		{
			name: "When data satisfies required properties it should return no errors",
			schema: &map[string]interface{}{
				"type":       "object",
				"required":   []interface{}{"envVars"},
				"properties": map[string]interface{}{"envVars": map[string]interface{}{"type": "object"}},
			},
			data: map[string]interface{}{"envVars": map[string]interface{}{"KEY": "val"}},
		},
		{
			name: "When schema sets additionalProperties false it should still allow extra fields",
			schema: &map[string]interface{}{
				"type":                 "object",
				"additionalProperties": false,
				"properties":           map[string]interface{}{"envVars": map[string]interface{}{"type": "object"}},
			},
			data: map[string]interface{}{
				"envVars": map[string]interface{}{"KEY": "val"},
				"appType": "container",
				"name":    "my-app",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateDataAgainstConfigSchema(tt.schema, tt.data)
			if tt.wantErrs && len(errs) == 0 {
				t.Error("expected validation errors but got none")
			}
			if !tt.wantErrs && len(errs) > 0 {
				t.Errorf("expected no errors but got: %v", errs)
			}
		})
	}
}

func TestValidateCatalogItemRefs_ConfigSchema(t *testing.T) {
	requireSchema := &map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"envVars"},
		"properties": map[string]interface{}{
			"envVars": map[string]interface{}{"type": "object"},
		},
	}

	tests := []struct {
		name       string
		items      map[string]*domain.CatalogItem
		spec       *domain.DeviceSpec
		wantOK     bool
		wantSubstr string
	}{
		{
			name: "When app conforms to configSchema it should pass",
			items: map[string]*domain.CatalogItem{
				"mycat/myitem": {
					Spec: domain.CatalogItemSpec{
						Versions: []domain.CatalogItemVersion{
							{Version: "1.0.0", ConfigSchema: requireSchema, Channels: []string{"stable"}, References: map[domain.CatalogItemArtifactType]string{}},
						},
					},
				},
			},
			spec: &domain.DeviceSpec{
				Applications: &[]domain.ApplicationProviderSpec{
					makeCatalogRefApp("mycat", "myitem", "1.0.0", lo.ToPtr("myapp"), &map[string]string{"KEY": "val"}),
				},
			},
			wantOK: true,
		},
		{
			name: "When app violates configSchema it should return bad request",
			items: map[string]*domain.CatalogItem{
				"mycat/myitem": {
					Spec: domain.CatalogItemSpec{
						Versions: []domain.CatalogItemVersion{
							{Version: "1.0.0", ConfigSchema: requireSchema, Channels: []string{"stable"}, References: map[domain.CatalogItemArtifactType]string{}},
						},
					},
				},
			},
			spec: &domain.DeviceSpec{
				Applications: &[]domain.ApplicationProviderSpec{
					makeCatalogRefApp("mycat", "myitem", "1.0.0", lo.ToPtr("myapp"), nil),
				},
			},
			wantOK:     false,
			wantSubstr: "configSchema",
		},
		{
			name: "When catalog item has no configSchema it should pass",
			items: map[string]*domain.CatalogItem{
				"mycat/myitem": {
					Spec: domain.CatalogItemSpec{
						Versions: []domain.CatalogItemVersion{
							{Version: "1.0.0", Channels: []string{"stable"}, References: map[domain.CatalogItemArtifactType]string{}},
						},
					},
				},
			},
			spec: &domain.DeviceSpec{
				Applications: &[]domain.ApplicationProviderSpec{
					makeCatalogRefApp("mycat", "myitem", "1.0.0", lo.ToPtr("myapp"), nil),
				},
			},
			wantOK: true,
		},
		{
			name: "When version has no configSchema but defaults does and app violates it should return bad request",
			items: map[string]*domain.CatalogItem{
				"mycat/myitem": {
					Spec: domain.CatalogItemSpec{
						Defaults: &domain.CatalogItemConfigurable{ConfigSchema: requireSchema},
						Versions: []domain.CatalogItemVersion{
							{Version: "1.0.0", Channels: []string{"stable"}, References: map[domain.CatalogItemArtifactType]string{}},
						},
					},
				},
			},
			spec: &domain.DeviceSpec{
				Applications: &[]domain.ApplicationProviderSpec{
					makeCatalogRefApp("mycat", "myitem", "1.0.0", lo.ToPtr("myapp"), nil),
				},
			},
			wantOK:     false,
			wantSubstr: "configSchema",
		},
		{
			name:  "When app uses image provider it should skip configSchema validation",
			items: map[string]*domain.CatalogItem{},
			spec: func() *domain.DeviceSpec {
				container := domain.ContainerApplication{
					AppType: domain.AppTypeContainer,
					Name:    lo.ToPtr("myapp"),
				}
				_ = container.FromImageApplicationProviderSpec(domain.ImageApplicationProviderSpec{Image: "quay.io/test:latest"})
				var appSpec domain.ApplicationProviderSpec
				_ = appSpec.FromContainerApplication(container)
				return &domain.DeviceSpec{Applications: &[]domain.ApplicationProviderSpec{appSpec}}
			}(),
			wantOK: true,
		},
		{
			name:   "When spec has no applications it should pass",
			items:  map[string]*domain.CatalogItem{},
			spec:   &domain.DeviceSpec{},
			wantOK: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeCatalogStore{items: tt.items}
			status := ValidateCatalogItemRefs(context.Background(), uuid.New(), store, tt.spec)
			if tt.wantOK && status != domain.StatusOK() {
				t.Errorf("expected OK but got: %+v", status)
			}
			if !tt.wantOK && status == domain.StatusOK() {
				t.Error("expected error status but got OK")
			}
			if tt.wantSubstr != "" && status != domain.StatusOK() {
				if !contains(status.Message, tt.wantSubstr) {
					t.Errorf("expected message to contain %q, got %q", tt.wantSubstr, status.Message)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
