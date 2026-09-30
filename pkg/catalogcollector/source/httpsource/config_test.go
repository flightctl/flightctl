package httpsource

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCreateDefaultConfig(t *testing.T) {
	f := NewFactory()
	cfg := f.CreateDefaultConfig()
	c, ok := cfg.(*Config)
	require.True(t, ok)
	require.Equal(t, "127.0.0.1:8080", c.ListenAddress)
	require.Equal(t, "/v1/snapshots", c.Path)
}
