package v1beta2

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSpec(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	require.Equal(t, "v1beta2", spec.Info.Version)
	require.Len(t, spec.Paths.Find("/devices/{name}").Operations(), 4)
	require.Len(t, spec.Paths.Find("/fleets/{name}").Operations(), 4)
	require.NotNil(t, spec.Components.Schemas["Device"].Value.Properties["spec"].Value)
}

func TestSpecYAMLMatchesEmbeddedDocument(t *testing.T) {
	require.Equal(t, specYAML, SpecYAML())
}
