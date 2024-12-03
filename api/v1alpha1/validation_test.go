package v1alpha1

import (
	"testing"

	"github.com/flightctl/flightctl/internal/util"
	"github.com/robfig/cron/v3"
	"github.com/stretchr/testify/require"
)

func TestValidateUpdateScheduleCron(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		schedule string
		wantErr  bool
	}{
		{
			name:     "valid every minute",
			schedule: "* * * * *",
		},
		{
			name:     "valid hourly",
			schedule: "0 * * * *",
		},
		{
			name:     "valid daily",
			schedule: "0 0 * * *",
		},
		{
			name:     "valid weekly",
			schedule: "0 0 * * 0",
		},
		{
			name:     "valid monthly",
			schedule: "0 0 1 * *",
		},
		{
			name:     "valid yearly",
			schedule: "0 0 1 1 *",
		},
		{
			name:     "valid step",
			schedule: "*/15 * * * *",
		},
		{
			name:     "valid range",
			schedule: "0-30 * * * *",
		},
		{
			name:     "valid list",
			schedule: "0,15,30,45 * * * *",
			wantErr:  false,
		},
		{
			name:     "valid multiple ranges",
			schedule: "0-15,30-45 * * * *",
		},

		// invalid
		{
			name:     "invalid too few fields",
			schedule: "* * * *",
			wantErr:  true,
		},
		{
			name:     "invalid too many fields",
			schedule: "* * * * * *",
			wantErr:  true,
		},
		{
			name:     "invalid minute out of range",
			schedule: "60 * * * *",
			wantErr:  true,
		},
		{
			name:     "invalid hour out of range",
			schedule: "* 24 * * *",
			wantErr:  true,
		},
		{
			name:     "invalid day of month out of range",
			schedule: "* * 32 * *",
			wantErr:  true,
		},
		{
			name:     "invalid month out of range",
			schedule: "* * * 13 *",
			wantErr:  true,
		},
		{
			name:     "invalid day of week out of range",
			schedule: "* * * * 7",
			wantErr:  true,
		},
		{
			name:     "invalid step syntax",
			schedule: "*/f * * * *",
			wantErr:  true,
		},
		{
			name:     "invalid range syntax",
			schedule: "0-f * * * *",
			wantErr:  true,
		},
		{
			name:     "invalid range start greater",
			schedule: "30-15 * * * *",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schedule := UpdateSchedule{
				At:       tt.schedule,
				TimeZone: util.StrToPtr("America/New_York"),
			}

			errs := schedule.Validate()
			if tt.wantErr {
				require.NotEmpty(errs)
				return
			}
			require.Empty(errs)
		})
	}
}

func TestValidateUpdateScheduleTimeZone(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		timeZone string
		wantErr  bool
	}{
		{
			name:     "valid time zone",
			timeZone: "America/Los_Angeles",
		},
		{
			name:     "valid default time zone",
			timeZone: "Local",
		},
		{
			name:     "invalid time zone name with ..",
			timeZone: "America/..",
			wantErr:  true,
		},
		{
			name:     "invalid time zone name with - prefix",
			timeZone: "-America/New_York",
			wantErr:  true,
		},
		{
			name:     "invalid name which exceeds 14 characters",
			timeZone: "ThisShouldNotBePossible/New_York",
			wantErr:  true,
		},
		{
			name:     "invalid ambiguous time zone",
			timeZone: "EST",
			wantErr:  true,
		},
		{
			name:     "valid UTC time zone",
			timeZone: "UTC",
		},
		{
			name:     "valid GMT time zone",
			timeZone: "UTC",
		},
		{
			name:     "invalid time zone with space",
			timeZone: "America/ New_York",
			wantErr:  true,
		},
		{
			name:     "invalid time zone with special characters",
			timeZone: "America/New_York!",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			schedule := UpdateSchedule{
				At:       "* * * * *",
				TimeZone: util.StrToPtr(tt.timeZone),
			}

			errs := schedule.Validate()
			if tt.wantErr {
				require.NotEmpty(errs)
				return
			}
			require.Empty(errs)
		})
	}
}

func TestValidateGraceDuration(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name           string
		cronExpression string
		duration       string
		expectError    error
	}{
		{
			name:           "graceDuration within interval",
			cronExpression: "0 * * * *", // every hr
			duration:       "30m",
		},
		{
			name:           "graceDuration exceeds cron interval",
			cronExpression: "0 * * * *", // every hr
			duration:       "2h",
			expectError:    ErrStartGraceDurationExceedsCronInterval,
		},
		{
			name:           "cron every 15 minutes graceDuration within interval",
			cronExpression: "*/15 * * * *", // every 15 minutes
			duration:       "10m",
		},
		{
			name:           "cron every 15 minutes graceDuration exceeds interval",
			cronExpression: "*/15 * * * *", // every 15 minutes
			duration:       "30m",
			expectError:    ErrStartGraceDurationExceedsCronInterval,
		},
		{
			name:           "daily cron graceDuration within interval",
			cronExpression: "0 0 * * *", // every day at midnight (24h)
			duration:       "12h",
		},
		{
			name:           "complex cron graceDuration within interval",
			cronExpression: "0 9,12,15 * * *", // 9 AM, 12 PM, 3 PM
			duration:       "2h",              // shortest interval is 3 hours
		},
		{
			name:           "cron with irregular schedule, graceDuration exceeds shortest interval",
			cronExpression: "0 9,12,15 * * *", // 9 AM, 12 PM, 3 PM
			duration:       "4h",              // shortest interval is 3 hours
			expectError:    ErrStartGraceDurationExceedsCronInterval,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
			schedule, err := parser.Parse(tt.cronExpression)
			require.NoError(err)

			err = validateGraceDuration(schedule, tt.duration)
			if tt.expectError != nil {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}
