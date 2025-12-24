package v1beta1

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/identity"
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
				At:                 tt.schedule,
				TimeZone:           lo.ToPtr("America/New_York"),
				StartGraceDuration: "30s",
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
				At:                 "* * * * *",
				TimeZone:           lo.ToPtr(tt.timeZone),
				StartGraceDuration: "30s",
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

func TestValidateScheduleAndGraceDuration(t *testing.T) {
	tests := []struct {
		name           string
		cronExpression string
		duration       string
		errMsg         string
	}{
		{
			name:           "invalid cron expression, valid duration",
			cronExpression: "* * * * * *", // invalid expression, too many *s
			duration:       "30m",
			errMsg:         "cannot validate grace duration",
		},
		{
			name:           "valid cron expression, invalid duration",
			cronExpression: "0 * * * *", // every hr
			duration:       "",
			errMsg:         "invalid duration",
		},
		// basic case that is handled more in depth in the TestValidateGraceDuration cases
		{
			name:           "valid cron expression, valid duration",
			cronExpression: "0 * * * *", // every hr
			duration:       "30m",
			errMsg:         "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			schedule := UpdateSchedule{
				At:                 tt.cronExpression,
				StartGraceDuration: tt.duration,
			}

			errs := schedule.Validate()
			if tt.errMsg != "" {
				require.Condition(func() bool {
					for _, err := range errs {
						if strings.Contains(err.Error(), tt.errMsg) {
							return true
						}
					}
					return false
				})
				return
			}
			require.Empty(errs)
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

	quadletContainerSpec := `[Container]
Image=quay.io/podman/hello:latest
PublishPort=8080:80`

	quadletBuildSpec := `[Build]
ContextDir=/tmp/build`

	base64Content := base64.StdEncoding.EncodeToString([]byte(composeSpec))
	tests := []struct {
		name          string
		appType       AppType
		spec          InlineApplicationProviderSpec
		fleetTemplate bool
		expectErr     bool
	}{
		{
			name:    "valid plain content",
			appType: AppTypeCompose,
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
			name:    "duplicate path",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr("abc"), ContentEncoding: &plain},
					{Path: "docker-compose.yaml", Content: lo.ToPtr("def"), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name:    "invalid base64 content",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "podman-compose.yaml", Content: lo.ToPtr(composeSpec), ContentEncoding: &base64Enc},
				},
			},
			expectErr: true,
		},
		{
			name:    "valid base64 content",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "podman-compose.yml", Content: &base64Content, ContentEncoding: &base64Enc},
				},
			},
			expectErr: false,
		},
		{
			name:    "unknown encoding",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr(composeSpec), ContentEncoding: lo.ToPtr(EncodingType("unknown"))},
				},
			},
			expectErr: true,
		},
		{
			name:    "invalid compose path",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "invalid-compose.yaml", Content: lo.ToPtr(composeSpec), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name:    "invalid use of container_name",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr(composeInvalidSpecContainerName), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name:    "invalid container short name",
			appType: AppTypeCompose,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "docker-compose.yaml", Content: lo.ToPtr(composeInvalidSpecContainerShortname), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
		{
			name:    "valid quadlet container file",
			appType: AppTypeQuadlet,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "app.container", Content: lo.ToPtr(quadletContainerSpec), ContentEncoding: &plain},
				},
			},
			expectErr: false,
		},
		{
			name:    "unsupported quadlet build type",
			appType: AppTypeQuadlet,
			spec: InlineApplicationProviderSpec{
				Inline: []ApplicationContent{
					{Path: "app.build", Content: lo.ToPtr(quadletBuildSpec), ContentEncoding: &plain},
				},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.spec.Validate(tt.appType, tt.fleetTemplate)
			if tt.expectErr {
				require.NotEmpty(t, errs, "expected errors but got none")
			} else {
				require.Empty(t, errs, "expected no errors but got: %v", errs)
			}
		})
	}
}

