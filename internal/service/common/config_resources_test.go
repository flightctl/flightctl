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
		{"", `{"configResources":[]}`, true},
		{"", `{"configResources":null}`, true},
		{"/os/image", `"quay.io/example/os:v1"`, false},
		{"/applications/0/values", `{"configResources":[]}`, false},
		{"/applications/0", `{"values":{"configResources":{}}}`, false},
		{"/config", `[{"inline":[{"path":"/etc/config","content":"configResources"}]}]`, false},
		{"/configResources~1nested", `{}`, false},
		{"/configResources~0nested", `{}`, false},
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
		t.Run("When a patch value cannot be encoded it should reject", func(t *testing.T) {
			errs := ValidateAuthoredConfigPatch(domain.PatchRequest{{Op: "add", Path: root + "/configResources", Value: json.RawMessage(`{`)}}, fleet)
			require.Len(t, errs, 1)
			require.EqualError(t, errs[0], "patch[0] has an invalid value")
		})
		t.Run("When adding and removing a prohibited field it should reject", func(t *testing.T) {
			require.NotEmpty(t, ValidateAuthoredConfigPatch(domain.PatchRequest{{Op: "add", Path: root + "/configResources", Value: []any{}}, {Op: "remove", Path: root + "/configResources"}}, fleet))
		})
	}
}
