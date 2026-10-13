package pam_issuer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSpec(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	require.NotNil(t, spec.Paths)
	require.NotZero(t, spec.Paths.Len())
	// The server clears Components.SecuritySchemes before installing the
	// request validator, so Components must not be nil.
	require.NotNil(t, spec.Components)
}

func TestGetSpecIsCached(t *testing.T) {
	first, err := GetSpec()
	require.NoError(t, err)
	second, err := GetSpec()
	require.NoError(t, err)
	require.Same(t, first, second)
}