func TestInlineApplicationProviderSpecValidateQuadletNames(t *testing.T) {
	tests := []struct {
		name       string
		inline     []ApplicationContent
		wantErr    bool
		wantSubstr string
	}{
		{
			name: "duplicate volume names",
			inline: []ApplicationContent{
				{
					Path:    "test.volume",
					Content: lo.ToPtr("[Volume]\nVolumeName=testdata\nDriver=local\n"),
				},
				{
					Path:    "test2.volume",
					Content: lo.ToPtr("[Volume]\nVolumeName=testdata\nDriver=local\n"),
				},
			},
			wantErr:    true,
			wantSubstr: `duplicate VolumeName "testdata"`,
		},
		{
			name: "unique names across types",
			inline: []ApplicationContent{
				{
					Path:    "app.container",
					Content: lo.ToPtr("[Container]\nContainerName=shared\nImage=quay.io/podman/hello:latest\n"),
				},
				{
					Path:    "net.network",
					Content: lo.ToPtr("[Network]\nNetworkName=shared\n"),
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := InlineApplicationProviderSpec{Inline: tt.inline}
			errs := spec.Validate(AppTypeQuadlet, false)
			if tt.wantErr {
				require.NotEmpty(t, errs, "expected duplicate error, got none")
				found := false
				for _, err := range errs {
					if strings.Contains(err.Error(), tt.wantSubstr) {
						found = true
						break
					}
				}
				require.True(t, found, "expected error containing %q, got %v", tt.wantSubstr, errs)
			} else {
				for _, err := range errs {
					require.NotContains(t, err.Error(), "duplicate", "unexpected duplicate error: %v", err)
				}
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

func TestValidateConfigs(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name    string
		configs []ConfigProviderSpec
		wantErr bool
	}{
		{
			name:    "duplicate http paths",
			configs: []ConfigProviderSpec{newHttpConfigProviderSpec("/dupe"), newHttpConfigProviderSpec("/dupe")},
			wantErr: true,
		},
		{
			name:    "duplicate inline paths",
			configs: []ConfigProviderSpec{newInlineConfigProviderSpec([]string{"/dupe", "/dupe"})},
			wantErr: true,
		},
		{
			name:    "http vs inline same path",
			configs: []ConfigProviderSpec{newHttpConfigProviderSpec("/dupe"), newInlineConfigProviderSpec([]string{"/dupe"})},
			wantErr: true,
		},
		{
			name:    "http vs multiple inline same path",
			configs: []ConfigProviderSpec{newHttpConfigProviderSpec("/dupe"), newInlineConfigProviderSpec([]string{"/new", "/dupe"})},
			wantErr: true,
		},
		{
			name:    "all unique",
			configs: []ConfigProviderSpec{newHttpConfigProviderSpec("/new"), newInlineConfigProviderSpec([]string{"/new2"})},
			wantErr: false,
		},
		{
			name:    "empty configs",
			configs: []ConfigProviderSpec{},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateConfigs(tt.configs, true)
			if tt.wantErr {
				require.NotEmpty(errs, "expected errors but got none")
				return
			}
			require.Empty(errs, "expected no errors but got: %v", errs)
		})
	}
}

func newHttpConfigProviderSpec(path string) ConfigProviderSpec {
	var provider ConfigProviderSpec
	spec := HttpConfigProviderSpec{
		Name: "default-provider",
		HttpRef: struct {
			FilePath   string  `json:"filePath"`
			Repository string  `json:"repository"`
			Suffix     *string `json:"suffix,omitempty"`
		}{
			FilePath:   path,
			Repository: "default-repo",
			Suffix:     nil,
		},
	}
	_ = provider.FromHttpConfigProviderSpec(spec)
	return provider
}

func newInlineConfigProviderSpec(paths []string) ConfigProviderSpec {
	var provider ConfigProviderSpec
	var inlines []FileSpec

	for _, path := range paths {
		inlines = append(inlines, FileSpec{
			Path: path,
		})
	}

	spec := InlineConfigProviderSpec{
		Name:   "default-inline-provider",
		Inline: inlines,
	}

	_ = provider.FromInlineConfigProviderSpec(spec)
	return provider
}

func TestValidateApplications(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		apps          []ApplicationProviderSpec
		fleetTemplate bool
		wantErrs      []string
	}{
		{
			name: "duplicate volume name in single application",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app1", "quay.io/app/image:1", "quay.io/vol/image:1", "vol1", "vol1"),
			},
			wantErrs: []string{"duplicate volume name for application"},
		},
		{
			name: "duplicate application name",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app1", "quay.io/app/image:1", "quay.io/vol/image:1", "vol1"),
				newTestApplication(require, "app1", "quay.io/app/image:2", "quay.io/vol/image:1", "vol2"),
			},
			wantErrs: []string{"duplicate application name"},
		},
		{
			name: "duplicate volume name across multiple applications",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app1", "quay.io/app/image:1", "quay.io/vol/image:1", "vol1"),
				newTestApplication(require, "app2", "quay.io/app/image:2", "quay.io/vol/image:1", "vol1"),
			},
		},
		{
			name: "invalid volume name",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app1", "quay.io/app/image:1", "quay.io/vol/image:1", "vol@1"),
			},
			wantErrs: []string{"spec.applications[app1].volumes[0].name: Invalid value"},
		},

		{
			name: "invalid application name",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app@1", "quay.io/app/image:1", "quay.io/vol/image:1", "vol1"),
			},
			wantErrs: []string{"spec.applications[].name: Invalid value"},
		},
		{
			name: "invalid application image",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app1", "_invalid-app", "quay.io/vol/image:1", "vol1"),
			},
			wantErrs: []string{"spec.applications[app1].image: Invalid value"},
		},
		{
			name: "invalid application volume image",
			apps: []ApplicationProviderSpec{
				newTestApplication(require, "app1", "quay.io/app/image:1", "_invalid-vol", "vol1"),
			},
			wantErrs: []string{"spec.applications[app1].volumes[0].image.reference"},
		},
		{
			name: "container app with image volume - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeContainer, "quay.io/app/image:1", createImageVolume(t, "vol1", "quay.io/vol/image:1")),
			},
			wantErrs: []string{"image application volume provider invalid for app type: container"},
		},
		{
			name: "container app with mount volume - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeContainer, "quay.io/app/image:1", createMountVolume(t, "vol1", "/host:/container")),
			},
		},
		{
			name: "container app with image-mount volume - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeContainer, "quay.io/app/image:1", createImageMountVolume(t, "vol1", "quay.io/vol/image:1", "/host:/container")),
			},
		},
		{
			name: "compose app with image volume - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeCompose, "quay.io/app/image:1", createImageVolume(t, "vol1", "quay.io/vol/image:1")),
			},
		},
		{
			name: "compose app with mount volume - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeCompose, "quay.io/app/image:1", createMountVolume(t, "vol1", "/host:/container")),
			},
			wantErrs: []string{"mount application volume provider invalid for app type: compose"},
		},
		{
			name: "compose app with image-mount volume - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeCompose, "quay.io/app/image:1", createImageMountVolume(t, "vol1", "quay.io/vol/image:1", "/host:/container")),
			},
			wantErrs: []string{"image mount application volume provider invalid for app type: compose"},
		},
		{
			name: "quadlet app with image volume - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeQuadlet, "quay.io/app/image:1", createImageVolume(t, "vol1", "quay.io/vol/image:1")),
			},
		},
		{
			name: "quadlet app with mount volume - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeQuadlet, "quay.io/app/image:1", createMountVolume(t, "vol1", "/host:/container")),
			},
			wantErrs: []string{"mount application volume provider invalid for app type: quadlet"},
		},
		{
			name: "quadlet app with image-mount volume - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithVolume(require, "app1", AppTypeQuadlet, "quay.io/app/image:1", createImageMountVolume(t, "vol1", "quay.io/vol/image:1", "/host:/container")),
			},
			wantErrs: []string{"image mount application volume provider invalid for app type: quadlet"},
		},
		{
			name: "container app with ports - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", []string{"8080:80"}, nil),
			},
		},
		{
			name: "container app with ports out of range",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", []string{"0:65536"}, nil),
			},
			wantErrs: []string{"must be a number in the valid port range", "must be a number in the valid port range"},
		},
		{
			name: "container app with resources - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Cpu:    lo.ToPtr("1"),
						Memory: lo.ToPtr("512m"),
					},
				}),
			},
		},
		{
			name: "container app with resources - valid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Cpu:    lo.ToPtr("1e3"),
						Memory: lo.ToPtr("512000000"),
					},
				}),
			},
		},
		{
			name: "compose app with ports - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeCompose, "quay.io/app/image:1", []string{"8080:80"}, nil),
			},
			wantErrs: []string{"ports can only be defined for container applications"},
		},
		{
			name: "compose app with resources - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeCompose, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Cpu:    lo.ToPtr("1"),
						Memory: lo.ToPtr("512m"),
					},
				}),
			},
			wantErrs: []string{"resources can only be defined for container applications"},
		},
		{
			name: "quadlet app with ports - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeQuadlet, "quay.io/app/image:1", []string{"8080:80"}, nil),
			},
			wantErrs: []string{"ports can only be defined for container applications"},
		},
		{
			name: "quadlet app with resources - invalid",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeQuadlet, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Cpu:    lo.ToPtr("1"),
						Memory: lo.ToPtr("512m"),
					},
				}),
			},
			wantErrs: []string{"resources can only be defined for container applications"},
		},
		{
			name: "container app with valid port format",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", []string{"8080:80", "443:443"}, nil),
			},
		},
		{
			name: "container app with invalid port format - no colon",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", []string{"8080"}, nil),
			},
			wantErrs: []string{"must be in format 'portnumber:portnumber'"},
		},
		{
			name: "container app with invalid port format - not numbers",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", []string{"abc:def"}, nil),
			},
			wantErrs: []string{"must be in format 'portnumber:portnumber'"},
		},
		{
			name: "container app with invalid port format - too many colons",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", []string{"8080:80:90"}, nil),
			},
			wantErrs: []string{"must be in format 'portnumber:portnumber'"},
		},
		{
			name: "container app with valid CPU formats",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Cpu: lo.ToPtr("1.5"),
					},
				}),
			},
		},
		{
			name: "container app with invalid CPU format",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Cpu: lo.ToPtr("not-a-number"),
					},
				}),
			},
			wantErrs: []string{"must be a valid number"},
		},
		{
			name: "container app with valid memory formats",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Memory: lo.ToPtr("512m"),
					},
				}),
			},
		},
		{
			name: "container app with valid memory format - bytes",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Memory: lo.ToPtr("1024b"),
					},
				}),
			},
		},
		{
			name: "container app with valid memory format - kibibytes",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Memory: lo.ToPtr("256k"),
					},
				}),
			},
		},
		{
			name: "container app with valid memory format - gibibytes",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Memory: lo.ToPtr("2g"),
					},
				}),
			},
		},
		{
			name: "container app with invalid memory format - wrong unit",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Memory: lo.ToPtr("1gb"),
					},
				}),
			},
			wantErrs: []string{"must be in format 'number[unit]' where unit is b, k, m, or g"},
		},
		{
			name: "container app with invalid memory format - uppercase unit",
			apps: []ApplicationProviderSpec{
				newTestApplicationWithPortsAndResources(require, "app1", AppTypeContainer, "quay.io/app/image:1", nil, &ApplicationResources{
					Limits: &ApplicationResourceLimits{
						Memory: lo.ToPtr("512M"),
					},
				}),
			},
			wantErrs: []string{"must be in format 'number[unit]' where unit is b, k, m, or g"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotErrs := validateApplications(tt.apps, tt.fleetTemplate)
			if len(tt.wantErrs) > 0 {
				require.Len(gotErrs, len(tt.wantErrs), "expected %d errors but got %d", len(tt.wantErrs), len(gotErrs))
				for i, wantErr := range tt.wantErrs {
					require.Contains(gotErrs[i].Error(), wantErr, "expected error at index %d to contain %q, got: %v", i, wantErr, gotErrs[i])
				}
			} else {
				require.Empty(gotErrs, "expected no errors but got: %v", gotErrs)
			}
		})
	}
}

