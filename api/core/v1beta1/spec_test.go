package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSpec(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	require.NotNil(t, spec.Paths)
	require.NotZero(t, spec.Paths.Len())
	require.NotNil(t, spec.Components)
	require.NotEmpty(t, spec.Components.Schemas)
}

func TestGetSpecIsCached(t *testing.T) {
	first, err := GetSpec()
	require.NoError(t, err)
	second, err := GetSpec()
	require.NoError(t, err)
	require.Same(t, first, second)
}

func TestSpecYAMLMatchesEmbeddedDocument(t *testing.T) {
	require.NotEmpty(t, SpecYAML())
	require.Equal(t, specYAML, SpecYAML())
}
