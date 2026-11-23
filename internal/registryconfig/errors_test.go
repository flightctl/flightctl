package registryconfig_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/distribution/reference"
	"github.com/flightctl/flightctl/internal/domain"
	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestSelectionErrorCauses(t *testing.T) {
	snapshot, err := rc.NewSnapshot(orgID, nil)
	require.NoError(t, err)

	t.Run("When reference parsing fails it should preserve the parser cause", func(t *testing.T) {
		_, err := snapshot.Select([]rc.PullNeed{need("quay.io/team//app")})
		require.ErrorIs(t, err, reference.ErrReferenceInvalidFormat)
		require.ErrorContains(t, err, "invalid pull need 0")
	})

	t.Run("When alias normalization makes a path too long it should preserve the parser cause safely", func(t *testing.T) {
		_, err := snapshot.Select([]rc.PullNeed{need("registry-1.docker.io/private-token" + strings.Repeat("a", 240))})
		require.ErrorIs(t, err, reference.ErrNameTooLong)
		require.NotContains(t, err.Error(), "private-token")
	})

	t.Run("When a parser error contains input it should retain the cause without displaying secrets", func(t *testing.T) {
		_, err := snapshot.Select([]rc.PullNeed{need("quay.io/private-token/UPPER")})
		require.Error(t, err)
		require.NotContains(t, err.Error(), "private-token")
		cause := err
		for errors.Unwrap(cause) != nil {
			cause = errors.Unwrap(cause)
		}
		require.ErrorContains(t, cause, "private-token")
	})
}

func TestSnapshotErrorCauses(t *testing.T) {
	for _, tt := range []struct {
		name string
		spec domain.OciRepoSpec
	}{
		{"registry", domain.OciRepoSpec{Registry: "private-token!"}},
		{"scope", domain.OciRepoSpec{Registry: "quay.io", Namespace: lo.ToPtr("private-token//app")}},
	} {
		t.Run("When "+tt.name+" parsing fails it should wrap the parser cause safely", func(t *testing.T) {
			_, err := rc.NewSnapshot(orgID, []rc.Candidate{candidate(t, "invalid", tt.spec)})
			require.ErrorIs(t, err, reference.ErrReferenceInvalidFormat)
			require.ErrorContains(t, err, `Repository "invalid"`)
			require.NotContains(t, err.Error(), "private-token")
		})
	}

	for _, raw := range []string{
		`{"type":42,"private-token":true}`,
		`{"type":"oci","registry":42,"private-token":true}`,
	} {
		t.Run("When Repository decoding fails it should preserve the typed JSON cause", func(t *testing.T) {
			c := credential(t, "invalid", "quay.io", "")
			require.NoError(t, json.Unmarshal([]byte(raw), &c.Repository.Spec))
			_, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
			var cause *json.UnmarshalTypeError
			require.ErrorAs(t, err, &cause)
			require.NotContains(t, err.Error(), "private-token")
		})
	}
}
