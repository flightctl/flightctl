package main

import (
	"os"
	"path/filepath"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/template"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestPodmanAuthProviderPrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		field string
		want  string
	}{
		{name: "When prompt is omitted it should default to select_account", want: "select_account"},
		{name: "When prompt is null it should default to select_account", field: "prompt: null", want: "select_account"},
		{name: "When prompt is empty it should preserve the opt-out", field: `prompt: ""`},
		{name: "When prompt has extra spaces it should preserve them", field: `prompt: " login  consent "`, want: " login  consent "},
		{name: "When prompt has YAML punctuation it should preserve it", field: `prompt: 'login "custom" #extension'`, want: `login "custom" #extension`},
	}
	for _, service := range []string{"flightctl-api", "flightctl-imagebuilder-api", "flightctl-remote-access", "flightctl-alertmanager-proxy"} {
		t.Run(service, func(t *testing.T) {
			t.Parallel()
			for _, pamEnabled := range []bool{false, true} {
				for _, tt := range tests {
					name := tt.name
					if pamEnabled {
						name += " with PAM"
					}
					t.Run(name, func(t *testing.T) {
						t.Parallel()
						contents, err := os.ReadFile("../../deploy/podman/service-config.yaml")
						require.NoError(t, err)
						var data map[string]interface{}
						require.NoError(t, yaml.Unmarshal(contents, &data))
						auth := data["global"].(map[string]interface{})["auth"].(map[string]interface{})
						auth["pamOidcIssuer"].(map[string]interface{})["enabled"] = pamEnabled
						oidc := auth["oidc"].(map[string]interface{})
						delete(oidc, "prompt")
						require.NoError(t, yaml.Unmarshal([]byte(tt.field), &oidc))
						output := filepath.Join(t.TempDir(), "config.yaml")
						input := filepath.Join("../../deploy/podman", service, service+"-config/config.yaml.template")
						require.NoError(t, template.RenderWithData(data, input, output, template.WithFuncMap(api.GetGoTemplateFuncMap())))
						rendered, err := os.ReadFile(output)
						require.NoError(t, err)
						var cfg struct {
							Auth struct {
								OIDC api.OIDCProviderSpec `json:"oidc"`
							} `json:"auth"`
						}
						require.NoError(t, yaml.Unmarshal(rendered, &cfg))
						require.NotNil(t, cfg.Auth.OIDC.Prompt)
						require.Equal(t, tt.want, *cfg.Auth.OIDC.Prompt)
					})
				}
			}
		})
	}
}
