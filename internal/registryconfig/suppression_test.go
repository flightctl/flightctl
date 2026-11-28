package registryconfig_test

import (
	"testing"

	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/stretchr/testify/require"
)

func TestAuthoredAuthenticationSelectionIsolation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		suppressed []bool
	}{
		{"When authored authentication is first it should retain selection for other needs", []bool{true, false}},
		{"When authored authentication is last it should suppress its supplier", []bool{false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot, err := rc.NewSnapshot(orgID, []rc.Candidate{credential(t, "supplier", "quay.io", "team")})
			require.NoError(t, err)
			needs := make([]rc.PullNeed, len(tc.suppressed))
			for i, suppressed := range tc.suppressed {
				needs[i] = need("quay.io/team/app:v1")
				needs[i].SuppressAuthentication = suppressed
			}
			results, err := snapshot.Select(needs)
			require.NoError(t, err)
			require.Len(t, results, len(needs))
			for i, result := range results {
				if tc.suppressed[i] {
					require.Equal(t, rc.OutcomeAllowed, result.Outcome)
					require.Nil(t, result.Supplier)
				} else {
					require.Equal(t, rc.OutcomeSelected, result.Outcome)
					require.NotNil(t, result.Supplier)
				}
			}
		})
	}
}
