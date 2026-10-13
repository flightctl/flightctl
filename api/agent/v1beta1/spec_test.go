package v1beta1

import (
	"fmt"
	"testing"

	core "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

func TestAgentStatusDeltaOutcomes(t *testing.T) {
	spec, err := GetSpec()
	require.NoError(t, err)
	statusSchema := spec.Paths.Find("/devices/{name}/status").Put.RequestBody.Value.
		Content["application/json"].Schema.Value.Properties["status"].Value

	statusTypes := []struct {
		name    string
		schema  *openapi3.Schema
		payload map[string]any
	}{
		{
			name:   "application",
			schema: statusSchema.Properties["applications"].Value.Items.Value,
			payload: map[string]any{
				"name": "app", "ready": "1/1", "restarts": 0,
				"status": "Running", "embedded": false, "appType": "container",
			},
		},
		{
			name:    "OS",
			schema:  statusSchema.Properties["os"].Value,
			payload: map[string]any{"image": "quay.io/example/os:latest", "imageDigest": "sha256:abc"},
		},
	}
	outcomes := []struct {
		outcome core.DeviceDeltaApplyOutcomeType
		valid   bool
	}{
		{core.DeviceDeltaApplyOutcomeNotUsed, true},
		{core.DeviceDeltaApplyOutcomeNotRequired, true},
		{core.DeviceDeltaApplyOutcomeApplied, true},
		{core.DeviceDeltaApplyOutcomeFallback, true},
		{core.DeviceDeltaApplyOutcomePartial, true},
		{"Unknown", false},
	}
	for _, statusType := range statusTypes {
		for _, outcome := range outcomes {
			expectation := "accept it"
			if !outcome.valid {
				expectation = "reject it"
			}
			t.Run(fmt.Sprintf("When %s reports %s it should %s", statusType.name, outcome.outcome, expectation), func(t *testing.T) {
				statusType.payload["lastDelta"] = map[string]any{"outcome": string(outcome.outcome)}
				err := statusType.schema.VisitJSON(statusType.payload)
				if outcome.valid {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			})
		}
	}
}
