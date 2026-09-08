package config

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveVmLauncherImage(t *testing.T) {
	t.Parallel()

	rhel9 := "registry.example.com/virt-launcher-rhel9:v1"
	rhel10 := "registry.example.com/virt-launcher-rhel10:v1"
	pinned := "registry.example.com/virt-launcher:pinned"

	tests := []struct {
		name  string
		json  string
		osKey string
		want  string
	}{
		{
			name:  "When config is empty it should use the built-in default",
			json:  `{}`,
			osKey: "rhel-9",
			want:  DefaultVirtLauncherImage,
		},
		{
			name: "When launcherImage is set and no per-OS map it should use launcherImage",
			json: `{
				"worker": {"vmRender": {"launcherImage": "` + pinned + `"}}
			}`,
			osKey: "rhel-10",
			want:  pinned,
		},
		{
			name: "When launcherImages has the device OS key it should use that image",
			json: `{
				"worker": {"vmRender": {
					"launcherImages": {"rhel-9": "` + rhel9 + `", "rhel-10": "` + rhel10 + `"}
				}}
			}`,
			osKey: "rhel-10",
			want:  rhel10,
		},
		{
			name: "When the OS is not in launcherImages it should use launcherImage",
			json: `{
				"worker": {"vmRender": {
					"launcherImage": "` + pinned + `",
					"launcherImages": {"rhel-9": "` + rhel9 + `"}
				}}
			}`,
			osKey: "fedora-42",
			want:  pinned,
		},
		{
			name: "When osKey is empty it should ignore launcherImages",
			json: `{
				"worker": {"vmRender": {
					"launcherImage": "` + pinned + `",
					"launcherImages": {"rhel-9": "` + rhel9 + `"}
				}}
			}`,
			osKey: "",
			want:  pinned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			require.NoError(t, json.Unmarshal([]byte(tt.json), cfg))
			assert.Equal(t, tt.want, cfg.EffectiveVmLauncherImage(tt.osKey))
		})
	}
}

func TestEffectiveRenderTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		want time.Duration
	}{
		{
			name: "When config is empty it should use the built-in default",
			json: `{}`,
			want: DefaultRenderTimeout,
		},
		{
			name: "When renderTimeout is set it should use the configured value",
			json: `{
				"worker": {"vmRender": {"renderTimeout": "2m"}}
			}`,
			want: 2 * time.Minute,
		},
		{
			name: "When renderTimeout is zero it should fall back to the default",
			json: `{
				"worker": {"vmRender": {"renderTimeout": "0s"}}
			}`,
			want: DefaultRenderTimeout,
		},
		{
			name: "When worker is set without vmRender it should use the default",
			json: `{
				"worker": {}
			}`,
			want: DefaultRenderTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			require.NoError(t, json.Unmarshal([]byte(tt.json), cfg))
			assert.Equal(t, tt.want, cfg.EffectiveRenderTimeout())
		})
	}

	t.Run("When Config is nil it should use the default", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		assert.Equal(t, DefaultRenderTimeout, cfg.EffectiveRenderTimeout())
	})
}
