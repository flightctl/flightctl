package common

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestValidateAuthoredConfigPatch(t *testing.T) {
	tests := []struct {
		path, value string
		reject      bool
	}{
		{"/configResources", `[]`, true},
		{"/configResources/0", `{}`, true},
		{"/os/pullConfig", `null`, true},
		{"/os", `{"image":"quay.io/example/os:v1","pullConfig":{}}`, true},
		{"/applications/0/pullConfig", `{}`, true},
		{"/applications/0", `{"pullConfig":null}`, true},
		{"/applications", `[{"pullConfig":{}}]`, true},
		{"", `{"configResources":[]}`, true},
		{"", `{"os":{"pullConfig":null}}`, true},
		{"", `{"applications":[{"pullConfig":{}}]}`, true},
		{"/os/image", `"quay.io/example/os:v1"`, false},
		{"/applications/0/values", `{"pullConfig":{},"configResources":[]}`, false},
		{"/applications/0", `{"values":{"pullConfig":{}}}`, false},
		{"/config", `[{"inline":[{"path":"/etc/config","content":"pullConfig"}]}]`, false},
		{"/applications/0/pullConfig~1nested", `{}`, false},
	}
	for _, fleet := range []bool{false, true} {
		root := "/spec"
		if fleet {
			root = "/spec/template/spec"
		}
		for _, tt := range tests {
			t.Run("When patching "+root+tt.path+" it should validate field presence", func(t *testing.T) {
				var value any
				require.NoError(t, json.Unmarshal([]byte(tt.value), &value))
				patch := domain.PatchRequest{{Op: "add", Path: root + tt.path, Value: value}}
				errs := ValidateAuthoredConfigPatch(patch, fleet)
				require.Equal(t, tt.reject, len(errs) > 0)
			})
		}
		t.Run("When replacing an ancestor it should inspect the embedded spec", func(t *testing.T) {
			value := map[string]any{"spec": map[string]any{"configResources": nil}}
			if fleet {
				value = map[string]any{"spec": map[string]any{"template": value}}
			}
			require.NotEmpty(t, ValidateAuthoredConfigPatch(domain.PatchRequest{{Op: "replace", Path: "", Value: value}}, fleet))
		})
		t.Run("When adding and removing a prohibited field it should reject", func(t *testing.T) {
			require.NotEmpty(t, ValidateAuthoredConfigPatch(domain.PatchRequest{{Op: "add", Path: root + "/configResources", Value: []any{}}, {Op: "remove", Path: root + "/configResources"}}, fleet))
		})
	}
}
