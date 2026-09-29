package labelsyncmapping

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCELExpressionValidator(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)
	validator, err := NewCELExpressionValidator(evaluator)
	require.NoError(t, err)

	testCases := []struct {
		name                  string
		mapping               domain.LabelSyncMapping
		expectedErrorContains string
	}{
		{
			name: "When a scalar expression is used with a destination key it should validate",
			mapping: domain.LabelSyncMapping{Spec: domain.LabelSyncMappingSpec{
				Key:        lo.ToPtr("architecture"),
				Expression: "status.systemInfo.architecture",
			}},
		},
		{
			name: "When a map expression is used without a destination key it should validate",
			mapping: domain.LabelSyncMapping{Spec: domain.LabelSyncMappingSpec{
				Expression: `{"site": "east"}`,
			}},
		},
		{
			name: "When a scalar expression is used without a destination key it should reject the mismatch",
			mapping: domain.LabelSyncMapping{Spec: domain.LabelSyncMappingSpec{
				Expression: `"east"`,
			}},
			expectedErrorContains: "produces scalar, expected map",
		},
		{
			name: "When a map expression is used with a destination key it should reject the mismatch",
			mapping: domain.LabelSyncMapping{Spec: domain.LabelSyncMappingSpec{
				Key:        lo.ToPtr("architecture"),
				Expression: `{"site": "east"}`,
			}},
			expectedErrorContains: "produces map, expected scalar",
		},
		{
			name: "When an expression has invalid CEL syntax it should return the syntax error",
			mapping: domain.LabelSyncMapping{Spec: domain.LabelSyncMappingSpec{
				Key:        lo.ToPtr("architecture"),
				Expression: "status..systemInfo",
			}},
			expectedErrorContains: "parsing CEL expression",
		},
		{
			name: "When an expression references an undeclared root it should return the checking error",
			mapping: domain.LabelSyncMapping{Spec: domain.LabelSyncMappingSpec{
				Key:        lo.ToPtr("architecture"),
				Expression: "device.status.systemInfo.architecture",
			}},
			expectedErrorContains: "checking CEL expression",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateLabelSyncMapping(context.Background(), tt.mapping)
			if tt.expectedErrorContains == "" {
				assert.NoError(t, err)
				return
			}

			require.ErrorContains(t, err, tt.expectedErrorContains)
		})
	}
}

func TestNewCELExpressionValidator(t *testing.T) {
	_, err := NewCELExpressionValidator(nil)
	require.ErrorContains(t, err, "CEL evaluator is required")
}
