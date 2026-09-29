package v1beta1

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestSecureString_PrintfS(t *testing.T) {
	s := SecureString("actual-secret")
	require.Equal(t, "[REDACTED]", fmt.Sprintf("%s", s))
}

func TestSecureString_PrintfQ(t *testing.T) {
	s := SecureString("actual-secret")
	result := fmt.Sprintf("%q", s)
	require.NotContains(t, result, "actual-secret")
	require.Contains(t, result, "REDACTED")
}

func TestSecureString_PrintfV(t *testing.T) {
	s := SecureString("actual-secret")
	require.Equal(t, "[REDACTED]", fmt.Sprintf("%v", s))
}

func TestSecureString_PrintfSharpV(t *testing.T) {
	s := SecureString("actual-secret")
	result := fmt.Sprintf("%#v", s)
	require.NotContains(t, result, "actual-secret")
	require.Contains(t, result, "REDACTED")
}

func TestSecureString_JSONMarshal(t *testing.T) {
	s := SecureString("actual-secret")
	data, err := json.Marshal(s)
	require.NoError(t, err)
	require.Equal(t, `"[REDACTED]"`, string(data))
	require.NotContains(t, string(data), "actual-secret")
}

func TestSecureString_YAMLMarshal(t *testing.T) {
	s := SecureString("actual-secret")
	data, err := yaml.Marshal(s)
	require.NoError(t, err)
	require.NotContains(t, string(data), "actual-secret")
	require.Contains(t, string(data), "REDACTED")
}

func TestSecureString_Value(t *testing.T) {
	s := SecureString("actual-secret")
	require.Equal(t, "actual-secret", s.Value())
}

func TestSecureString_StructMarshal(t *testing.T) {
	type wrapper struct {
		Token SecureString `json:"token" yaml:"token"`
	}
	w := wrapper{Token: "actual-secret"}

	jsonData, err := json.Marshal(w)
	require.NoError(t, err)
	require.NotContains(t, string(jsonData), "actual-secret")

	yamlData, err := yaml.Marshal(w)
	require.NoError(t, err)
	require.NotContains(t, string(yamlData), "actual-secret")
}
