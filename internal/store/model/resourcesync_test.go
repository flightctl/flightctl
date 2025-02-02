package model

import (
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestNeedsSyncToHash_no_status(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(nil, nil)

	// no status - should run sync
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_hash_changed(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(lo.ToPtr("old"), lo.ToPtr(int64(1)))

	// hash changed - should run
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_gen_outdated(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(lo.ToPtr("hash"), lo.ToPtr(int64(0)))

	// Observed generation not up do date - should run sync
	rs.Status.Data.ObservedCommit = lo.ToPtr("hash")
	rs.Status.Data.ObservedGeneration = lo.ToPtr(int64(0))
	require.True(rs.NeedsSyncToHash("hash"))
}
func TestNeedSyncToHash_no_sync_condition(t *testing.T) {
	require := require.New(t)

	// Generation and commit fine, but no sync condition
	rs := getTestRS(lo.ToPtr("hash"), lo.ToPtr(int64(1)))
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_bad_condition(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(lo.ToPtr("hash"), lo.ToPtr(int64(1)))

	// Sync condition false - should run sync
	rs.AddSyncedCondition(fmt.Errorf("Some error"))
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_in_sync(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(lo.ToPtr("hash"), lo.ToPtr(int64(1)))

	// No need to run. all up to date
	rs.AddSyncedCondition(nil)
	require.False(rs.NeedsSyncToHash("hash"))
}

func getTestRS(obsHash *string, obsGen *int64) ResourceSync {
	rs := ResourceSync{
		Resource: Resource{
			Generation: lo.ToPtr(int64(1)),
		},
		Spec: &JSONField[api.ResourceSyncSpec]{
			Data: api.ResourceSyncSpec{
				Repository: "demoRepo",
				Path:       "/examples",
			},
		},
	}
	if obsHash != nil || obsGen != nil {
		if obsHash == nil {
			obsHash = lo.ToPtr("not-observed-hash")
		}
		if obsGen == nil {
			obsGen = lo.ToPtr(int64(-1))
		}
		rs.Status = &JSONField[api.ResourceSyncStatus]{
			Data: api.ResourceSyncStatus{
				ObservedCommit:     obsHash,
				ObservedGeneration: obsGen,
			},
		}
	}
	return rs
}
