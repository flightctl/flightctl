package deviceidprocessor

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/otel-collector/cnauthenticator"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pmetric"
)

func TestProcessor_ProcessMetrics(t *testing.T) {
	tests := []struct {
		name           string
		deviceID       string
		orgID          string
		expectDeviceID bool
		expectOrgID    bool
	}{
		{
			name:           "no device id or org id",
			deviceID:       "",
			orgID:          "",
			expectDeviceID: false,
			expectOrgID:    false,
		},
		{
			name:           "with device id only",
			deviceID:       "test-device",
			orgID:          "",
			expectDeviceID: true,
			expectOrgID:    false,
		},
		{
			name:           "with org id only",
			deviceID:       "",
			orgID:          "test-org",
			expectDeviceID: false,
			expectOrgID:    true,
		},
		{
			name:           "with both device id and org id",
			deviceID:       "test-device",
			orgID:          "test-org",
			expectDeviceID: true,
			expectOrgID:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create test metrics
			md := pmetric.NewMetrics()
			rm := md.ResourceMetrics().AppendEmpty()
			sm := rm.ScopeMetrics().AppendEmpty()
			metric := sm.Metrics().AppendEmpty()
			metric.SetName("test_metric")
			gauge := metric.SetEmptyGauge()
			dp := gauge.DataPoints().AppendEmpty()
			dp.SetDoubleValue(1.0)

			// Create processor
			p := &deviceIdProcessor{}

			// Create context with device_id and org_id using the correct ContextKey types
			ctx := context.Background()
			if tt.deviceID != "" {
				ctx = context.WithValue(ctx, cnauthenticator.DeviceIDKey, tt.deviceID)
			}
			if tt.orgID != "" {
				ctx = context.WithValue(ctx, cnauthenticator.OrgIDKey, tt.orgID)
			}

			// Process metrics
			result, err := p.processMetrics(ctx, md)
			require.NoError(t, err)

			// Check results - verify resource attributes only
			for i := 0; i < result.ResourceMetrics().Len(); i++ {
				rm := result.ResourceMetrics().At(i)

				// Check resource attributes
				resourceAttrs := rm.Resource().Attributes()
				if tt.expectDeviceID {
					if val, exists := resourceAttrs.Get("device_id"); exists {
						assert.Equal(t, tt.deviceID, val.Str())
					} else {
						t.Errorf("device_id resource attribute not found when expected")
					}
				} else {
					_, exists := resourceAttrs.Get("device_id")
					assert.False(t, exists, "device_id resource attribute should not be present")
				}

				if tt.expectOrgID {
					if val, exists := resourceAttrs.Get("org_id"); exists {
						assert.Equal(t, tt.orgID, val.Str())
					} else {
						t.Errorf("org_id resource attribute not found when expected")
					}
				} else {
					_, exists := resourceAttrs.Get("org_id")
					assert.False(t, exists, "org_id resource attribute should not be present")
				}

				// Note: Metric attributes are handled by the transform processor,
				// so we don't test for them here
			}
		})
	}
}
