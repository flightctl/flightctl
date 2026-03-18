package transport

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testResource struct {
	Name   string `json:"name"`
	Value  int    `json:"value"`
	Nested struct {
		Field string `json:"field"`
	} `json:"nested"`
}

func TestStrictDecodeJSONBody(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid JSON with known fields",
			json:    `{"name": "test", "value": 42}`,
			wantErr: false,
		},
		{
			name:    "valid JSON with nested known fields",
			json:    `{"name": "test", "value": 1, "nested": {"field": "hello"}}`,
			wantErr: false,
		},
		{
			name:    "unknown top-level field",
			json:    `{"name": "test", "unknownField": "should fail"}`,
			wantErr: true,
			errMsg:  "unknownField",
		},
		{
			name:    "unknown nested field",
			json:    `{"name": "test", "nested": {"field": "ok", "extra": "bad"}}`,
			wantErr: true,
			errMsg:  "extra",
		},
		{
			name:    "misspelled field name",
			json:    `{"nme": "test", "value": 42}`,
			wantErr: true,
			errMsg:  "nme",
		},
		{
			name:    "empty JSON object is valid",
			json:    `{}`,
			wantErr: false,
		},
		{
			name:    "malformed JSON",
			json:    `{"name": }`,
			wantErr: true,
		},
		{
			name:    "multiple unknown fields",
			json:    `{"name": "test", "mountPath": "/foo", "bogus": true}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var resource testResource
			err := StrictDecodeJSONBody(strings.NewReader(tt.json), &resource)
			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestStrictDecodeJSONBody_PreservesValidData(t *testing.T) {
	json := `{"name": "mydevice", "value": 99, "nested": {"field": "data"}}`
	var resource testResource
	err := StrictDecodeJSONBody(strings.NewReader(json), &resource)
	require.NoError(t, err)
	assert.Equal(t, "mydevice", resource.Name)
	assert.Equal(t, 99, resource.Value)
	assert.Equal(t, "data", resource.Nested.Field)
}
