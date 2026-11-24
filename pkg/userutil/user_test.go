package userutil_test

import (
	"os/user"
	"strconv"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/userutil"
	"github.com/stretchr/testify/require"
)

func TestLookupExecutionUser(t *testing.T) {
	current, err := user.Current()
	require.NoError(t, err)

	for _, tc := range []struct {
		name string
		user v1beta1.Username
	}{
		{"When the execution user is omitted it should resolve the process account", v1beta1.CurrentProcessUsername},
		{"When the execution user is named it should resolve the named account", v1beta1.Username(current.Username)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			uid, gid, home, err := userutil.LookupExecutionUser(tc.user)
			require.NoError(t, err)
			require.Equal(t, current.Uid, strconv.FormatUint(uint64(uid), 10))
			require.Equal(t, current.Gid, strconv.FormatUint(uint64(gid), 10))
			require.Equal(t, current.HomeDir, home)
		})
	}

	t.Run("When the named account is missing it should return a lookup error", func(t *testing.T) {
		_, _, _, err := userutil.LookupExecutionUser("flightctl-nonexistent-test-account")
		require.Error(t, err)
	})
}
