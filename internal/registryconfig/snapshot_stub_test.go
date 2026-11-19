package registryconfig_test

import (
	"testing"

	"github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSnapshotStub(t *testing.T) {
	for _, suppressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "When authentication is required it should deliver nothing", true: "When authentication is authored it should deliver nothing"}[suppressed], func(t *testing.T) {
			needs := []registryconfig.PullNeed{{Reference: "quay.io/example/app:latest", Consumer: registryconfig.ConsumerContainer, Destination: "containers:", SuppressAuthentication: suppressed}}
			before := needs[0]
			snapshot, err := registryconfig.NewSnapshot(uuid.New(), nil)
			require.NoError(t, err)
			result, err := snapshot.Compose(needs)
			require.NoError(t, err)
			require.Empty(t, result.Selections)
			require.Empty(t, result.Endpoints)
			require.Equal(t, before, needs[0])
		})
	}
}
