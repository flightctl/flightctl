package executer

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecuterUserHandling(t *testing.T) {
	t.Run("running with no options", func(t *testing.T) {
		e := NewCommonExecuter()
		_, _, code := e.ExecuteWithContext(t.Context(), "env")
		require.Equal(t, 0, code)
	})

	t.Run("setting homedir", func(t *testing.T) {
		e := NewCommonExecuter(WithHomeDir("/tmp"))
		out, _, code := e.ExecuteWithContext(t.Context(), "env")
		require.Equal(t, 0, code)
		require.NotContains(t, out, "HOME=/tmp")
	})

	t.Run("running as user", func(t *testing.T) {
		e := NewCommonExecuter(WithUIDAndGID(8484, 8484), WithHomeDir("/tmp"))
		_, _, code := e.ExecuteWithContext(t.Context(), "env")
		require.Equal(t, -1, code)
	})
}
