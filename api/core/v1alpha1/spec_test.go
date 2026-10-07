package v1alpha1

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGetSpec also covers resolution of the external references this
// specification makes into core v1beta1: an unregistered or misspelled
// reference path fails the load rather than surfacing at server startup.
func TestGetSpec(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	require.NotNil(t, spec.Paths)
	require.NotZero(t, spec.Paths.Len())
}

func TestGetSpecIsCached(t *testing.T) {
	first, err := GetSpec()
	require.NoError(t, err)
	second, err := GetSpec()
	require.NoError(t, err)
	require.Same(t, first, second)
}
