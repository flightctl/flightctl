package policy

import (
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
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

func TestScheduleParse(t *testing.T) {
	testCases := []struct {
		name          string
		timeZone      *string
		at            string
		grace         string
		expectedError string
	}{
		{
			name:  "When time zone is omitted it should use the device local time zone",
			at:    "0 12 * * *",
			grace: "5m",
		},
		{
			name:          "When time zone is invalid it should report a parsing error",
			timeZone:      lo.ToPtr("Invalid/Timezone"),
			at:            "0 12 * * *",
			grace:         "5m",
			expectedError: "invalid time zone",
		},
		{
			name:          "When cron is invalid it should report a parsing error",
			timeZone:      lo.ToPtr("UTC"),
			at:            "invalid",
			grace:         "5m",
			expectedError: "invalid cron expression",
		},
		{
			name:          "When grace duration is invalid it should report a parsing error",
			timeZone:      lo.ToPtr("UTC"),
			at:            "0 12 * * *",
			grace:         "invalid",
			expectedError: "invalid start grace duration",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			for _, policyType := range []Type{Update, Download} {
				t.Run(string(policyType), func(t *testing.T) {
					require := require.New(t)
					logger := log.NewPrefixLogger("test")
					s := newSchedule(policyType)
					now := time.Date(2026, 1, 1, 12, 3, 0, 0, time.Local)
					s.nowFn = func() time.Time { return now }
					err := s.Parse(logger, &v1beta1.UpdateSchedule{
						TimeZone:           tt.timeZone,
						At:                 tt.at,
						StartGraceDuration: tt.grace,
					})
					if tt.expectedError != "" {
						require.ErrorContains(err, tt.expectedError)
						return
					}

					require.NoError(err)
					require.Equal(time.Local, s.location)
					require.True(s.IsReady(logger))
					now = now.Add(3 * time.Minute)
					require.False(s.IsReady(logger))
				})
			}
		})
	}
}

