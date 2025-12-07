package policy

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestIsReady(t *testing.T) {
	require := require.New(t)
	nyLoc, err := time.LoadLocation("America/New_York")
	require.NoError(err)
	berlinLoc, err := time.LoadLocation("Europe/Berlin")
	require.NoError(err)

	testCases := []struct {
		name            string
		timeZone        string
		updateSchedule  *v1beta1.UpdateSchedule
		currentTime     time.Time
		expectedReady   bool
		expectedNextRun time.Time
	}{
		{
			name: "not ready: current time is before next run UTC",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: "10m",
			},
			currentTime:     time.Date(2024, 12, 20, 11, 5, 0, 0, time.UTC), // 11:05
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // 12:00 today
		},
		{
			name: "not ready: outside grace period UTC",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: "5m",
			},
			currentTime:     time.Date(2024, 12, 20, 12, 6, 0, 0, time.UTC), // 12:06 - 5m = 12:01
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: within grace period America/New_York",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: "10m",
			},
			currentTime:     time.Date(2024, 12, 20, 12, 6, 0, 0, nyLoc), // 12:06 - 10m = 11:56
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, nyLoc), // 12:00 next day
		},
		{
			name: "ready: handles backward DST transition in America/New_York",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 1 * * *", // 1:00 AM
				StartGraceDuration: "60m",
			},
			currentTime:     time.Date(2024, 11, 3, 1, 30, 0, 0, nyLoc), // 1:30 am during repeated hour
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 11, 4, 1, 0, 0, 0, nyLoc), // 1:00 am next day
		},
		{
			name: "ready: grace period handles DST transition in Europe/Berlin",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("Europe/Berlin"),
				At:                 "0 1 * * *", // 1:00 am
				StartGraceDuration: "90m",
			},
			// test time at 03:15 am, which falls within the grace period
			// even though the 2 am hour was skipped due to DST.
			currentTime:     time.Date(2025, 3, 30, 3, 15, 0, 0, berlinLoc),
			expectedReady:   true,
			expectedNextRun: time.Date(2025, 3, 31, 1, 0, 0, 0, berlinLoc), // 1:00 am next day
		},
		{
			name: "ready: time equal to next",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: "10m",
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc), // 12:00
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc), // 12:00
		},
		{
			name: "not ready: current time equals grace period end",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: "5m",
			},
			currentTime:     time.Date(2024, 12, 20, 12, 5, 0, 0, time.UTC), // 12:05 (end of grace)
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: equals grace period for 30-minute interval",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "*/30 * * * *", // every 30 minutes
				StartGraceDuration: "5m",
			},
			currentTime:     time.Date(2024, 12, 20, 12, 35, 0, 0, time.UTC), // 12:35
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 20, 13, 0, 0, 0, time.UTC), // 13:00
		},
		{
			name: "ready: exactly at start of grace period",
			updateSchedule: &v1beta1.UpdateSchedule{
				TimeZone:           lo.ToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: "5m",
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // 12:00
			expectedReady:   true,
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
