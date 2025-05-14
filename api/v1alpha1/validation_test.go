package v1alpha1

import (
	"encoding/base64"
	"testing"

	"github.com/robfig/cron/v3"
	"github.com/samber/lo"
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
				TimeZone: lo.ToPtr("America/New_York"),
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
				TimeZone: lo.ToPtr(tt.timeZone),
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

func TestValidateParametersInString(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name           string
		paramString    string
		containsParams bool
		expectError    int
	}{
		{
			name:           "no parameters",
			paramString:    "hello world",
			containsParams: false,
			expectError:    0,
		},
		{
			name:           "simple name access",
			paramString:    "hello {{ .metadata.name }} world",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "name access using Go struct syntax fails",
			paramString:    "hello {{ .Metadata.Name }} world",
			containsParams: true,
			expectError:    1,
		},
		{
			name:           "label access using Go struct syntax fails",
			paramString:    "hello {{ .Metadata.Labels.key }} world",
			containsParams: true,
			expectError:    1,
		},
		{
			name:           "accessing non-exposed field fails",
			paramString:    "hello {{ .metadata.annotations.key }} world",
			containsParams: true,
			expectError:    1,
		},
		{
			name:           "upper name",
			paramString:    "{{ upper .metadata.name }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "upper label",
			paramString:    "{{ upper .metadata.labels.key }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "lower name",
			paramString:    "{{ lower .metadata.name }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "lower label",
			paramString:    "{{ lower .metadata.labels.key }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "replace name",
			paramString:    "{{ replace \"old\" \"new\" .metadata.name }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "replace label",
			paramString:    "{{ replace \"old\" \"new\" .metadata.labels.key }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "index",
			paramString:    "{{ index .metadata.labels \"key\" }}",
			containsParams: true,
			expectError:    0,
		},
		{
			name:           "missing function",
			paramString:    "{{ badfunction .metadata.labels \"key\" }}",
			containsParams: true,
			expectError:    1,
		},
		{
			name:           "using range",
			paramString:    "Labels: {{range $key, $value := .metadata.labels }} {{$key}}: {{$value}} {{ end }}",
			containsParams: true,
			expectError:    1,
		},
		{
			name:           "using if",
			paramString:    "{{if .metadata.name }} Resource Name: {{ .metadata.name }} {{ else }} Resource Name is not set. {{ end }}",
			containsParams: true,
			expectError:    1,
		},
		{
			name:           "pipeline",
			paramString:    "{{ .metadata.labels.key | lower | replace \" \" \"-\"}}",
			containsParams: true,
			expectError:    0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			containsParams, errs := validateParametersInString(&(tt.paramString), "path", true)
			require.Len(errs, tt.expectError)
			if len(errs) == 0 {
				require.Equal(tt.containsParams, containsParams)
			}
		})
	}
}

