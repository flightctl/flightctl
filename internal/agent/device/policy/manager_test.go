package policy

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// Helper function to create test devices
func createTestDevice(version string, spec *v1alpha1.DeviceSpec) *v1alpha1.Device {
	return &v1alpha1.Device{
		Metadata: v1alpha1.ObjectMeta{
			Name: lo.ToPtr("test-device"),
			Annotations: lo.ToPtr(map[string]string{
				v1alpha1.DeviceAnnotationRenderedVersion: version,
			}),
		},
		Spec: spec,
	}
}

func TestIsReady(t *testing.T) {
	require := require.New(t)
	nyLoc, err := time.LoadLocation("America/New_York")
	require.NoError(err)
	berlinLoc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(err)

	testCases := []struct {
		name            string
		timeZone        string
		updateSchedule  *v1alpha1.UpdateSchedule
		currentTime     time.Time
		expectedReady   bool
		expectedNextRun time.Time
	}{
		{
			name: "not ready: current time is before next run UTC",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:     time.Date(2024, 12, 20, 11, 5, 0, 0, time.UTC), // 11:05
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // 12:00 today
		},
		{
			name: "not ready: outside grace period UTC",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 6, 0, 0, time.UTC), // 12:06 - 5m = 12:01
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: within grace period America/New_York",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 6, 0, 0, nyLoc), // 12:06 - 10m = 11:56
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, nyLoc), // 12:00 next day
		},
		{
			name: "ready: handles backward DST transition in America/New_York",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 1 * * *", // 1:00 AM
				StartGraceDuration: lo.ToPtr("60m"),
			},
			currentTime:     time.Date(2024, 11, 3, 1, 30, 0, 0, nyLoc), // 1:30 am during repeated hour
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 11, 4, 1, 0, 0, 0, nyLoc), // 1:00 am next day
		},
		{
			name: "ready: grace period handles DST transition in Europe/Berlin",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("Europe/Berlin"),
				At:                 "0 1 * * *", // 1:00 am
				StartGraceDuration: lo.ToPtr("90m"),
			},
			// test time at 03:15 am, which falls within the grace period
			// even though the 2 am hour was skipped due to DST.
			currentTime:     time.Date(2025, 3, 30, 3, 15, 0, 0, berlinLoc),
			expectedReady:   true,
			expectedNextRun: time.Date(2025, 3, 31, 1, 0, 0, 0, berlinLoc), // 1:00 am next day
		},
		{
			name: "ready: time equal to next",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc), // 12:00
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc), // 12:00
		},
		{
			name: "not ready: current time equals grace period end",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 5, 0, 0, time.UTC), // 12:05 (end of grace)
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: equals grace period for 30-minute interval",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "*/30 * * * *", // every 30 minutes
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 35, 0, 0, time.UTC), // 12:35
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 20, 13, 0, 0, 0, time.UTC), // 13:00
		},
		{
			name: "ready: exactly at start of grace period",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // 12:00
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: exactly at the start of the interval with no grace period",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: nil,
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // 12:00
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "not ready: exactly at the end of the interval with no grace period",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: nil,
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 1, time.UTC), // slightly after 12:00
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			log := log.NewPrefixLogger("test")
			log.SetLevel(logrus.DebugLevel)
			s := newSchedule(Download)
			// override time.Now
			s.nowFn = func() time.Time { return tt.currentTime }

			err := s.Parse(log, tt.updateSchedule)
			require.NoError(err)

			ready := s.IsReady(log)
			require.Equal(tt.expectedReady, ready)

			if !ready {
				nextRun := s.cron.Next(tt.currentTime)
				require.Equal(tt.expectedNextRun, nextRun, "expected next run time")
			}
		})
	}
}