func TestIsReadyAcrossTime(t *testing.T) {
	type check struct {
		at    string
		ready bool
	}

	testCases := []struct {
		name      string
		timeZone  string
		at        string
		grace     string
		parseTime string
		checks    []check
	}{
		{
			name:      "When parsed before the London window it should honor windows across autumn DST",
			timeZone:  "Europe/London",
			at:        "18 11 * * *",
			grace:     "1h",
			parseTime: "2026-10-23T11:17:00+01:00",
			checks: []check{
				{at: "2026-10-23T11:44:00+01:00", ready: true},
				{at: "2026-10-24T11:30:00+01:00", ready: true},
				{at: "2026-10-25T11:30:00Z", ready: true},
				{at: "2026-10-26T11:30:00Z", ready: true},
			},
		},
		{
			name:      "When parsed during the London window it should honor windows across autumn DST",
			timeZone:  "Europe/London",
			at:        "18 11 * * *",
			grace:     "1h",
			parseTime: "2026-10-23T11:19:17+01:00",
			checks: []check{
				{at: "2026-10-23T11:44:00+01:00", ready: true},
				{at: "2026-10-23T12:18:00+01:00", ready: true},
				{at: "2026-10-23T12:18:00.000000001+01:00", ready: false},
				{at: "2026-10-24T11:17:59.999999999+01:00", ready: false},
				{at: "2026-10-24T11:18:00+01:00", ready: true},
				{at: "2026-10-24T11:30:00+01:00", ready: true},
				{at: "2026-10-25T11:30:00Z", ready: true},
				{at: "2026-10-26T11:30:00Z", ready: true},
			},
		},
		{
			name:      "When parsed before New York autumn DST it should honor daily windows",
			timeZone:  "America/New_York",
			at:        "0 12 * * *",
			grace:     "30m",
			parseTime: "2026-10-30T13:00:00-04:00",
			checks: []check{
				{at: "2026-10-31T12:10:00-04:00", ready: true},
				{at: "2026-11-01T12:10:00-05:00", ready: true},
				{at: "2026-11-02T12:10:00-05:00", ready: true},
			},
		},
		{
			name:      "When the activation hour repeats it should honor both windows independently",
			timeZone:  "America/New_York",
			at:        "0 1 * * *",
			grace:     "20m",
			parseTime: "2026-10-30T13:00:00-04:00",
			checks: []check{
				{at: "2026-11-01T01:10:00-04:00", ready: true},
				{at: "2026-11-01T01:20:00-04:00", ready: true},
				{at: "2026-11-01T01:30:00-04:00", ready: false},
				{at: "2026-11-01T01:10:00-05:00", ready: true},
				{at: "2026-11-01T01:20:00-05:00", ready: true},
				{at: "2026-11-01T01:30:00-05:00", ready: false},
			},
		},
		{
			name:      "When parsed before Berlin spring DST it should honor daily windows",
			timeZone:  "Europe/Berlin",
			at:        "0 12 * * *",
			grace:     "30m",
			parseTime: "2026-03-27T13:00:00+01:00",
			checks: []check{
				{at: "2026-03-28T12:10:00+01:00", ready: true},
				{at: "2026-03-29T12:10:00+02:00", ready: true},
				{at: "2026-03-30T12:10:00+02:00", ready: true},
			},
		},
		{
			name:      "When grace spans the skipped hour it should use elapsed time",
			timeZone:  "Europe/Berlin",
			at:        "0 1 * * *",
			grace:     "90m",
			parseTime: "2026-03-27T13:00:00+01:00",
			checks: []check{
				{at: "2026-03-29T03:15:00+02:00", ready: true},
				{at: "2026-03-29T03:30:00+02:00", ready: true},
				{at: "2026-03-29T03:30:00.000000001+02:00", ready: false},
			},
		},
		{
			name:      "When the activation hour is skipped it should wait for the next activation",
			timeZone:  "Europe/Berlin",
			at:        "30 2 * * *",
			grace:     "30m",
			parseTime: "2026-03-27T13:00:00+01:00",
			checks: []check{
				{at: "2026-03-28T02:45:00+01:00", ready: true},
				{at: "2026-03-29T03:00:00+02:00", ready: false},
				{at: "2026-03-30T02:45:00+02:00", ready: true},
			},
		},
		{
			name:      "When weekly activations span autumn DST it should honor each window",
			timeZone:  "Europe/London",
			at:        "18 11 * * 1",
			grace:     "1h",
			parseTime: "2026-10-12T12:00:00+01:00",
			checks: []check{
				{at: "2026-10-19T11:30:00+01:00", ready: true},
				{at: "2026-10-26T11:30:00Z", ready: true},
				{at: "2026-11-02T11:30:00Z", ready: true},
			},
		},
		{
			name:      "When months have different lengths it should honor each monthly window",
			timeZone:  "UTC",
			at:        "0 12 1 * *",
			grace:     "30m",
			parseTime: "2025-12-15T12:00:00Z",
			checks: []check{
				{at: "2026-01-01T12:10:00Z", ready: true},
				{at: "2026-02-01T12:10:00Z", ready: true},
				{at: "2026-03-01T12:10:00Z", ready: true},
				{at: "2026-04-01T12:10:00Z", ready: true},
			},
		},
		{
			name:      "When activations are irregular it should honor the actual activation",
			timeZone:  "UTC",
			at:        "0 12 1,15 * *",
			grace:     "30m",
			parseTime: "2026-01-02T12:00:00Z",
			checks: []check{
				{at: "2026-01-15T12:10:00Z", ready: true},
				{at: "2026-02-01T12:10:00Z", ready: true},
				{at: "2026-02-15T12:10:00Z", ready: true},
			},
		},
		{
			name:      "When checking window boundaries it should include only the exact closed interval",
			timeZone:  "UTC",
			at:        "0 12 * * *",
			grace:     "5m",
			parseTime: "2026-01-01T10:00:00Z",
			checks: []check{
				{at: "2026-01-01T11:59:59.999999999Z", ready: false},
				{at: "2026-01-01T12:00:00Z", ready: true},
				{at: "2026-01-01T12:00:00.000000001Z", ready: true},
				{at: "2026-01-01T12:04:59.999999999Z", ready: true},
				{at: "2026-01-01T12:05:00Z", ready: true},
				{at: "2026-01-01T12:05:00.000000001Z", ready: false},
				{at: "2026-01-01T12:05:00.5Z", ready: false},
			},
		},
		{
			name:      "When grace includes fractional seconds it should preserve the precise endpoint",
			timeZone:  "UTC",
			at:        "0 12 * * *",
			grace:     "1.5s",
			parseTime: "2026-01-01T10:00:00Z",
			checks: []check{
				{at: "2026-01-01T12:00:01.499999999Z", ready: true},
				{at: "2026-01-01T12:00:01.5Z", ready: true},
				{at: "2026-01-01T12:00:01.500000001Z", ready: false},
			},
		},
		{
			name:      "When the cron has no possible activation it should remain not ready",
			timeZone:  "UTC",
			at:        "0 12 31 2 *",
			grace:     "5m",
			parseTime: "2026-01-01T10:00:00Z",
			checks: []check{
				{at: "2026-02-28T12:00:00Z", ready: false},
				{at: "2026-03-01T12:00:00Z", ready: false},
			},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			for _, policyType := range []Type{Update, Download} {
				t.Run(string(policyType), func(t *testing.T) {
					setupRequire := require.New(t)
					logger := log.NewPrefixLogger("test")
					logger.SetLevel(logrus.DebugLevel)
					now, err := time.Parse(time.RFC3339Nano, tt.parseTime)
					setupRequire.NoError(err)

					s := newSchedule(policyType)
					clockReads := 0
					s.nowFn = func() time.Time {
						clockReads++
						return now
					}
					setupRequire.NoError(s.Parse(logger, &v1beta1.UpdateSchedule{
						TimeZone:           lo.ToPtr(tt.timeZone),
						At:                 tt.at,
						StartGraceDuration: tt.grace,
					}))
					setupRequire.Zero(clockReads, "parsing should be independent of the current time")

					for _, check := range tt.checks {
						t.Run(check.at, func(t *testing.T) {
							require := require.New(t)
							checkTime, err := time.Parse(time.RFC3339Nano, check.at)
							require.NoError(err)
							now = checkTime
							require.Equal(check.ready, s.IsReady(logger))
						})
					}
				})
			}
		})
	}
}
