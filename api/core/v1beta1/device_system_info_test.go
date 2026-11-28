package v1beta1

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeviceSystemInfoConfigResourcesSupportedJSON(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value *bool
	}{
		{"When support is absent it should omit the field", nil},
		{"When support is false it should preserve false", lo.ToPtr(false)},
		{"When support is true it should preserve true", lo.ToPtr(true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := DeviceSystemInfo{ConfigResourcesSupported: tc.value}
			data, err := json.Marshal(info)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(data, &fields))
			if tc.value == nil {
				assert.NotContains(t, fields, "configResourcesSupported")
			} else {
				assert.JSONEq(t, strconv.FormatBool(*tc.value), string(fields["configResourcesSupported"]))
			}
			var decoded DeviceSystemInfo
			require.NoError(t, json.Unmarshal(data, &decoded))
			assert.Equal(t, tc.value, decoded.ConfigResourcesSupported)
		})
	}
}