func newTestApplication(require *require.Assertions, name string, appImage, volImage string, volumeNames ...string) ApplicationProviderSpec {
	app := ApplicationProviderSpec{
		Name:    lo.ToPtr(name),
		AppType: AppTypeCompose,
	}

	var volumes []ApplicationVolume
	for _, volName := range volumeNames {
		imageVolumeProvider := ImageVolumeProviderSpec{
			Image: ImageVolumeSource{
				Reference:  volImage,
				PullPolicy: lo.ToPtr(PullIfNotPresent), // pull policy is validated by openapi
			},
		}

		volumeProvider := ApplicationVolume{Name: volName}
		require.NoError(volumeProvider.FromImageVolumeProviderSpec(imageVolumeProvider))
		volumes = append(volumes, volumeProvider)
	}

	provider := ImageApplicationProviderSpec{
		Image:   appImage,
		Volumes: &volumes,
	}
	require.NoError(app.FromImageApplicationProviderSpec(provider))

	return app
}

func newTestApplicationWithVolume(require *require.Assertions, name string, appType AppType, appImage string, volume ApplicationVolume) ApplicationProviderSpec {
	app := ApplicationProviderSpec{
		Name:    lo.ToPtr(name),
		AppType: appType,
	}

	volumes := []ApplicationVolume{volume}

	provider := ImageApplicationProviderSpec{
		Image:   appImage,
		Volumes: &volumes,
	}
	require.NoError(app.FromImageApplicationProviderSpec(provider))

	return app
}

