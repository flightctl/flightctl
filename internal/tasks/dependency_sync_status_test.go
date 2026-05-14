package tasks

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestComputeStatus(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name               string
		refs               []model.DependencyRefWithSyncState
		informerConnected  *bool
		expectedCondStatus domain.ConditionStatus
		expectedReason     string
		expectedRefCount   int
	}{
		{
			name: "When all refs are synced it should produce DependenciesSynced=True/NoDrift",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: lo.ToPtr("abc"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
				{ResourceKey: "http:repo/config", RefType: "http", ConfigProviderName: "http-cfg", Fingerprint: lo.ToPtr("etag"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
			},
			informerConnected:  nil,
			expectedCondStatus: domain.ConditionStatusTrue,
			expectedReason:     "NoDrift",
			expectedRefCount:   2,
		},
		{
			name: "When a ref has ProbeFailed it should produce DependenciesSynced=False/ProbeFailed",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: lo.ToPtr("abc"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
				{ResourceKey: "http:repo/config", RefType: "http", ConfigProviderName: "http-cfg", ProbeStatus: lo.ToPtr("ProbeFailed"), ProbeMessage: lo.ToPtr("timeout"), LastCheckedAt: &now},
			},
			informerConnected:  nil,
			expectedCondStatus: domain.ConditionStatusFalse,
			expectedReason:     "ProbeFailed",
			expectedRefCount:   2,
		},
		{
			name: "When informerConnected=false and fleet has secret refs it should produce Unknown/SecretWatchDisconnected",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: lo.ToPtr("abc"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
				{ResourceKey: "secret:ns/name", RefType: "secret", ConfigProviderName: "secret-cfg", Fingerprint: lo.ToPtr("rv1"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
			},
			informerConnected:  lo.ToPtr(false),
			expectedCondStatus: domain.ConditionStatusUnknown,
			expectedReason:     "SecretWatchDisconnected",
			expectedRefCount:   2,
		},
		{
			name: "When informerConnected=true and secret refs are synced it should produce True/NoDrift",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "secret:ns/name", RefType: "secret", ConfigProviderName: "secret-cfg", Fingerprint: lo.ToPtr("rv1"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
			},
			informerConnected:  lo.ToPtr(true),
			expectedCondStatus: domain.ConditionStatusTrue,
			expectedReason:     "NoDrift",
			expectedRefCount:   1,
		},
		{
			name: "When informerConnected=nil and fleet has secret refs it should use sync_state as-is",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: lo.ToPtr("abc"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
				{ResourceKey: "secret:ns/name", RefType: "secret", ConfigProviderName: "secret-cfg", Fingerprint: lo.ToPtr("rv1"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
			},
			informerConnected:  nil,
			expectedCondStatus: domain.ConditionStatusTrue,
			expectedReason:     "NoDrift",
			expectedRefCount:   2,
		},
		{
			name: "When informerConnected=false and mixed git+secret refs it should only override secret refs",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: lo.ToPtr("abc"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
				{ResourceKey: "secret:ns/name", RefType: "secret", ConfigProviderName: "secret-cfg", Fingerprint: lo.ToPtr("rv1"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
			},
			informerConnected:  lo.ToPtr(false),
			expectedCondStatus: domain.ConditionStatusUnknown,
			expectedReason:     "SecretWatchDisconnected",
			expectedRefCount:   2,
		},
		{
			name: "When a ref has nil fingerprint (first seen) it should still be Synced with message",
			refs: []model.DependencyRefWithSyncState{
				{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: nil, ProbeStatus: nil},
			},
			informerConnected:  nil,
			expectedCondStatus: domain.ConditionStatusTrue,
			expectedReason:     "NoDrift",
			expectedRefCount:   1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			condition, syncStatus := computeStatus(tc.refs, tc.informerConnected, domain.ConditionTypeFleetDependenciesSynced)

			require.Equal(t, tc.expectedCondStatus, condition.Status, "condition status")
			require.Equal(t, tc.expectedReason, condition.Reason, "condition reason")
			require.NotNil(t, syncStatus)
			require.NotNil(t, syncStatus.ConfigRefs)
			require.Len(t, *syncStatus.ConfigRefs, tc.expectedRefCount)

			// Verify config ref details
			for i, cfgRef := range *syncStatus.ConfigRefs {
				require.Equal(t, tc.refs[i].ConfigProviderName, cfgRef.ConfigProviderName, "configProviderName for ref %d", i)
			}
		})
	}
}

func TestComputeStatus_PopulatesAllFields(t *testing.T) {
	earlier := time.Now().Add(-10 * time.Minute)
	later := time.Now()
	changeTime := time.Now().Add(-5 * time.Minute)

	refs := []model.DependencyRefWithSyncState{
		{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg",
			Fingerprint: lo.ToPtr("sha123"), ProbeStatus: lo.ToPtr("Synced"),
			LastCheckedAt: &later, LastChangeAt: &changeTime},
		{ResourceKey: "http:repo/config", RefType: "http", ConfigProviderName: "http-cfg",
			Fingerprint: lo.ToPtr(`"etag1"`), ProbeStatus: lo.ToPtr("ProbeFailed"),
			ProbeMessage: lo.ToPtr("timeout"), LastCheckedAt: &earlier},
	}

	_, syncStatus := computeStatus(refs, nil, domain.ConditionTypeFleetDependenciesSynced)

	cfgRefs := *syncStatus.ConfigRefs
	require.Len(t, cfgRefs, 2)

	require.Equal(t, lo.ToPtr("sha123"), cfgRefs[0].Fingerprint)
	require.Equal(t, &changeTime, cfgRefs[0].LastUpdatedAt)
	require.Equal(t, &later, cfgRefs[0].LastProbeTime)

	require.Equal(t, lo.ToPtr(`"etag1"`), cfgRefs[1].Fingerprint)
	require.Nil(t, cfgRefs[1].LastUpdatedAt)
	require.Equal(t, &earlier, cfgRefs[1].LastProbeTime)

	require.Equal(t, &later, syncStatus.LastProbeTime)
	require.Equal(t, &later, syncStatus.LastSuccessfulProbeTime)
}

func TestComputeStatus_SecretOverridePreservesGitStatus(t *testing.T) {
	t.Run("When informerConnected=false git refs retain their probe status", func(t *testing.T) {
		now := time.Now()
		refs := []model.DependencyRefWithSyncState{
			{ResourceKey: "git:repo/main", RefType: "git", ConfigProviderName: "git-cfg", Fingerprint: lo.ToPtr("abc"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
			{ResourceKey: "secret:ns/name", RefType: "secret", ConfigProviderName: "secret-cfg", Fingerprint: lo.ToPtr("rv1"), ProbeStatus: lo.ToPtr("Synced"), LastCheckedAt: &now},
		}

		_, syncStatus := computeStatus(refs, lo.ToPtr(false), domain.ConditionTypeDeviceDependenciesSynced)

		require.NotNil(t, syncStatus.ConfigRefs)
		cfgRefs := *syncStatus.ConfigRefs
		require.Len(t, cfgRefs, 2)

		// Git ref keeps its Synced status
		require.Equal(t, domain.DependencySyncConfigRefStatusSynced, cfgRefs[0].Status)
		// Secret ref is overridden to SecretWatchDisconnected
		require.Equal(t, domain.DependencySyncConfigRefStatusSecretWatchDisconnected, cfgRefs[1].Status)
	})
}
