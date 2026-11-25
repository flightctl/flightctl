package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetSpec(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	require.NotNil(t, spec.Paths)
	require.NotZero(t, spec.Paths.Len())
	require.NotNil(t, spec.Components)
	require.NotEmpty(t, spec.Components.Schemas)
}

func TestGetSpecIsCached(t *testing.T) {
	first, err := GetSpec()
	require.NoError(t, err)
	second, err := GetSpec()
	require.NoError(t, err)
	require.Same(t, first, second)
}

func TestSpecYAMLMatchesEmbeddedDocument(t *testing.T) {
	require.NotEmpty(t, SpecYAML())
	require.Equal(t, specYAML, SpecYAML())
}

func TestUpdateScheduleJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		json string
	}{
		{
			name: "When a legacy recurring schedule is round-tripped it should preserve the JSON contract",
			json: `{"updateSchedule":{"at":"0 4 * * 1","startGraceDuration":"1h","timeZone":"UTC"}}`,
		},
		{
			name: "When a one-time schedule is round-tripped it should preserve the JSON contract",
			json: `{"downloadSchedule":{"timeZone":"Europe/Jerusalem","oneTimeWindows":[{"date":"2026-11-15","startTime":"22:00","startGraceDuration":"90m"}]}}`,
		},
		{
			name: "When recurring and one-time schedules are combined it should preserve the JSON contract",
			json: `{"updateSchedule":{"at":"0 4 * * 1","startGraceDuration":"1h","timeZone":"UTC","oneTimeWindows":[{"date":"2026-11-16","startTime":"04:00","startGraceDuration":"2h"}]}}`,
		},
		{
			name: "When download and update schedules are independent it should preserve the JSON contract",
			json: `{"downloadSchedule":{"timeZone":"Europe/Jerusalem","oneTimeWindows":[{"date":"2026-11-15","startTime":"22:00","startGraceDuration":"90m"}]},"updateSchedule":{"timeZone":"UTC","oneTimeWindows":[{"date":"2026-11-16","startTime":"04:00","startGraceDuration":"2h"}]}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var policy DeviceUpdatePolicySpec
			require.NoError(t, json.Unmarshal([]byte(tt.json), &policy))

			encoded, err := json.Marshal(policy)
			require.NoError(t, err)
			require.JSONEq(t, tt.json, string(encoded))
		})
	}
}

func TestUpdateScheduleSchemaRequiresCompleteRecurringFields(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	schema := spec.Components.Schemas["UpdateSchedule"].Value

	tests := []struct {
		name    string
		value   map[string]any
		wantErr bool
	}{
		{
			name: "When recurring fields are complete it should satisfy the schema",
			value: map[string]any{
				"at":                 "0 4 * * 1",
				"startGraceDuration": "1h",
			},
		},
		{
			name: "When only one-time windows are provided it should satisfy the schema",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"startTime":          "22:00",
					"startGraceDuration": "90m",
				}},
			},
		},
		{
			name: "When recurring and one-time fields are provided it should satisfy the schema",
			value: map[string]any{
				"at":                 "0 4 * * 1",
				"startGraceDuration": "1h",
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"startTime":          "22:00",
					"startGraceDuration": "90m",
				}},
			},
		},
		{
			name: "When at is provided without a grace duration it should fail schema validation",
			value: map[string]any{
				"at": "0 4 * * 1",
			},
			wantErr: true,
		},
		{
			name: "When a grace duration is provided without at it should fail schema validation",
			value: map[string]any{
				"startGraceDuration": "1h",
			},
			wantErr: true,
		},
		{
			name: "When one-time windows are empty it should fail schema validation",
			value: map[string]any{
				"oneTimeWindows": []any{},
			},
			wantErr: true,
		},
		{
			name: "When a one-time window omits its date it should fail schema validation",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"startTime":          "22:00",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
		{
			name: "When a one-time window omits its time it should fail schema validation",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
		{
			name: "When a one-time window omits its grace duration it should fail schema validation",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":      "2026-11-15",
					"startTime": "22:00",
				}},
			},
			wantErr: true,
		},
		{
			name: "When a one-time window has an invalid date it should fail schema validation",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-1-15",
					"startTime":          "22:00",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
		{
			name: "When a one-time window has an invalid time it should fail schema validation",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"startTime":          "24:00",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := schema.VisitJSON(tt.value)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
