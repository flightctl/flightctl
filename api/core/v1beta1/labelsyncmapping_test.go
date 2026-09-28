package v1beta1

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
)

func validLabelSyncMapping() LabelSyncMapping {
	name := "architecture"
	return LabelSyncMapping{
		Metadata: ObjectMeta{Name: &name},
		Spec: LabelSyncMappingSpec{
			ResourceType: LabelSyncMappingSpecResourceTypeDevice,
			Key:          lo.ToPtr("architecture"),
			Expression:   "device.status.systemInfo.architecture",
		},
	}
}

func TestLabelSyncMappingValidate(t *testing.T) {
	tests := []struct {
		name    string
		mapping LabelSyncMapping
		valid   bool
	}{
		{name: "When the mapping is valid it should accept", mapping: validLabelSyncMapping(), valid: true},
		{name: "When the resource type is unsupported it should reject", mapping: func() LabelSyncMapping { m := validLabelSyncMapping(); m.Spec.ResourceType = "Fleet"; return m }()},
		{name: "When a qualified key is provided it should accept", mapping: func() LabelSyncMapping {
			m := validLabelSyncMapping()
			m.Spec.Key = lo.ToPtr("example.com/key")
			return m
		}(), valid: true},
		{name: "When the key is omitted it should accept map mode", mapping: func() LabelSyncMapping { m := validLabelSyncMapping(); m.Spec.Key = nil; return m }(), valid: true},
		{name: "When the key is empty it should reject", mapping: func() LabelSyncMapping { m := validLabelSyncMapping(); m.Spec.Key = lo.ToPtr(""); return m }()},
		{name: "When the key contains spaces it should reject", mapping: func() LabelSyncMapping { m := validLabelSyncMapping(); m.Spec.Key = lo.ToPtr("bad key"); return m }()},
		{name: "When the expression is empty it should reject", mapping: func() LabelSyncMapping { m := validLabelSyncMapping(); m.Spec.Expression = ""; return m }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.mapping.Validate()
			if tt.valid {
				assert.Empty(t, errs)
				return
			}
			assert.NotEmpty(t, errs)
		})
	}
}

func TestLabelSyncMappingValidateUpdate(t *testing.T) {
	current := validLabelSyncMapping()
	updated := validLabelSyncMapping()
	updated.Spec.Key = lo.ToPtr("cpu-architecture")
	assert.Empty(t, current.ValidateUpdate(&updated))

	updated.Spec.ResourceType = "Fleet"
	assert.NotEmpty(t, current.ValidateUpdate(&updated))
}
