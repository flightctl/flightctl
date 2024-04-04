package model

import (
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
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
	rs := getTestRS(util.StrToPtr("old"), util.Int64ToPtr(1))

	// hash changed - should run
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_gen_outdated(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(util.StrToPtr("hash"), util.Int64ToPtr(0))

	// Observed generation not up do date - should run sync
	rs.Status.Data.ObservedCommit = util.StrToPtr("hash")
	rs.Status.Data.ObservedGeneration = util.Int64ToPtr(0)
	require.True(rs.NeedsSyncToHash("hash"))
}
func TestNeedSyncToHash_no_sync_condition(t *testing.T) {
	require := require.New(t)

	// Generation and commit fine, but no sync condition
	rs := getTestRS(util.StrToPtr("hash"), util.Int64ToPtr(1))
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_bad_condition(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(util.StrToPtr("hash"), util.Int64ToPtr(1))

	// Sync condition false - should run sync
	rs.AddSyncedCondition(fmt.Errorf("Some error"))
	require.True(rs.NeedsSyncToHash("hash"))
}

func TestNeedSyncToHash_in_sync(t *testing.T) {
	require := require.New(t)
	rs := getTestRS(util.StrToPtr("hash"), util.Int64ToPtr(1))

	// No need to run. all up to date
	rs.AddSyncedCondition(nil)
	require.False(rs.NeedsSyncToHash("hash"))
}

func getTestRS(obsHash *string, obsGen *int64) ResourceSync {
	rs := ResourceSync{
		Resource: Resource{
			Generation: util.Int64ToPtr(1),
		},
		Spec: &JSONField[api.ResourceSyncSpec]{
			Data: api.ResourceSyncSpec{
				Repository: util.StrToPtr("demoRepo"),
				Path:       util.StrToPtr("/examples"),
			},
		},
	}
	if obsHash != nil || obsGen != nil {
		if obsHash == nil {
			obsHash = util.StrToPtr("not-observed-hash")
		}
		if obsGen == nil {
			obsGen = util.Int64ToPtr(-1)
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