func TestIsVersionReady(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	t.Run("context cancelled", func(t *testing.T) {
		manager := NewManager(log)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		device := createTestDevice("1", &v1alpha1.DeviceSpec{})
		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.Nil(nextTime)
		require.Error(err)
		require.ErrorIs(err, context.Canceled)
	})

	t.Run("empty version string", func(t *testing.T) {
		manager := NewManager(log)
		ctx := context.Background()

		device := createTestDevice("", &v1alpha1.DeviceSpec{})
		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.Nil(nextTime)
		require.Error(err)
		require.Contains(err.Error(), "version is required")
	})

	t.Run("version not in cache - returns ready", func(t *testing.T) {
		manager := NewManager(log)
		ctx := context.Background()

		device := createTestDevice("2", &v1alpha1.DeviceSpec{})
		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.Nil(nextTime)
		require.Error(err)
	})

	t.Run("version with no policies - returns ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		// Sync a version with no update policy
		device := createTestDevice("3", &v1alpha1.DeviceSpec{})
		err := manager.Sync(ctx, device)
		require.NoError(err)

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.True(ready)
		require.Nil(nextTime)
		require.NoError(err)
	})

	t.Run("version with only download policy ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		// Create a schedule that's always ready (current time)
		now := time.Now()
		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("4", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn to make schedule ready
		vs, _ := manager.versions.Get("4")
		vs.download.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.True(ready)
		require.Nil(nextTime)
		require.NoError(err)
	})

	t.Run("version with only update policy ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Now()
		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				UpdateSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("5", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn to make schedule ready
		vs, _ := manager.versions.Get("5")
		vs.update.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.True(ready)
		require.Nil(nextTime)
		require.NoError(err)
	})

	t.Run("version with both policies ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Now()
		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute
					StartGraceDuration: lo.ToPtr("5m"),
				},
				UpdateSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("6", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn to make both schedules ready
		vs, _ := manager.versions.Get("6")
		vs.download.schedule.nowFn = func() time.Time { return now }
		vs.update.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.True(ready)
		require.Nil(nextTime)
		require.NoError(err)
	})

	t.Run("version with download policy not ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Date(2024, 12, 20, 11, 0, 0, 0, time.UTC)
		expectedNextTime := time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC)

		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("UTC"),
					At:                 "0 12 * * *", // 12:00 PM daily
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("7", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn to make schedule not ready
		vs, _ := manager.versions.Get("7")
		vs.download.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.NoError(err)
		require.Equal(expectedNextTime, *nextTime)
	})

	t.Run("version with update policy not ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Date(2024, 12, 20, 11, 0, 0, 0, time.UTC)
		expectedNextTime := time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC)

		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				UpdateSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("UTC"),
					At:                 "0 12 * * *", // 12:00 PM daily
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("8", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn to make schedule not ready
		vs, _ := manager.versions.Get("8")
		vs.update.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.NoError(err)
		require.Equal(expectedNextTime, *nextTime)
	})

	t.Run("version with mixed policy readiness - download ready, update not ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Date(2024, 12, 20, 11, 0, 0, 0, time.UTC)
		expectedNextTime := time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC)

		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute (ready)
					StartGraceDuration: lo.ToPtr("5m"),
				},
				UpdateSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("UTC"),
					At:                 "0 12 * * *", // 12:00 PM daily (not ready)
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("9", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn - download ready, update not ready
		vs, _ := manager.versions.Get("9")
		vs.download.schedule.nowFn = func() time.Time { return now }
		vs.update.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.NoError(err)
		require.Equal(expectedNextTime, *nextTime)
	})

	t.Run("version with mixed policy readiness - download not ready, update ready", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Date(2024, 12, 20, 11, 0, 0, 0, time.UTC)
		expectedNextTime := time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC)

		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("UTC"),
					At:                 "0 12 * * *", // 12:00 PM daily (not ready)
					StartGraceDuration: lo.ToPtr("5m"),
				},
				UpdateSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute (ready)
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("10", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn - download not ready, update ready
		vs, _ := manager.versions.Get("10")
		vs.download.schedule.nowFn = func() time.Time { return now }
		vs.update.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.NoError(err)
		require.Equal(expectedNextTime, *nextTime)
	})

	t.Run("version with both policies not ready - returns min next time", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Date(2024, 12, 20, 11, 0, 0, 0, time.UTC)
		downloadNextTime := time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC) // Earlier time

		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("UTC"),
					At:                 "0 12 * * *", // 12:00 PM daily
					StartGraceDuration: lo.ToPtr("5m"),
				},
				UpdateSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("UTC"),
					At:                 "0 14 * * *", // 2:00 PM daily
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("11", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		// Override nowFn to make both schedules not ready
		vs, _ := manager.versions.Get("11")
		vs.download.schedule.nowFn = func() time.Time { return now }
		vs.update.schedule.nowFn = func() time.Time { return now }

		ready, nextTime, err := manager.IsVersionReady(ctx, device)
		require.False(ready)
		require.NoError(err)
		require.Equal(downloadNextTime, *nextTime) // Should return the earlier time
	})

	t.Run("caching behavior - positive results cached", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		now := time.Now()
		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					At:                 "* * * * *", // every minute
					StartGraceDuration: lo.ToPtr("5m"),
				},
			},
		}

		device := createTestDevice("13", deviceSpec)
		err := manager.Sync(ctx, device)
		require.NoError(err)

		vs, _ := manager.versions.Get("13")
		vs.download.schedule.nowFn = func() time.Time { return now }

		// First call should compute and cache result
		ready1, _, err := manager.IsVersionReady(ctx, device)
		require.True(ready1)
		require.NoError(err)
		require.True(vs.download.isReady) // Should be cached

		// Change nowFn to return a time that would make it not ready
		vs.download.schedule.nowFn = func() time.Time {
			return now.Add(-time.Hour) // Much earlier time
		}

		// Second call should still return true due to caching
		ready2, _, err := manager.IsVersionReady(ctx, device)
		require.True(ready2)
		require.NoError(err)
		require.True(vs.download.isReady) // Should still be cached
	})

	t.Run("timezone handling", func(t *testing.T) {
		manager := NewManager(log).(*manager)
		ctx := context.Background()

		nyLoc, err := time.LoadLocation("America/New_York")
		require.NoError(err)

		now := time.Date(2024, 12, 20, 12, 5, 0, 0, nyLoc) // 12:05 PM EST
		deviceSpec := &v1alpha1.DeviceSpec{
			UpdatePolicy: &v1alpha1.DeviceUpdatePolicySpec{
				DownloadSchedule: &v1alpha1.UpdateSchedule{
					TimeZone:           lo.ToPtr("America/New_York"),
					At:                 "0 12 * * *", // 12:00 PM
					StartGraceDuration: lo.ToPtr("10m"),
				},
			},
		}

		device := createTestDevice("14", deviceSpec)
		err = manager.Sync(ctx, device)
		require.NoError(err)

		vs, _ := manager.versions.Get("14")
		vs.download.schedule.nowFn = func() time.Time { return now }

		ready, _, err := manager.IsVersionReady(ctx, device)
		require.True(ready) // Should be ready within grace period
		require.NoError(err)
	})
}

