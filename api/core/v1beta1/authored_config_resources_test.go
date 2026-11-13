package v1beta1

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAuthoredConfigResources(t *testing.T) {
	for _, fleet := range []bool{false, true} {
		t.Run("When an authored collection is present it should reject even when empty", func(t *testing.T) {
			spec := DeviceSpec{ConfigResources: &[]ConfigResource{}}
			require.ErrorContains(t, errors.Join(spec.Validate(fleet)...), "configResources")
			require.Empty(t, (DeviceSpec{}).Validate(fleet))
		})
	}
}
