package celruntime

import (
	"strings"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
)

func TestCachedExpressionStorage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		expression string
		wantError  bool
	}{
		{"When compilation succeeds it should cache a key with its own backing storage", "true", false},
		{"When compilation fails it should cache a key with its own backing storage", "unknown", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := New()
			require.NoError(t, err)
			buffer := tc.expression + strings.Repeat(" ", 1<<20)
			expression := buffer[:len(tc.expression)]
			_, err = engine.Compile(expression)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			item := engine.programs.Get(expression)
			require.NotNil(t, item)
			require.Equal(t, expression, item.Key())
			require.NotSame(t, unsafe.StringData(expression), unsafe.StringData(item.Key()))
		})
	}
}
