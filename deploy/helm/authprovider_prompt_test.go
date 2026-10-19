package helm

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestHelmAuthProviderPrompt(t *testing.T) {
	t.Parallel()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm is required to render the chart")
	}
	// Render the shared auth helper with the real chart values and schema.
	chart := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(chart, "templates"), 0700))
	for _, file := range []string{"Chart.yaml", "values.yaml", "values.schema.json", "templates/_helpers.tpl"} {
		contents, err := os.ReadFile(filepath.Join("flightctl", file))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(chart, file), contents, 0600))
	}
	require.NoError(t, os.WriteFile(filepath.Join(chart, "templates/auth.yaml"), []byte(`{{ include "flightctl.authConfig" . }}`), 0600))
	tests := []struct {
		name    string
		field   string
		want    string
		invalid bool
	}{
		{name: "When prompt is omitted it should default to select_account", want: "select_account"},
		{name: "When prompt is null it should default to select_account", field: "prompt: null", want: "select_account"},
		{name: "When prompt is empty it should preserve the opt-out", field: `prompt: ""`},
		{name: "When prompt has extra spaces it should preserve them", field: `prompt: " login  consent "`, want: " login  consent "},
		{name: "When prompt has YAML punctuation it should preserve it", field: `prompt: 'login "custom" #extension'`, want: `login "custom" #extension`},
		{name: "When prompt is not a string it should reject it", field: "prompt: 123", invalid: true},
		{name: "When prompt has a tab it should reject it", field: `prompt: "login\tconsent"`, invalid: true},
		{name: "When prompt combines none with another value it should reject it", field: `prompt: "none login"`, invalid: true},
		{name: "When prompt is none it should preserve it", field: `prompt: "none"`, want: "none"},
		{name: "When prompt repeats none with extra spaces it should preserve it", field: `prompt: " none  none "`, want: " none  none "},
		{name: "When prompt contains uppercase NONE it should preserve it", field: `prompt: "NONE login"`, want: "NONE login"},
		{name: "When prompt contains none as a substring it should preserve it", field: `prompt: "nonetheless login"`, want: "nonetheless login"},
		{name: "When prompt contains a none extension it should preserve it", field: `prompt: "none-extension login"`, want: "none-extension login"},
		{name: "When prompt combines another value with trailing none it should reject it", field: `prompt: "login none"`, invalid: true},
		{name: "When prompt combines another value with middle none it should reject it", field: `prompt: " login  none consent "`, invalid: true},
		{name: "When prompt combines none with an extension it should reject it", field: `prompt: "none none-extension"`, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			values := filepath.Join(t.TempDir(), "values.yaml")
			require.NoError(t, os.WriteFile(values, []byte("global:\n  auth:\n    type: oidc\n    oidc:\n      issuer: https://example.com\n      "+tt.field+"\n"), 0600))
			output, err := exec.Command(helm, "template", "test", chart, "--values", values).CombinedOutput()
			if tt.invalid {
				require.Error(t, err, "%s", output)
				require.Contains(t, string(output), "prompt")
				return
			}
			require.NoError(t, err, "%s", output)
			var cfg struct {
				Auth struct {
					OIDC api.OIDCProviderSpec `json:"oidc"`
				} `json:"auth"`
			}
			require.NoError(t, yaml.Unmarshal(output, &cfg))
			require.NotNil(t, cfg.Auth.OIDC.Prompt)
			require.Equal(t, tt.want, *cfg.Auth.OIDC.Prompt)
		})
	}
}
