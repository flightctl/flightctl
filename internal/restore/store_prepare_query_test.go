package restore

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildPrepareDevicesAfterRestoreQuery(t *testing.T) {
	t.Parallel()

	base := DeviceAwaitingReconnectPrepareParams{
		AnnotationKey: "awaitingReconnect",
		SummaryStatus: "AwaitingReconnect",
		SummaryInfo:   "info",
		UpdatedStatus: "Unknown",
	}

	t.Run("When exclusions are empty it should omit NOT IN and bind updated status as $4", func(t *testing.T) {
		t.Parallel()
		params := base
		params.ExcludedLifecycleStatuses = nil

		sql, args := buildPrepareDevicesAfterRestoreQuery(params)

		require.NotContains(t, sql, "NOT IN")
		require.Contains(t, sql, "jsonb_build_object('status', $4::text)")
		require.Equal(t, []any{
			"awaitingReconnect",
			"AwaitingReconnect",
			"info",
			"Unknown",
		}, args)
	})

	t.Run("When exclusions has one status it should use NOT IN ($4) and bind updated status as $5", func(t *testing.T) {
		t.Parallel()
		params := base
		params.ExcludedLifecycleStatuses = []string{"Decommissioned"}

		sql, args := buildPrepareDevicesAfterRestoreQuery(params)

		require.Contains(t, sql, "NOT IN ($4)")
		require.Contains(t, sql, "jsonb_build_object('status', $5::text)")
		require.Equal(t, []any{
			"awaitingReconnect",
			"AwaitingReconnect",
			"info",
			"Decommissioned",
			"Unknown",
		}, args)
	})

	t.Run("When exclusions has two statuses it should use NOT IN ($4, $5) and bind updated status as $6", func(t *testing.T) {
		t.Parallel()
		params := NewDeviceAwaitingReconnectPrepareParams()

		sql, args := buildPrepareDevicesAfterRestoreQuery(params)

		require.Contains(t, sql, "NOT IN ($4, $5)")
		require.Contains(t, sql, "jsonb_build_object('status', $6::text)")
		require.Equal(t, []any{
			params.AnnotationKey,
			params.SummaryStatus,
			params.SummaryInfo,
			params.ExcludedLifecycleStatuses[0],
			params.ExcludedLifecycleStatuses[1],
			params.UpdatedStatus,
		}, args)
	})

	t.Run("When exclusions has three statuses it should use NOT IN ($4, $5, $6) and bind updated status as $7", func(t *testing.T) {
		t.Parallel()
		params := base
		params.ExcludedLifecycleStatuses = []string{"Decommissioned", "Decommissioning", "Extra"}

		sql, args := buildPrepareDevicesAfterRestoreQuery(params)

		require.Contains(t, sql, "NOT IN ($4, $5, $6)")
		require.NotContains(t, sql, "NOT IN ($4, $5, $6,")
		require.Contains(t, sql, "jsonb_build_object('status', $7::text)")
		require.Equal(t, []any{
			"awaitingReconnect",
			"AwaitingReconnect",
			"info",
			"Decommissioned",
			"Decommissioning",
			"Extra",
			"Unknown",
		}, args)
		require.Equal(t, 2, strings.Count(sql, "$7::text"), "updated status placeholder should appear in both CASE branches")
	})
}
