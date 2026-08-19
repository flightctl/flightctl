package config

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffectiveVmLauncherImage(t *testing.T) {
	t.Parallel()

	rhel9 := "registry.example.com/virt-launcher-rhel9:v1"
	rhel10 := "registry.example.com/virt-launcher-rhel10:v1"
	pinned := "registry.example.com/virt-launcher:pinned"

	tests := []struct {
		name    string
		json    string
		osMajor string
		want    string
	}{
		{
			name:    "When config is empty it should use the built-in default",
			json:    `{}`,
			osMajor: "9",
			want:    DefaultVirtLauncherImage,
		},
		{
			name: "When launcherImage is set and no per-OS map it should use launcherImage",
			json: `{
				"worker": {"vmRender": {"launcherImage": "` + pinned + `"}}
			}`,
			osMajor: "10",
			want:    pinned,
		},
		{
			name: "When launcherImages has the device major it should use that image",
			json: `{
				"worker": {"vmRender": {
					"launcherImages": {"9": "` + rhel9 + `", "10": "` + rhel10 + `"}
				}}
			}`,
			osMajor: "10",
			want:    rhel10,
		},
		{
			name: "When launcherImages has no entry for the major it should use launcherImage",
			json: `{
				"worker": {"vmRender": {
					"launcherImage": "` + pinned + `",
					"launcherImages": {"9": "` + rhel9 + `"}
				}}
			}`,
			osMajor: "10",
			want:    pinned,
		},
		{
			name: "When osMajor is empty it should ignore launcherImages",
			json: `{
				"worker": {"vmRender": {
					"launcherImage": "` + pinned + `",
					"launcherImages": {"9": "` + rhel9 + `"}
				}}
			}`,
			osMajor: "",
			want:    pinned,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{}
			require.NoError(t, json.Unmarshal([]byte(tt.json), cfg))
			assert.Equal(t, tt.want, cfg.EffectiveVmLauncherImage(tt.osMajor))
		})
	}
}
