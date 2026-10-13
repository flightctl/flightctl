package catalogcollector

import (
	"strings"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
)

func ptr(s string) *string { return &s }

func validCatalog(name string) apiv1alpha1.Catalog {
	return apiv1alpha1.Catalog{
		Metadata: apiv1beta1.ObjectMeta{Name: ptr(name)},
	}
}

func validCatalogItem(catalog, name string) apiv1alpha1.CatalogItem {
	return apiv1alpha1.CatalogItem{
		Metadata: apiv1alpha1.CatalogItemMeta{
			Catalog: catalog,
			Name:    ptr(name),
		},
		Spec: apiv1alpha1.CatalogItemSpec{
			Type: apiv1alpha1.CatalogItemTypeData,
			Artifacts: []apiv1alpha1.CatalogItemArtifact{
				{Type: apiv1alpha1.CatalogItemArtifactTypeContainer, Uri: "example.com/repo"},
			},
			Versions: []apiv1alpha1.CatalogItemVersion{
				{
					Version:  "1.0.0",
					Channels: []string{"stable"},
					References: map[apiv1alpha1.CatalogItemArtifactType]string{
						apiv1alpha1.CatalogItemArtifactTypeContainer: "sha256:abc123",
					},
				},
			},
		},
	}
}

func TestValidateSnapshot(t *testing.T) {
	cases := []struct {
		name      string
		snapshot  *CatalogSnapshot
		wantErr   bool
		errSubstr string
	}{
		{
			name:      "When snapshot is nil it should return error",
			snapshot:  nil,
			wantErr:   true,
			errSubstr: "nil",
		},
		{
			name:      "When revision is empty it should return error",
			snapshot:  &CatalogSnapshot{Revision: ""},
			wantErr:   true,
			errSubstr: "revision is empty",
		},
		{
			name: "When snapshot is valid with no items it should succeed",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				Catalogs: []apiv1alpha1.Catalog{validCatalog("test-catalog")},
			},
			wantErr: false,
		},
		{
			name: "When snapshot is valid with catalogs and items it should succeed",
			snapshot: &CatalogSnapshot{
				Revision:     "abc123",
				Catalogs:     []apiv1alpha1.Catalog{validCatalog("my-catalog")},
				CatalogItems: []apiv1alpha1.CatalogItem{validCatalogItem("my-catalog", "item-a")},
			},
			wantErr: false,
		},
		{
			name: "When catalog has empty name it should return error",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				Catalogs: []apiv1alpha1.Catalog{
					{Metadata: apiv1beta1.ObjectMeta{}},
				},
			},
			wantErr:   true,
			errSubstr: "name is required",
		},
		{
			name: "When catalog has nil name it should return error",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				Catalogs: []apiv1alpha1.Catalog{
					{Metadata: apiv1beta1.ObjectMeta{Name: nil}},
				},
			},
			wantErr:   true,
			errSubstr: "name is required",
		},
		{
			name: "When catalogs have duplicate names it should return error",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				Catalogs: []apiv1alpha1.Catalog{
					validCatalog("dup"),
					validCatalog("dup"),
				},
			},
			wantErr:   true,
			errSubstr: "duplicate name",
		},
		{
			name: "When catalog item has empty catalog it should return error",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				CatalogItems: []apiv1alpha1.CatalogItem{
					{
						Metadata: apiv1alpha1.CatalogItemMeta{Catalog: "", Name: ptr("item-a")},
						Spec: apiv1alpha1.CatalogItemSpec{
							Type:      apiv1alpha1.CatalogItemTypeData,
							Artifacts: []apiv1alpha1.CatalogItemArtifact{{Type: apiv1alpha1.CatalogItemArtifactTypeContainer, Uri: "r"}},
							Versions:  []apiv1alpha1.CatalogItemVersion{{Version: "1.0.0", Channels: []string{"stable"}, References: map[apiv1alpha1.CatalogItemArtifactType]string{apiv1alpha1.CatalogItemArtifactTypeContainer: "d"}}},
						},
					},
				},
			},
			wantErr:   true,
			errSubstr: "catalog and name identity is required",
		},
		{
			name: "When catalog item has nil name it should return error",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				CatalogItems: []apiv1alpha1.CatalogItem{
					{
						Metadata: apiv1alpha1.CatalogItemMeta{Catalog: "cat", Name: nil},
						Spec: apiv1alpha1.CatalogItemSpec{
							Type:      apiv1alpha1.CatalogItemTypeData,
							Artifacts: []apiv1alpha1.CatalogItemArtifact{{Type: apiv1alpha1.CatalogItemArtifactTypeContainer, Uri: "r"}},
							Versions:  []apiv1alpha1.CatalogItemVersion{{Version: "1.0.0", Channels: []string{"stable"}, References: map[apiv1alpha1.CatalogItemArtifactType]string{apiv1alpha1.CatalogItemArtifactTypeContainer: "d"}}},
						},
					},
				},
			},
			wantErr:   true,
			errSubstr: "catalog and name identity is required",
		},
		{
			name: "When catalog items have duplicate identities it should return error",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				CatalogItems: []apiv1alpha1.CatalogItem{
					validCatalogItem("cat", "item-a"),
					validCatalogItem("cat", "item-a"),
				},
			},
			wantErr:   true,
			errSubstr: "duplicate identity",
		},
		{
			name: "When catalog items differ by catalog it should succeed",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
				CatalogItems: []apiv1alpha1.CatalogItem{
					validCatalogItem("cat-a", "item"),
					validCatalogItem("cat-b", "item"),
				},
			},
			wantErr: false,
		},
		{
			name: "When snapshot has only revision and empty slices it should succeed",
			snapshot: &CatalogSnapshot{
				Revision: "abc123",
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateSnapshot(tc.snapshot)
			if (err != nil) != tc.wantErr {
				t.Errorf("ValidateSnapshot() error = %v, wantErr %v", err, tc.wantErr)
			}
			if tc.errSubstr != "" && err != nil {
				if !strings.Contains(err.Error(), tc.errSubstr) {
					t.Errorf("ValidateSnapshot() error = %q, want it to contain %q", err.Error(), tc.errSubstr)
				}
			}
		})
	}
}