func newTestApplicationWithPortsAndResources(require *require.Assertions, name string, appType AppType, appImage string, ports []string, resources *ApplicationResources) ApplicationProviderSpec {
	app := ApplicationProviderSpec{
		Name:    lo.ToPtr(name),
		AppType: appType,
	}

	var appPorts *[]ApplicationPort
	if len(ports) > 0 {
		appPorts = &ports
	}

	provider := ImageApplicationProviderSpec{
		Image:     appImage,
		Ports:     appPorts,
		Resources: resources,
	}
	require.NoError(app.FromImageApplicationProviderSpec(provider))

	return app
}

func TestValidateVolumeAppTypeCompatibility(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name          string
		appType       AppType
		volume        func(t *testing.T) ApplicationVolume
		fleetTemplate bool
		expectErr     bool
		errorSubstr   string
	}{
		{
			name:          "image volume with container app - invalid",
			appType:       AppTypeContainer,
			volume:        func(t *testing.T) ApplicationVolume { return createImageVolume(t, "vol1", "quay.io/test/image:v1") },
			fleetTemplate: false,
			expectErr:     true,
			errorSubstr:   "image application volume provider invalid for app type: container",
		},
		{
			name:          "image volume with compose app - valid",
			appType:       AppTypeCompose,
			volume:        func(t *testing.T) ApplicationVolume { return createImageVolume(t, "vol1", "quay.io/test/image:v1") },
			fleetTemplate: false,
			expectErr:     false,
		},
		{
			name:          "image volume with quadlet app - valid",
			appType:       AppTypeQuadlet,
			volume:        func(t *testing.T) ApplicationVolume { return createImageVolume(t, "vol1", "quay.io/test/image:v1") },
			fleetTemplate: false,
			expectErr:     false,
		},
		{
			name:          "mount volume with container app - valid",
			appType:       AppTypeContainer,
			volume:        func(t *testing.T) ApplicationVolume { return createMountVolume(t, "vol1", "/host:/container") },
			fleetTemplate: false,
			expectErr:     false,
		},
		{
			name:          "mount volume with compose app - invalid",
			appType:       AppTypeCompose,
			volume:        func(t *testing.T) ApplicationVolume { return createMountVolume(t, "vol1", "/host:/container") },
			fleetTemplate: false,
			expectErr:     true,
			errorSubstr:   "mount application volume provider invalid for app type: compose",
		},
		{
			name:          "mount volume with quadlet app - invalid",
			appType:       AppTypeQuadlet,
			volume:        func(t *testing.T) ApplicationVolume { return createMountVolume(t, "vol1", "/host:/container") },
			fleetTemplate: false,
			expectErr:     true,
			errorSubstr:   "mount application volume provider invalid for app type: quadlet",
		},
		{
			name:    "image-mount volume with container app - valid",
			appType: AppTypeContainer,
			volume: func(t *testing.T) ApplicationVolume {
				return createImageMountVolume(t, "vol1", "quay.io/test/image:v1", "/host:/container")
			},
			fleetTemplate: false,
			expectErr:     false,
		},
		{
			name:    "image-mount volume with compose app - invalid",
			appType: AppTypeCompose,
			volume: func(t *testing.T) ApplicationVolume {
				return createImageMountVolume(t, "vol1", "quay.io/test/image:v1", "/host:/container")
			},
			fleetTemplate: false,
			expectErr:     true,
			errorSubstr:   "image mount application volume provider invalid for app type: compose",
		},
		{
			name:    "image-mount volume with quadlet app - invalid",
			appType: AppTypeQuadlet,
			volume: func(t *testing.T) ApplicationVolume {
				return createImageMountVolume(t, "vol1", "quay.io/test/image:v1", "/host:/container")
			},
			fleetTemplate: false,
			expectErr:     true,
			errorSubstr:   "image mount application volume provider invalid for app type: quadlet",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vol := tt.volume(t)
			path := "spec.applications[test].volumes[0]"
			errs := validateVolume(vol, path, tt.fleetTemplate, tt.appType)

			if tt.expectErr {
				require.NotEmpty(errs, "expected errors but got none")
				if tt.errorSubstr != "" {
					found := false
					for _, err := range errs {
						if strings.Contains(err.Error(), tt.errorSubstr) {
							found = true
							break
						}
					}
					require.True(found, "expected error containing %q, got errors: %v", tt.errorSubstr, errs)
				}
			} else {
				require.Empty(errs, "expected no errors but got: %v", errs)
			}
		})
	}
}

