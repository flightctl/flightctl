package appinspection_test

import (
	"testing"

	"github.com/flightctl/flightctl/internal/store/appinspection"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/require"
)

func TestNormalizeReference(t *testing.T) {
	withDigest := "registry.example/ns/image@" + digest.FromString("image").String()
	for _, tt := range []struct {
		name, raw, normalized, registry string
		port                            uint16
		path                            model.StringArray
	}{
		{"short name", "alpine", "docker.io/library/alpine", "docker.io", 0, model.StringArray{"library", "alpine"}},
		{"explicit port and tag", "registry.example:5443/ns/tool/image:stable", "registry.example:5443/ns/tool/image:stable", "registry.example", 5443, model.StringArray{"ns", "tool", "image"}},
		{"digest", withDigest, withDigest, "registry.example", 0, model.StringArray{"ns", "image"}},
		{"uppercase host", "REGISTRY.EXAMPLE/ns/image", "registry.example/ns/image", "registry.example", 0, model.StringArray{"ns", "image"}},
	} {
		t.Run("When given a "+tt.name+" it should persist the canonical reference facts", func(t *testing.T) {
			ref, err := appinspection.NormalizeReference(appinspection.ReferenceInput{
				Reference: tt.raw, Role: model.InspectionRoleVolume, Mechanism: model.InspectionPullContainer,
			})
			require.NoError(t, err)
			require.Equal(t, tt.normalized, ref.Reference)
			require.Equal(t, tt.registry, ref.Registry)
			require.Equal(t, tt.port, ref.Port)
			require.Equal(t, tt.path, ref.Path)
			require.Equal(t, model.InspectionRoleVolume, ref.Role)
			require.Equal(t, model.InspectionPullContainer, ref.Mechanism)
		})
	}
	for _, tt := range []struct {
		name string
		ref  appinspection.ReferenceInput
	}{
		{"acquisition URL", appinspection.ReferenceInput{Reference: "https://user:secret-value@registry.example/image", Role: model.InspectionRoleWorkload, Mechanism: model.InspectionPullContainer}},
		{"invalid port", appinspection.ReferenceInput{Reference: "registry.example:65536/image", Role: model.InspectionRoleWorkload, Mechanism: model.InspectionPullContainer}},
		{"invalid role", appinspection.ReferenceInput{Reference: "alpine", Role: "secret-value", Mechanism: model.InspectionPullContainer}},
		{"invalid mechanism", appinspection.ReferenceInput{Reference: "alpine", Role: model.InspectionRoleWorkload, Mechanism: "secret-value"}},
	} {
		t.Run("When given an "+tt.name+" it should fail without exposing input", func(t *testing.T) {
			_, err := appinspection.NormalizeReference(tt.ref)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "secret-value")
		})
	}
}