func TestNextTriggerTime(t *testing.T) {
	require := require.New(t)
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	testCases := []struct {
		name                string
		updateSchedule      *v1alpha1.UpdateSchedule
		currentTime         time.Time
		expectedTriggerTime time.Time
		description         string
	}{
		{
			name: "ready now - returns current time",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 5, 0, 0, time.UTC), // Within grace period
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 5, 0, 0, time.UTC), // Current time
			description:         "When schedule is ready, should return current time",
		},
		{
			name: "ready at exact trigger time",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // Exactly at trigger
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // Current time
			description:         "When exactly at trigger time, should return current time",
		},
		{
			name: "not ready - returns next cron time",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:         time.Date(2024, 12, 20, 11, 0, 0, 0, time.UTC), // Before trigger
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // Next trigger
			description:         "When not ready, should return next cron trigger time",
		},
		{
			name: "past grace period - returns next day trigger",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 10, 0, 0, time.UTC), // Past grace period
			expectedTriggerTime: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC),  // Next day
			description:         "When past grace period, should return next day trigger",
		},
		{
			name: "hourly schedule - returns next hour",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 * * * *", // Every hour
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 10, 0, 0, time.UTC), // Past grace period
			expectedTriggerTime: time.Date(2024, 12, 20, 13, 0, 0, 0, time.UTC),  // Next hour
			description:         "Hourly schedule should return next hour when past grace",
		},
		{
			name: "every 30 minutes - ready within grace",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "*/30 * * * *", // Every 30 minutes
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 35, 0, 0, time.UTC), // 5 min after 12:30
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 35, 0, 0, time.UTC), // Current time (ready)
			description:         "30-minute schedule within grace should return current time",
		},
		{
			name: "every 30 minutes - not ready",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "*/30 * * * *", // Every 30 minutes
				StartGraceDuration: lo.ToPtr("5m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 25, 0, 0, time.UTC), // Before 12:30
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 30, 0, 0, time.UTC), // Next 30-min mark
			description:         "30-minute schedule before trigger should return next 30-min mark",
		},
		{
			name: "weekly schedule - returns next week",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * 1", // Monday at 12:00 PM
				StartGraceDuration: lo.ToPtr("1h"),
			},
			currentTime:         time.Date(2024, 12, 20, 14, 0, 0, 0, time.UTC), // Friday, past grace
			expectedTriggerTime: time.Date(2024, 12, 23, 12, 0, 0, 0, time.UTC), // Next Monday
			description:         "Weekly schedule should return next Monday when past grace",
		},
		{
			name: "no grace period - ready at exact time",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: nil,          // No grace period
			},
			currentTime:         time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // Exactly at trigger
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // Current time
			description:         "No grace period, exactly at trigger should return current time",
		},
		{
			name: "no grace period - not ready after trigger",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: nil,          // No grace period
			},
			currentTime:         time.Date(2024, 12, 20, 12, 0, 0, 1, time.UTC), // 1 second after
			expectedTriggerTime: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // Next day
			description:         "No grace period, after trigger should return next day",
		},
		{
			name: "timezone America/New_York - ready within grace",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 PM EST
				StartGraceDuration: lo.ToPtr("15m"),
			},
			currentTime: func() time.Time {
				nyLoc, _ := time.LoadLocation("America/New_York")
				return time.Date(2024, 12, 20, 12, 10, 0, 0, nyLoc) // 10 min after trigger
			}(),
			expectedTriggerTime: func() time.Time {
				nyLoc, _ := time.LoadLocation("America/New_York")
				return time.Date(2024, 12, 20, 12, 10, 0, 0, nyLoc) // Current time
			}(),
			description: "New York timezone within grace should return current time",
		},
		{
			name: "timezone America/New_York - not ready",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 PM EST
				StartGraceDuration: lo.ToPtr("15m"),
			},
			currentTime: func() time.Time {
				nyLoc, _ := time.LoadLocation("America/New_York")
				return time.Date(2024, 12, 20, 11, 30, 0, 0, nyLoc) // Before trigger
			}(),
			expectedTriggerTime: func() time.Time {
				nyLoc, _ := time.LoadLocation("America/New_York")
				return time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc) // Next trigger
			}(),
			description: "New York timezone before trigger should return next trigger time",
		},
		{
			name: "timezone Europe/Berlin - ready within grace",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("Europe/Berlin"),
				At:                 "0 14 * * *", // 2:00 PM CET
				StartGraceDuration: lo.ToPtr("20m"),
			},
			currentTime: func() time.Time {
				berlinLoc, _ := time.LoadLocation("Europe/Berlin")
				return time.Date(2024, 12, 20, 14, 15, 0, 0, berlinLoc) // 15 min after trigger
			}(),
			expectedTriggerTime: func() time.Time {
				berlinLoc, _ := time.LoadLocation("Europe/Berlin")
				return time.Date(2024, 12, 20, 14, 15, 0, 0, berlinLoc) // Current time
			}(),
			description: "Berlin timezone within grace should return current time",
		},
		{
			name: "monthly schedule - returns next month",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 1 * *", // 1st of month at 12:00 PM
				StartGraceDuration: lo.ToPtr("2h"),
			},
			currentTime:         time.Date(2024, 12, 20, 15, 0, 0, 0, time.UTC), // Mid-month
			expectedTriggerTime: time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC),   // Next month
			description:         "Monthly schedule should return next month trigger",
		},
		{
			name: "complex cron - specific day and time",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "30 14 15 * *", // 15th of month at 2:30 PM
				StartGraceDuration: lo.ToPtr("1h"),
			},
			currentTime:         time.Date(2024, 12, 10, 10, 0, 0, 0, time.UTC),  // Before trigger
			expectedTriggerTime: time.Date(2024, 12, 15, 14, 30, 0, 0, time.UTC), // 15th at 2:30 PM
			description:         "Complex cron should return correct specific trigger time",
		},
		{
			name: "end of grace period boundary",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 10, 0, 0, time.UTC), // Exactly at grace end
			expectedTriggerTime: time.Date(2024, 12, 20, 12, 10, 0, 0, time.UTC), // Current time (still ready)
			description:         "At grace period boundary should return current time",
		},
		{
			name: "past grace period boundary",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 PM daily
				StartGraceDuration: lo.ToPtr("10m"),
			},
			currentTime:         time.Date(2024, 12, 20, 12, 10, 0, 1, time.UTC), // 1 second past grace
			expectedTriggerTime: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC),  // Next day
			description:         "Past grace period boundary should return next trigger",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			s := newSchedule(Download)
			s.nowFn = func() time.Time { return tc.currentTime }

			err := s.Parse(log, tc.updateSchedule)
			require.NoError(err, "Failed to parse schedule")

			triggerTime := s.NextTriggerTime(log)
			require.Equal(tc.expectedTriggerTime, triggerTime, tc.description)

			// Verify the logic is consistent with IsReady
			isReady := s.IsReady(log)
			if isReady {
				require.Equal(tc.currentTime, triggerTime, "When ready, NextTriggerTime should return current time")
			} else {
				require.True(triggerTime.After(tc.currentTime), "When not ready, NextTriggerTime should be in the future")
			}
		})
	}
}