func TestValidateVolumeReclaimPolicy(t *testing.T) {
	require := require.New(t)

	t.Run("delete reclaim policy unsupported", func(t *testing.T) {
		vol := createImageVolume(t, "data", "quay.io/test/image:v1")
		policy := ApplicationVolumeReclaimPolicy("Delete")
		vol.ReclaimPolicy = &policy

		errs := validateVolume(vol, "spec.applications[test].volumes[0]", false, AppTypeCompose)
		require.NotEmpty(errs)
		require.Contains(errs[0].Error(), "only \"Retain\" is supported")
	})

	t.Run("invalid reclaim policy value", func(t *testing.T) {
		vol := createImageVolume(t, "data", "quay.io/test/image:v1")
		policy := ApplicationVolumeReclaimPolicy("Recycle")
		vol.ReclaimPolicy = &policy

		errs := validateVolume(vol, "spec.applications[test].volumes[0]", false, AppTypeCompose)
		require.Len(errs, 1)
		require.Contains(errs[0].Error(), "reclaimPolicy")
	})
}

func TestValidateResourceMonitor(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name      string
		resources []ResourceMonitor
		wantErrs  []error
	}{
		{
			name: "valid unique monitor types",
			resources: []ResourceMonitor{
				createCPUMonitor(t),
				createDiskMonitor(t),
				createMemoryMonitor(t),
			},
			wantErrs: nil,
		},
		{
			name: "duplicate CPU monitor types",
			resources: []ResourceMonitor{
				createCPUMonitor(t),
				createCPUMonitor(t),
			},
			wantErrs: []error{ErrDuplicateMonitorType},
		},
		{
			name: "duplicate Disk monitor types",
			resources: []ResourceMonitor{
				createDiskMonitor(t),
				createDiskMonitor(t),
			},
			wantErrs: []error{ErrDuplicateMonitorType},
		},
		{
			name: "duplicate Memory monitor types",
			resources: []ResourceMonitor{
				createMemoryMonitor(t),
				createMemoryMonitor(t),
			},
			wantErrs: []error{ErrDuplicateMonitorType},
		},
		{
			name: "multiple duplicates",
			resources: []ResourceMonitor{
				createCPUMonitor(t),
				createCPUMonitor(t),
				createDiskMonitor(t),
				createDiskMonitor(t),
			},
			wantErrs: []error{ErrDuplicateMonitorType, ErrDuplicateMonitorType},
		},
		{
			name:      "empty resources array",
			resources: []ResourceMonitor{},
			wantErrs:  nil,
		},
		{
			name: "single monitor type",
			resources: []ResourceMonitor{
				createCPUMonitor(t),
			},
			wantErrs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateResourceMonitor(tt.resources)
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

func TestResourceMonitorValidate_CPUPathField(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name     string
		monitor  ResourceMonitor
		wantErrs []error
	}{
		{
			name:     "valid CPU monitor without path",
			monitor:  createCPUMonitor(t),
			wantErrs: nil,
		},
		{
			name:     "invalid CPU monitor with path field",
			monitor:  createCPUMonitorWithPath(t),
			wantErrs: []error{ErrInvalidCPUMonitorField},
		},
		{
			name:     "valid Disk monitor with path",
			monitor:  createDiskMonitor(t),
			wantErrs: nil,
		},
		{
			name:     "valid Memory monitor without path",
			monitor:  createMemoryMonitor(t),
			wantErrs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.monitor.Validate()
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

func TestDeviceSpecValidate_ResourceMonitors(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name         string
		resources    *[]ResourceMonitor
		wantErrs     []error
		errorStrings []string
	}{
		{
			name:      "valid mixed monitors",
			resources: &[]ResourceMonitor{createCPUMonitor(t), createDiskMonitor(t)},
			wantErrs:  nil,
		},
		{
			name:         "duplicate monitors in DeviceSpec",
			resources:    &[]ResourceMonitor{createCPUMonitor(t), createCPUMonitor(t)},
			errorStrings: []string{"duplicate monitorType in resources: CPU"},
		},
		{
			name:         "CPU monitor with invalid path field",
			resources:    &[]ResourceMonitor{createCPUMonitorWithPath(t)},
			errorStrings: []string{"CPU monitors cannot have a path field"},
		},
		{
			name: "combination of duplicate and invalid path",
			resources: &[]ResourceMonitor{
				createCPUMonitorWithPath(t),
				createCPUMonitorWithPath(t),
			},
			errorStrings: []string{"CPU monitors cannot have a path field", "CPU monitors cannot have a path field", "duplicate monitorType in resources: CPU"},
		},
		{
			name:      "nil resources",
			resources: nil,
			wantErrs:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := DeviceSpec{
				Resources: tt.resources,
			}
			errs := spec.Validate(false)

			if len(tt.wantErrs) > 0 {
				require.Len(errs, len(tt.wantErrs), "expected %d errors but got %d", len(tt.wantErrs), len(errs))
				for i, wantErr := range tt.wantErrs {
					require.ErrorIs(errs[i], wantErr, "expected error at index %d to be %v, got: %v", i, wantErr, errs[i])
				}
			} else if len(tt.errorStrings) > 0 {
				require.Len(errs, len(tt.errorStrings), "expected %d errors but got %d", len(tt.errorStrings), len(errs))
				for i, errStr := range tt.errorStrings {
					require.Contains(errs[i].Error(), errStr, "expected error at index %d to contain %q, got: %v", i, errStr, errs[i])
				}
			} else {
				require.Empty(errs, "expected no errors but got: %v", errs)
			}
		})
	}
}

// Helper functions to create test ResourceMonitor instances

func createCPUMonitor(t *testing.T) ResourceMonitor {
	var monitor ResourceMonitor
	err := monitor.FromCpuResourceMonitorSpec(CpuResourceMonitorSpec{
		MonitorType:      "CPU",
		SamplingInterval: "30s",
		AlertRules: []ResourceAlertRule{
			{
				Severity:    ResourceAlertSeverityTypeWarning,
				Percentage:  75,
				Duration:    "5m",
				Description: "CPU usage above 75%",
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create CPU monitor: %v", err)
	}
	return monitor
}

func createCPUMonitorWithPath(t *testing.T) ResourceMonitor {
	// We need to create a ResourceMonitor with path field manually
	// since FromCpuResourceMonitorSpec doesn't accept path field
	cpuSpec := CpuResourceMonitorSpec{
		MonitorType:      "CPU",
		SamplingInterval: "30s",
		AlertRules: []ResourceAlertRule{
			{
				Severity:    ResourceAlertSeverityTypeWarning,
				Percentage:  75,
				Duration:    "5m",
				Description: "CPU usage above 75%",
			},
		},
	}

	// Create monitor first without path
	var monitor ResourceMonitor
	err := monitor.FromCpuResourceMonitorSpec(cpuSpec)
	if err != nil {
		t.Fatalf("Failed to create CPU monitor: %v", err)
	}

	// Now manually inject the path field into the raw JSON
	// This simulates what would happen if someone manually included a path field
	rawWithPath := `{
		"monitorType": "CPU",
		"samplingInterval": "30s", 
		"path": "/invalid/path/for/cpu",
		"alertRules": [
			{
				"severity": "Warning",
				"percentage": 75,
				"duration": "5m",
				"description": "CPU usage above 75%"
			}
		]
	}`
	monitor.union = []byte(rawWithPath)

	return monitor
}

func createDiskMonitor(t *testing.T) ResourceMonitor {
	var monitor ResourceMonitor
	err := monitor.FromDiskResourceMonitorSpec(DiskResourceMonitorSpec{
		MonitorType:      "Disk",
		Path:             "/var/data",
		SamplingInterval: "30s",
		AlertRules: []ResourceAlertRule{
			{
				Severity:    ResourceAlertSeverityTypeCritical,
				Percentage:  90,
				Duration:    "3m",
				Description: "Disk usage above 90%",
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create Disk monitor: %v", err)
	}
	return monitor
}

func createMemoryMonitor(t *testing.T) ResourceMonitor {
	var monitor ResourceMonitor
	err := monitor.FromMemoryResourceMonitorSpec(MemoryResourceMonitorSpec{
		MonitorType:      "Memory",
		SamplingInterval: "30s",
		AlertRules: []ResourceAlertRule{
			{
				Severity:    ResourceAlertSeverityTypeInfo,
				Percentage:  80,
				Duration:    "10m",
				Description: "Memory usage above 80%",
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create Memory monitor: %v", err)
	}
	return monitor
}

func createImageVolume(t *testing.T, name, imageRef string) ApplicationVolume {
	var volume ApplicationVolume
	volume.Name = name
	imageVolumeProvider := ImageVolumeProviderSpec{
		Image: ImageVolumeSource{
			Reference:  imageRef,
			PullPolicy: lo.ToPtr(PullIfNotPresent),
		},
	}
	err := volume.FromImageVolumeProviderSpec(imageVolumeProvider)
	if err != nil {
		t.Fatalf("Failed to create image volume: %v", err)
	}
	return volume
}

func createMountVolume(t *testing.T, name, path string) ApplicationVolume {
	var volume ApplicationVolume
	volume.Name = name
	mountVolumeProvider := MountVolumeProviderSpec{
		Mount: VolumeMount{
			Path: path,
		},
	}
	err := volume.FromMountVolumeProviderSpec(mountVolumeProvider)
	if err != nil {
		t.Fatalf("Failed to create mount volume: %v", err)
	}
	return volume
}

func createImageMountVolume(t *testing.T, name, imageRef, path string) ApplicationVolume {
	var volume ApplicationVolume
	volume.Name = name
	imageMountVolumeProvider := ImageMountVolumeProviderSpec{
		Image: ImageVolumeSource{
			Reference:  imageRef,
			PullPolicy: lo.ToPtr(PullIfNotPresent),
		},
		Mount: VolumeMount{
			Path: path,
		},
	}
	err := volume.FromImageMountVolumeProviderSpec(imageMountVolumeProvider)
	if err != nil {
		t.Fatalf("Failed to create image-mount volume: %v", err)
	}
	return volume
}

func TestValidateApplicationContentQuadlet(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		content       string
		path          string
		wantErrCount  int
		wantErrSubstr string
	}{
		{
			name: "valid container quadlet with OCI image",
			content: `[Container]
Image=quay.io/podman/hello:latest
PublishPort=8080:80`,
			path:         "test.container",
			wantErrCount: 0,
		},
		{
			name: "valid container quadlet with .image reference",
			content: `[Container]
Image=my-app.image
PublishPort=8080:80`,
			path:         "test.container",
			wantErrCount: 0,
		},
		{
			name: "valid volume quadlet",
			content: `[Volume]
Image=quay.io/containers/volume:latest`,
			path:         "test.volume",
			wantErrCount: 0,
		},
		{
			name: "valid image quadlet",
			content: `[Image]
Image=quay.io/fedora/fedora:latest`,
			path:         "test.image",
			wantErrCount: 0,
		},
		{
			name: "valid network quadlet",
			content: `[Network]
Subnet=10.0.0.0/24`,
			path:         "test.network",
			wantErrCount: 0,
		},
		{
			name: "valid pod quadlet",
			content: `[Pod]
PodName=my-pod`,
			path:         "test.pod",
			wantErrCount: 0,
		},
		{
			name: "invalid quadlet - parsing error (bad INI)",
			content: `[Container
Image=test`,
			path:          "test.container",
			wantErrCount:  1,
			wantErrSubstr: "parse quadlet spec",
		},
		{
			name: "invalid quadlet - .build reference",
			content: `[Container]
Image=my-app.build`,
			path:          "test.container",
			wantErrCount:  1,
			wantErrSubstr: ".build quadlet types are unsupported",
		},
		{
			name: "invalid quadlet - short OCI reference",
			content: `[Container]
Image=nginx:latest`,
			path:          "test.container",
			wantErrCount:  1,
			wantErrSubstr: "container.image",
		},
		{
			name: "invalid quadlet - image type missing Image key",
			content: `[Image]
Label=test`,
			wantErrCount:  1,
			path:          "test.image",
			wantErrSubstr: ".image quadlet must have an Image key",
		},
		{
			name: "invalid quadlet - unsupported Build type",
			content: `[Build]
ContextDir=/tmp/build`,
			path:          "test.build",
			wantErrCount:  1,
			wantErrSubstr: "parse quadlet spec",
		},
		{
			name: "non-quadlet systemd file",
			content: `[Service]
Type=simple
ExecStart=/usr/bin/myapp`,
			path:          "test.container",
			wantErrCount:  1,
			wantErrSubstr: "non quadlet type",
		},
		{
			name:         "non-quadlet config file",
			content:      `{"key": "val"}`,
			path:         "conf.json",
			wantErrCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := []byte(tt.content)
			validator := quadletValidator{quadlets: make(map[string]*common.QuadletReferences)}
			errs := validator.ValidateContents(tt.path, content, false)

			require.Len(errs, tt.wantErrCount, "expected %d errors, got %d: %v", tt.wantErrCount, len(errs), errs)
			if tt.wantErrSubstr != "" && len(errs) > 0 {
				require.Contains(errs[0].Error(), tt.wantErrSubstr)
			}
		})
	}
}

// contextWithSuperAdmin creates a context with a super admin mapped identity
func contextWithSuperAdmin(ctx context.Context) context.Context {
	mappedIdentity := identity.NewMappedIdentity("admin", "admin-uid", nil, nil, true, nil)
	return context.WithValue(ctx, consts.MappedIdentityCtxKey, mappedIdentity)
}

func TestAuthStaticRoleAssignment_Validate(t *testing.T) {
	require := require.New(t)
	baseCtx := context.Background()
	superAdminCtx := contextWithSuperAdmin(baseCtx)

	tests := []struct {
		name       string
		ctx        context.Context
		assignment AuthStaticRoleAssignment
		wantErrs   int
		errSubstrs []string
	}{
		{
			name: "empty roles list",
			ctx:  baseCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{},
			},
			wantErrs:   1,
			errSubstrs: []string{"at least one role is required"},
		},
		{
			name: "invalid custom role",
			ctx:  superAdminCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{ExternalRoleAdmin, ExternalRoleViewer, "custom-role"},
			},
			wantErrs:   1,
			errSubstrs: []string{"is not a valid role"},
		},
		{
			name: "valid known roles",
			ctx:  superAdminCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{ExternalRoleAdmin, ExternalRoleViewer, ExternalRoleOperator},
			},
			wantErrs: 0,
		},
		{
			name: "invalid role",
			ctx:  superAdminCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{ExternalRoleAdmin, "invalid-role"},
			},
			wantErrs:   1,
			errSubstrs: []string{"is not a valid role"},
		},
		{
			name: "empty role string",
			ctx:  superAdminCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{ExternalRoleAdmin, ""},
			},
			wantErrs:   2,
			errSubstrs: []string{"cannot be empty", "is not a valid role"},
		},
		{
			name: "all known external roles",
			ctx:  superAdminCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{ExternalRoleAdmin, ExternalRoleOrgAdmin, ExternalRoleOperator, ExternalRoleViewer, ExternalRoleInstaller},
			},
			wantErrs: 0,
		},
		{
			name: "multiple invalid roles",
			ctx:  baseCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{"invalid-role-1", "invalid-role-2"},
			},
			wantErrs:   2,
			errSubstrs: []string{"is not a valid role", "is not a valid role"},
		},
		{
			name: "mix of valid and invalid roles",
			ctx:  superAdminCtx,
			assignment: AuthStaticRoleAssignment{
				Type:  AuthStaticRoleAssignmentTypeStatic,
				Roles: []string{ExternalRoleAdmin, "invalid-role", ExternalRoleViewer},
			},
			wantErrs:   1,
			errSubstrs: []string{"is not a valid role"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := tt.assignment.Validate(tt.ctx)
			require.Len(errs, tt.wantErrs, "expected %d errors, got %d: %v", tt.wantErrs, len(errs), errs)

			if len(tt.errSubstrs) > 0 {
				require.Equal(len(tt.errSubstrs), len(errs), "number of error substrings (%d) must match number of actual errors (%d)", len(tt.errSubstrs), len(errs))
				for i, substr := range tt.errSubstrs {
					if i < len(errs) {
						require.Contains(errs[i].Error(), substr, "error at index %d should contain %q", i, substr)
					}
				}
			}
		})
	}
}

func TestInlineConfigProviderSpec_Validate_ForbiddenPaths(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"reject /var/lib/flightctl", "/var/lib/flightctl/data.txt", true},
		{"reject /usr/lib/flightctl", "/usr/lib/flightctl/binary", true},
		{"reject /etc/flightctl/certs", "/etc/flightctl/certs/ca.crt", true},
		{"reject /etc/flightctl/config.yaml", "/etc/flightctl/config.yaml", true},
		{"allow /etc/myapp", "/etc/myapp/config.txt", false},
		{"allow /etc/flightctl custom", "/etc/flightctl/custom.txt", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := InlineConfigProviderSpec{
				Name:   "test-config",
				Inline: []FileSpec{{Path: tt.path, Content: "test", Mode: lo.ToPtr(0644)}},
			}

			errs := spec.Validate(false)

			if tt.wantErr {
				require.NotEmpty(t, errs)
			} else {
				require.Empty(t, errs)
			}
		})
	}
}

