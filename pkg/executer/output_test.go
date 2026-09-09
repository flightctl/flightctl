package executer

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecuteWithBoundedOutputFromDir(t *testing.T) {
	require := require.New(t)
	e := NewCommonExecuter()

	t.Run("When output exceeds the combined limit it should truncate capture", func(t *testing.T) {
		stdout, stderr, code := e.ExecuteWithBoundedOutputFromDir(
			t.Context(),
			"",
			"sh",
			[]string{"-c", "dd if=/dev/zero bs=1 count=5000 2>/dev/null | tr '\\0' 'A'; dd if=/dev/zero bs=1 count=5000 2>/dev/null | tr '\\0' 'B'"},
			4096,
		)
		require.Equal(0, code)
		require.LessOrEqual(len(stdout)+len(stderr), 4096)
	})

	t.Run("When limit is zero it should capture full output", func(t *testing.T) {
		payload := strings.Repeat("x", 5000)
		stdout, stderr, code := e.ExecuteWithBoundedOutputFromDir(
			t.Context(),
			"",
			"sh",
			[]string{"-c", "printf '" + payload + "'"},
			0,
		)
		require.Equal(0, code)
		require.Equal(payload, stdout)
		require.Empty(stderr)
	})
}
