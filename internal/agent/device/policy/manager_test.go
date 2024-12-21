package policy

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestIsReady(t *testing.T) {
	require := require.New(t)
	nyLoc, err := time.LoadLocation("America/New_York")
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
				TimeZone:           util.StrToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: util.StrToPtr("10m"),
			},
			currentTime:     time.Date(2024, 12, 20, 11, 5, 0, 0, time.UTC), // 11:05
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 20, 12, 0, 0, 0, time.UTC), // 12:00 today
		},
		{
			name: "not ready: outside grace period UTC",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           util.StrToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: util.StrToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 6, 0, 0, time.UTC), // 12:06 - 5m = 12:01
			expectedReady:   false,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: within grace period America/New_York",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           util.StrToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: util.StrToPtr("10m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 6, 0, 0, nyLoc), // 12:06 - 10m = 11:56
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, nyLoc), // 12:00 next day
		},
		{
			name: "ready: time equal to next",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           util.StrToPtr("America/New_York"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: util.StrToPtr("10m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc), // 12:00
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 20, 12, 0, 0, 0, nyLoc), // 12:00
		},
		{
			name: "not ready: current time equals grace period end",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           util.StrToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: util.StrToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 5, 0, 0, time.UTC), // 12:05 (end of grace)
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 21, 12, 0, 0, 0, time.UTC), // 12:00 next day
		},
		{
			name: "ready: equals grace period for 30-minute interval",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           util.StrToPtr("UTC"),
				At:                 "*/30 * * * *", // every 30 minutes
				StartGraceDuration: util.StrToPtr("5m"),
			},
			currentTime:     time.Date(2024, 12, 20, 12, 35, 0, 0, time.UTC), // 12:35
			expectedReady:   true,
			expectedNextRun: time.Date(2024, 12, 20, 13, 0, 0, 0, time.UTC), // 13:00
		},
		{
			name: "ready: exactly at start of grace period",
			updateSchedule: &v1alpha1.UpdateSchedule{
				TimeZone:           util.StrToPtr("UTC"),
				At:                 "0 12 * * *", // 12:00 pm
				StartGraceDuration: util.StrToPtr("5m"),
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

			err := s.Parse(tt.updateSchedule)
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