func TestValidateInlineApplicationProviderSpec(t *testing.T) {
	plain := EncodingPlain
	base64Enc := EncodingBase64
	composeSpec := `version: '3'
services:
  app:
    image: quay.io/flightctl-tests/alpine:v1`

	composeInvalidSpecContainerName := `version: '3'
services:
  app:
    container_name: app
    image: quay.io/flightctl-tests/alpine:v1`

	composeInvalidSpecContainerShortname := `version: '3'
services:
  app:
    image: nginx:latest`

	base64Content := base64.StdEncoding.EncodeToString([]byte(composeSpec))
	tests := []struct {
		name          string
		spec          InlineApplicationProviderSpec
		fleetTemplate bool
		expectErr     bool
	}{
		{
			name: "valid plain content",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{
						Path:            "docker-compose.yaml",
						Content:         lo.ToPtr(composeSpec),
						ContentEncoding: &plain,
					},
				},
			},
			expectErr: false,
		},
		{
			name: "duplicate path",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr("abc"), ContentEncoding: &plain},
					{Path: "docker-compose.yaml", Content: lo.ToPtr("def"), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name: "invalid base64 content",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "podman-compose.yaml", Content: lo.ToPtr(composeSpec), ContentEncoding: &base64Enc},
				},
			},
			expectErr: true,
		},
		{
			name: "valid base64 content",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "podman-compose.yml", Content: &base64Content, ContentEncoding: &base64Enc},
				},
			},
			expectErr: false,
		},
		{
			name: "unknown encoding",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr(composeSpec), ContentEncoding: lo.ToPtr(EncodingType("unknown"))},
				},
			},
			expectErr: true,
		},
		{
			name: "invalid compose path",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "invalid-compose.yaml", Content: lo.ToPtr(composeSpec), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name: "invalid use of container_name",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr(composeInvalidSpecContainerName), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name: "invalid container short name",
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr(composeInvalidSpecContainerShortname), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.spec.Validate(lo.ToPtr(AppTypeCompose), tt.fleetTemplate)
			if tt.expectErr {
				require.NotEmpty(t, errs, "expected errors but got none")
			} else {
				require.Empty(t, errs, "expected no errors but got: %v", errs)
			}
		})
	}
}

func TestValidateAlertRules(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name             string
		rules            []ResourceAlertRule
		samplingInterval string
		wantErrs         []error
	}{
		{
			name: "valid increasing thresholds",
			rules: []ResourceAlertRule{
				{Severity: ResourceAlertSeverityTypeInfo, Percentage: 10, Duration: "3s"},
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 20, Duration: "4s"},
				{Severity: ResourceAlertSeverityTypeCritical, Percentage: 30, Duration: "3s"},
			},
			samplingInterval: "1s",
			wantErrs:         nil,
		},
		{
			name: "info equals warning",
			rules: []ResourceAlertRule{
				{Severity: ResourceAlertSeverityTypeInfo, Percentage: 20, Duration: "4s"},
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 20, Duration: "3s"},
			},
			wantErrs:         []error{ErrInfoAlertLessThanWarn},
			samplingInterval: "1s",
		},
		{
			name: "warning greater than critical",
			rules: []ResourceAlertRule{
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 50, Duration: "4s"},
				{Severity: ResourceAlertSeverityTypeCritical, Percentage: 40, Duration: "3s"},
			},
			wantErrs:         []error{ErrWarnAlertLessThanCritical},
			samplingInterval: "1s",
		},
		{
			name: "info greater than critical",
			rules: []ResourceAlertRule{
				{Severity: ResourceAlertSeverityTypeInfo, Percentage: 90, Duration: "3s"},
				{Severity: ResourceAlertSeverityTypeCritical, Percentage: 70, Duration: "4s"},
			},
			wantErrs:         []error{ErrInfoAlertLessThanCritical},
			samplingInterval: "1s",
		},
		{
			name: "duplicate severity and percentage",
			rules: []ResourceAlertRule{
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 10, Duration: "3s"},
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 10, Duration: "3s"},
			},
			wantErrs:         []error{ErrDuplicateAlertSeverity},
			samplingInterval: "1s",
		},
		{
			name: "duplicate severity",
			rules: []ResourceAlertRule{
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 20, Duration: "3s"},
				{Severity: ResourceAlertSeverityTypeWarning, Percentage: 10, Duration: "5s"},
			},
			wantErrs:         []error{ErrDuplicateAlertSeverity},
			samplingInterval: "1s",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateAlertRules(tt.rules, tt.samplingInterval)
			if len(tt.wantErrs) > 0 {
				require.Len(errs, len(tt.wantErrs), "expected %d errors but got %d", len(tt.wantErrs), len(errs))
				for i, wantErr := range tt.wantErrs {
					require.ErrorIs(errs[i], wantErr, "expected error at index %d to be %v, got: %v", i, wantErr, errs[i])
				}
			} else {
				require.Empty(errs, "expected no errors but got: %v", errs)
			}
		})
	}
}
