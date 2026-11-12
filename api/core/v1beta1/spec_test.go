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
			name: "legacy recurring schedule",
			json: `{"updateSchedule":{"at":"0 4 * * 1","startGraceDuration":"1h","timeZone":"UTC"}}`,
		},
		{
			name: "one-time schedule",
			json: `{"downloadSchedule":{"timeZone":"Europe/Jerusalem","oneTimeWindows":[{"date":"2026-11-15","at":"22:00","startGraceDuration":"90m"}]}}`,
		},
		{
			name: "recurring and one-time schedule",
			json: `{"updateSchedule":{"at":"0 4 * * 1","startGraceDuration":"1h","timeZone":"UTC","oneTimeWindows":[{"date":"2026-11-16","at":"04:00","startGraceDuration":"2h"}]}}`,
		},
		{
			name: "independent download and update schedules",
			json: `{"downloadSchedule":{"timeZone":"Europe/Jerusalem","oneTimeWindows":[{"date":"2026-11-15","at":"22:00","startGraceDuration":"90m"}]},"updateSchedule":{"timeZone":"UTC","oneTimeWindows":[{"date":"2026-11-16","at":"04:00","startGraceDuration":"2h"}]}}`,
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
			name: "recurring schedule",
			value: map[string]any{
				"at":                 "0 4 * * 1",
				"startGraceDuration": "1h",
			},
		},
		{
			name: "one-time schedule",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"at":                 "22:00",
					"startGraceDuration": "90m",
				}},
			},
		},
		{
			name: "combined schedule",
			value: map[string]any{
				"at":                 "0 4 * * 1",
				"startGraceDuration": "1h",
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"at":                 "22:00",
					"startGraceDuration": "90m",
				}},
			},
		},
		{
			name: "at without grace duration",
			value: map[string]any{
				"at": "0 4 * * 1",
			},
			wantErr: true,
		},
		{
			name: "grace duration without at",
			value: map[string]any{
				"startGraceDuration": "1h",
			},
			wantErr: true,
		},
		{
			name: "empty one-time windows",
			value: map[string]any{
				"oneTimeWindows": []any{},
			},
			wantErr: true,
		},
		{
			name: "one-time window without date",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"at":                 "22:00",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
		{
			name: "one-time window without time",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
		{
			name: "one-time window without grace duration",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date": "2026-11-15",
					"at":   "22:00",
				}},
			},
			wantErr: true,
		},
		{
			name: "one-time window with invalid date",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-1-15",
					"at":                 "22:00",
					"startGraceDuration": "90m",
				}},
			},
			wantErr: true,
		},
		{
			name: "one-time window with invalid time",
			value: map[string]any{
				"oneTimeWindows": []any{map[string]any{
					"date":               "2026-11-15",
					"at":                 "24:00",
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
