package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type testConfig struct {
	Endpoint string `json:"endpoint"`
	Port     int    `json:"port,omitempty"`
}

type validatingConfig struct {
	Server string `json:"server"`
}

func (c *validatingConfig) Validate() error {
	if c.Server == "" {
		return fmt.Errorf("server is required")
	}
	return nil
}

// 1. DecodeComponent with nil raw and non-nil defaults preserves defaults
func TestDecodeComponent_NilRawPreservesDefaults(t *testing.T) {
	cfg := &testConfig{Endpoint: "https://default.example.com", Port: 8080}
	err := DecodeComponent(nil, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "https://default.example.com", cfg.Endpoint)
	require.Equal(t, 8080, cfg.Port)
}

// 2. DecodeComponent decodes raw JSON over defaults, preserving unset defaults
func TestDecodeComponent_PartialOverridePreservesDefaults(t *testing.T) {
	cfg := &testConfig{Endpoint: "https://default.example.com", Port: 8080}
	raw := json.RawMessage(`{"endpoint":"https://override.example.com"}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "https://override.example.com", cfg.Endpoint)
	require.Equal(t, 8080, cfg.Port)
}

// 3. DecodeComponent strict rejects unknown fields
func TestDecodeComponent_RejectsUnknownFields(t *testing.T) {
	cfg := &testConfig{Endpoint: "default"}
	raw := json.RawMessage(`{"endpoint":"value","unknownField":"bad"}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.Error(t, err)
	require.Contains(t, err.Error(), "test.component")
}

// 4. DecodeComponent resolves ${env:NAME} references
func TestDecodeComponent_ResolvesEnvColon(t *testing.T) {
	t.Setenv("TEST_DC_ENDPOINT", "https://resolved.example.com")
	cfg := &testConfig{}
	raw := json.RawMessage(`{"endpoint":"${env:TEST_DC_ENDPOINT}"}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "https://resolved.example.com", cfg.Endpoint)
}

// 5. DecodeComponent resolves ${NAME} compatibility form
func TestDecodeComponent_ResolvesEnvCompat(t *testing.T) {
	t.Setenv("TEST_DC_COMPAT", "https://compat.example.com")
	cfg := &testConfig{}
	raw := json.RawMessage(`{"endpoint":"${TEST_DC_COMPAT}"}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "https://compat.example.com", cfg.Endpoint)
}

// 6. DecodeComponent errors on unset env var
func TestDecodeComponent_UnsetEnvVar(t *testing.T) {
	cfg := &testConfig{}
	raw := json.RawMessage(`{"endpoint":"${env:UNSET_VAR_DC_12345}"}`)
	err := DecodeComponent(raw, cfg, "sources.http/test")
	require.Error(t, err)
	require.Contains(t, err.Error(), "UNSET_VAR_DC_12345")
	require.Contains(t, err.Error(), "sources.http/test")
}

// 7. DecodeComponent keeps non-whole-string env references literal
func TestDecodeComponent_InlineEnvRefKeptLiteral(t *testing.T) {
	cfg := &testConfig{}
	raw := json.RawMessage(`{"endpoint":"prefix-${env:FOO}"}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "prefix-${env:FOO}", cfg.Endpoint)
}

// 8. DecodeComponent calls Validate() on typed config
func TestDecodeComponent_CallsValidate(t *testing.T) {
	cfg := &validatingConfig{}
	raw := json.RawMessage(`{"server":""}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.Error(t, err)
	require.Contains(t, err.Error(), "server is required")
}

// 9. DecodeComponent calls Validate() even with nil raw
func TestDecodeComponent_ValidateCalledOnNilRaw(t *testing.T) {
	cfg := &validatingConfig{}
	err := DecodeComponent(nil, cfg, "test.component")
	require.Error(t, err)
	require.Contains(t, err.Error(), "server is required")
}

// 10. DecodeComponent passes Validate() when config is valid
func TestDecodeComponent_ValidatePassesOnValid(t *testing.T) {
	cfg := &validatingConfig{}
	raw := json.RawMessage(`{"server":"https://example.com"}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "https://example.com", cfg.Server)
}

// 11. DecodeComponent does not modify the original raw JSON
func TestDecodeComponent_OriginalRawUnmodified(t *testing.T) {
	t.Setenv("TEST_DC_PRESERVE", "resolved-value")
	raw := json.RawMessage(`{"endpoint":"${env:TEST_DC_PRESERVE}"}`)
	original := make(json.RawMessage, len(raw))
	copy(original, raw)

	cfg := &testConfig{}
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "resolved-value", cfg.Endpoint)
	require.Equal(t, original, raw, "original raw JSON must not be modified")
}

// 12. DecodeComponent resolves env vars in nested objects
func TestDecodeComponent_NestedEnvResolution(t *testing.T) {
	type nestedConfig struct {
		Outer struct {
			Inner string `json:"inner"`
		} `json:"outer"`
	}
	t.Setenv("TEST_DC_NESTED", "deep-value")
	cfg := &nestedConfig{}
	raw := json.RawMessage(`{"outer":{"inner":"${env:TEST_DC_NESTED}"}}`)
	err := DecodeComponent(raw, cfg, "test.component")
	require.NoError(t, err)
	require.Equal(t, "deep-value", cfg.Outer.Inner)
}