func TestHttpConfigProviderSpec_Validate_ForbiddenPaths(t *testing.T) {
	tests := []struct {
		name     string
		filePath string
		wantErr  bool
	}{
		{"reject /var/lib/flightctl", "/var/lib/flightctl/data.txt", true},
		{"reject /etc/flightctl/certs", "/etc/flightctl/certs/key.pem", true},
		{"allow /etc/myapp", "/etc/myapp/config.yaml", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := HttpConfigProviderSpec{
				Name: "test-http-config",
				HttpRef: struct {
					FilePath   string  `json:"filePath"`
					Repository string  `json:"repository"`
					Suffix     *string `json:"suffix,omitempty"`
				}{
					FilePath:   tt.filePath,
					Repository: "test-repo",
				},
			}

			errs := spec.Validate(false)

			if tt.wantErr {
				require.NotEmpty(t, errs)
			} else {
				require.Empty(t, errs)
			}
		})
	}
}

func TestKubernetesSecretProviderSpec_Validate_ForbiddenPaths(t *testing.T) {
	tests := []struct {
		name      string
		mountPath string
		wantErr   bool
	}{
		{"reject /etc/flightctl/certs", "/etc/flightctl/certs", true},
		{"reject /var/lib/flightctl", "/var/lib/flightctl/data", true},
		{"allow /etc/myapp", "/etc/myapp/secrets", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := KubernetesSecretProviderSpec{
				Name: "test-k8s-config",
				SecretRef: struct {
					MountPath string `json:"mountPath"`
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				}{
					MountPath: tt.mountPath,
					Name:      "test-secret",
					Namespace: "default",
				},
			}

			errs := spec.Validate(false)

			if tt.wantErr {
				require.NotEmpty(t, errs)
			} else {
				require.Empty(t, errs)
			}
		})
	}
}
