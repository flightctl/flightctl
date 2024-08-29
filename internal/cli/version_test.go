package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVersion(t *testing.T) {
	require := require.New(t)

	cmd := NewCmdVersion()
	err := cmd.Execute()
	require.NoError(err)
}
