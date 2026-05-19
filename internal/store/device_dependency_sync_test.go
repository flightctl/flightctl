package store

import (
	"testing"
	"time"

	domain "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildDependencySyncStatus(t *testing.T) {
	t.Run("When fingerprints is empty it should return nil", func(t *testing.T) {
		result := buildDependencySyncStatus(nil, nil)
		assert.Nil(t, result)

		result = buildDependencySyncStatus(nil, []domain.DependencySyncConfigRefStatus{})
		assert.Nil(t, result)
	})

	t.Run("When no prior state exists it should set lastUpdatedAt to now for all entries", func(t *testing.T) {
		before := time.Now()
		fps := []domain.DependencySyncConfigRefStatus{
			{ConfigProviderName: "git-config", Fingerprint: lo.ToPtr("abc123")},
			{ConfigProviderName: "http-config", Fingerprint: lo.ToPtr("sha256:def456")},
		}

		result := buildDependencySyncStatus(nil, fps)
		require.NotNil(t, result)
		require.NotNil(t, result.Data.DependencySync)
		require.NotNil(t, result.Data.DependencySync.ConfigRefs)

		refs := *result.Data.DependencySync.ConfigRefs
		require.Len(t, refs, 2)

		for _, ref := range refs {
			require.NotNil(t, ref.LastUpdatedAt, "lastUpdatedAt should be set for %s", ref.ConfigProviderName)
			assert.False(t, ref.LastUpdatedAt.Before(before), "lastUpdatedAt should be >= test start time")
		}

		assert.Equal(t, "git-config", refs[0].ConfigProviderName)
		assert.Equal(t, "abc123", *refs[0].Fingerprint)
		assert.Equal(t, "http-config", refs[1].ConfigProviderName)
		assert.Equal(t, "sha256:def456", *refs[1].Fingerprint)
	})

	t.Run("When fingerprint is unchanged it should preserve the original lastUpdatedAt", func(t *testing.T) {
		oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		existingRefs := []domain.DependencySyncConfigRefStatus{
			{
				ConfigProviderName: "git-config",
				Fingerprint:        lo.ToPtr("abc123"),
				LastUpdatedAt:      &oldTime,
			},
		}
		existing := model.MakeJSONField(model.ServiceConditions{
			DependencySync: &domain.DependencySyncStatus{
				ConfigRefs: &existingRefs,
			},
		})

		fps := []domain.DependencySyncConfigRefStatus{
			{ConfigProviderName: "git-config", Fingerprint: lo.ToPtr("abc123")},
		}

		result := buildDependencySyncStatus(existing, fps)
		require.NotNil(t, result)

		refs := *result.Data.DependencySync.ConfigRefs
		require.Len(t, refs, 1)
		require.NotNil(t, refs[0].LastUpdatedAt)
		assert.Equal(t, oldTime, *refs[0].LastUpdatedAt, "lastUpdatedAt should be preserved when fingerprint unchanged")
	})

	t.Run("When fingerprint changes it should update lastUpdatedAt to now", func(t *testing.T) {
		oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		existingRefs := []domain.DependencySyncConfigRefStatus{
			{
				ConfigProviderName: "git-config",
				Fingerprint:        lo.ToPtr("oldsha"),
				LastUpdatedAt:      &oldTime,
			},
		}
		existing := model.MakeJSONField(model.ServiceConditions{
			DependencySync: &domain.DependencySyncStatus{
				ConfigRefs: &existingRefs,
			},
		})

		before := time.Now()
		fps := []domain.DependencySyncConfigRefStatus{
			{ConfigProviderName: "git-config", Fingerprint: lo.ToPtr("newsha")},
		}

		result := buildDependencySyncStatus(existing, fps)
		require.NotNil(t, result)

		refs := *result.Data.DependencySync.ConfigRefs
		require.Len(t, refs, 1)
		require.NotNil(t, refs[0].LastUpdatedAt)
		assert.False(t, refs[0].LastUpdatedAt.Before(before), "lastUpdatedAt should be updated to current time")
		assert.Equal(t, "newsha", *refs[0].Fingerprint)
	})

	t.Run("When a new config ref is added it should get a fresh lastUpdatedAt", func(t *testing.T) {
		oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		existingRefs := []domain.DependencySyncConfigRefStatus{
			{
				ConfigProviderName: "git-config",
				Fingerprint:        lo.ToPtr("abc123"),
				LastUpdatedAt:      &oldTime,
			},
		}
		existing := model.MakeJSONField(model.ServiceConditions{
			DependencySync: &domain.DependencySyncStatus{
				ConfigRefs: &existingRefs,
			},
		})

		before := time.Now()
		fps := []domain.DependencySyncConfigRefStatus{
			{ConfigProviderName: "git-config", Fingerprint: lo.ToPtr("abc123")},
			{ConfigProviderName: "http-config", Fingerprint: lo.ToPtr("newfingerprint")},
		}

		result := buildDependencySyncStatus(existing, fps)
		require.NotNil(t, result)

		refs := *result.Data.DependencySync.ConfigRefs
		require.Len(t, refs, 2)

		assert.Equal(t, oldTime, *refs[0].LastUpdatedAt, "existing unchanged ref keeps old timestamp")
		assert.False(t, refs[1].LastUpdatedAt.Before(before), "new ref gets fresh timestamp")
	})

	t.Run("When existing conditions are present it should preserve them", func(t *testing.T) {
		conditions := []domain.Condition{
			{Type: "Ready", Status: "True", Reason: "AllGood"},
		}
		existing := model.MakeJSONField(model.ServiceConditions{
			Conditions: &conditions,
		})

		fps := []domain.DependencySyncConfigRefStatus{
			{ConfigProviderName: "git-config", Fingerprint: lo.ToPtr("sha1")},
		}

		result := buildDependencySyncStatus(existing, fps)
		require.NotNil(t, result)
		require.NotNil(t, result.Data.Conditions)
		assert.Len(t, *result.Data.Conditions, 1)
		assert.Equal(t, domain.ConditionType("Ready"), (*result.Data.Conditions)[0].Type)
	})

	t.Run("When fingerprint is nil it should set lastUpdatedAt to now", func(t *testing.T) {
		oldTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
		existingRefs := []domain.DependencySyncConfigRefStatus{
			{
				ConfigProviderName: "inline-config",
				Fingerprint:        nil,
				LastUpdatedAt:      &oldTime,
			},
		}
		existing := model.MakeJSONField(model.ServiceConditions{
			DependencySync: &domain.DependencySyncStatus{
				ConfigRefs: &existingRefs,
			},
		})

		before := time.Now()
		fps := []domain.DependencySyncConfigRefStatus{
			{ConfigProviderName: "inline-config", Fingerprint: nil},
		}

		result := buildDependencySyncStatus(existing, fps)
		require.NotNil(t, result)
		refs := *result.Data.DependencySync.ConfigRefs
		require.Len(t, refs, 1)
		assert.False(t, refs[0].LastUpdatedAt.Before(before), "nil fingerprints always get fresh timestamp")
	})
}
