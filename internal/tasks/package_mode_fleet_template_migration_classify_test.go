package tasks

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestOsImageEligibleForGuard(t *testing.T) {
	tests := []struct {
		name  string
		image string
		want  bool
	}{
		{name: "When the image is a plain literal it should be eligible", image: "quay.io/org/os:v2", want: true},
		{name: "When the image has no tag it should be eligible", image: "quay.io/org/os", want: true},
		{name: "When the image uses a registry port it should be eligible", image: "quay.io:5000/org/os:v2", want: true},
		{
			name:  "When the image uses a digest it should be eligible",
			image: "quay.io/org/os@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			want:  true,
		},
		{
			name:  "When only the tag is parameterized it should be eligible",
			image: "quay.io/org/os:{{ .metadata.labels.version }}",
			want:  true,
		},
		{
			name:  "When several substitutions appear it should be eligible",
			image: "{{ .metadata.labels.registry }}/org/os:{{ .metadata.labels.version }}",
			want:  true,
		},

		{name: "When the image is empty it should not be eligible", image: "", want: false},
		{name: "When the image is only whitespace it should not be eligible", image: "   ", want: false},
		{
			name:  "When the whole value is guarded by the image-mode helper it should not be eligible",
			image: "{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}",
			want:  false,
		},
		{
			name:  "When the guard uses trim markers it should not be eligible",
			image: "{{- if deviceHasImageMode -}}quay.io/org/os:v2{{- end -}}",
			want:  false,
		},
		{
			name:  "When the guard has no inner spaces it should not be eligible",
			image: "{{if deviceHasImageMode}}quay.io/org/os:v2{{end}}",
			want:  false,
		},
		{
			name:  "When the whole value is guarded by an unrelated condition it should not be eligible",
			image: "{{ if .metadata.labels.tier }}quay.io/org/os:v2{{ end }}",
			want:  false,
		},
		{
			name:  "When the whole value is guarded by a hand-written os mode expression it should not be eligible",
			image: `{{ if eq (getOrDefault .metadata.labels "feature.flightctl.io/os.mode" "image") "image" }}quay.io/org/os:v2{{ end }}`,
			want:  false,
		},
		{
			name:  "When the wrapper has an else branch it should not be eligible",
			image: "{{ if deviceHasImageMode }}quay.io/org/a:v1{{ else }}quay.io/org/b:v2{{ end }}",
			want:  false,
		},
		{
			name:  "When the wrapper has an else if branch it should not be eligible",
			image: "{{ if deviceHasImageMode }}quay.io/org/a:v1{{ else if .metadata.labels.tier }}quay.io/org/b:v2{{ end }}",
			want:  false,
		},
		{
			name:  "When conditionals are nested it should not be eligible",
			image: "{{ if .metadata.labels.tier }}{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}{{ end }}",
			want:  false,
		},
		{
			name:  "When two sibling conditionals appear it should not be eligible",
			image: "{{ if deviceHasImageMode }}quay.io/org/a:v1{{ end }}{{ if deviceHasImageMode }}quay.io/org/b:v2{{ end }}",
			want:  false,
		},
		{
			name:  "When a conditional appears only in the tag it should not be eligible",
			image: "quay.io/org/os:{{ if deviceHasImageMode }}v2{{ end }}",
			want:  false,
		},
		{
			name:  "When the value uses a with block it should not be eligible",
			image: "{{ with .metadata.labels }}quay.io/org/os:v2{{ end }}",
			want:  false,
		},
		{
			name:  "When the value does not parse it should not be eligible",
			image: "{{ if deviceHasImageMode }}quay.io/org/os:v2",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, osImageEligibleForGuard(tt.image))
		})
	}
}

func TestClassifyFleetForMigration(t *testing.T) {
	withOwner := func(fleet domain.Fleet, owner string) domain.Fleet {
		fleet.Metadata.Owner = lo.ToPtr(owner)
		return fleet
	}
	withCatalogRef := func(fleet domain.Fleet) domain.Fleet {
		if fleet.Spec.Template.Spec.Os == nil {
			fleet.Spec.Template.Spec.Os = &domain.DeviceOsSpec{}
		}
		fleet.Spec.Template.Spec.Os.CatalogItemRef = &domain.CatalogItemRefSpec{
			Catalog: "example-catalog",
			Item:    "example-item",
			Version: "1.0.0",
		}
		return fleet
	}

	tests := []struct {
		name      string
		fleet     domain.Fleet
		want      fleetMigrationDisposition
		wantOwner string
	}{
		{
			name:  "When a fleet carries a literal image it should be transformed",
			fleet: fleetWithImage("a", "quay.io/org/os:v2"),
			want:  dispositionTransform,
		},
		{
			name:  "When a fleet has no OS section it should be skipped",
			fleet: fleetWithImage("a", ""),
			want:  dispositionSkipNoImage,
		},
		{
			name:  "When a fleet names only a catalog item it should be skipped",
			fleet: withCatalogRef(fleetWithImage("a", "")),
			want:  dispositionSkipNoImage,
		},
		{
			name:  "When a fleet carries a catalog item alongside an eligible image it should still be transformed",
			fleet: withCatalogRef(fleetWithImage("a", "quay.io/org/os:v2")),
			want:  dispositionTransform,
		},
		{
			name:  "When a fleet is already guarded it should be skipped",
			fleet: fleetWithImage("a", "{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}"),
			want:  dispositionSkipConditionalImage,
		},
		{
			name:  "When a fleet is guarded by an unrelated condition it should be skipped",
			fleet: fleetWithImage("a", "{{ if .metadata.labels.tier }}quay.io/org/os:v2{{ end }}"),
			want:  dispositionSkipConditionalImage,
		},
		{
			name:      "When a fleet is owned by a resource sync it should be deferred to that owner",
			fleet:     withOwner(fleetWithImage("a", "quay.io/org/os:v2"), "ResourceSync/from-git"),
			want:      dispositionDeferResourceSync,
			wantOwner: "ResourceSync/from-git",
		},
		{
			name:  "When a fleet is owned by another kind it should still be transformed",
			fleet: withOwner(fleetWithImage("a", "quay.io/org/os:v2"), "Fleet/parent"),
			want:  dispositionTransform,
		},
		{
			name:  "When the owner string is malformed it should still be transformed",
			fleet: withOwner(fleetWithImage("a", "quay.io/org/os:v2"), "not-an-owner"),
			want:  dispositionTransform,
		},
		{
			name:  "When a resource sync owns a fleet with nothing to migrate it should be skipped rather than deferred",
			fleet: withOwner(fleetWithImage("a", ""), "ResourceSync/from-git"),
			want:  dispositionSkipNoImage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, owner := classifyFleetForMigration(&tt.fleet)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantOwner, owner)
		})
	}
}
