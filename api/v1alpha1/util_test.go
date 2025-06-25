package v1alpha1

import (
	"encoding/json"
	"fmt"
	"testing"
	"text/template"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestExecuteGoTemplateOnDevice(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name        string
		paramString string
		err         bool
		expect      string
	}{
		{
			name:        "no parameters",
			paramString: "hello world",
			err:         false,
			expect:      "hello world",
		},
		{
			name:        "simple name access",
			paramString: "hello {{ .metadata.name }} world",
			err:         false,
			expect:      "hello Name world",
		},
		{
			name:        "name access using Go struct syntax fails",
			paramString: "hello {{ .Metadata.Name }} world",
			err:         true,
		},
		{
			name:        "label access using Go struct syntax fails",
			paramString: "hello {{ .Metadata.Labels.key }} world",
			err:         true,
		},
		{
			name:        "accessing non-exposed field fails",
			paramString: "hello {{ .metadata.annotations.key }} world",
			err:         true,
		},
		{
			name:        "upper name",
			paramString: "Hello {{ upper .metadata.name }}",
			err:         false,
			expect:      "Hello NAME",
		},
		{
			name:        "upper label",
			paramString: "Hello {{ upper .metadata.labels.key }}",
			err:         false,
			expect:      "Hello VALUE",
		},
		{
			name:        "lower name",
			paramString: "Hello {{ lower .metadata.name }}",
			err:         false,
			expect:      "Hello name",
		},
		{
			name:        "lower label",
			paramString: "Hello {{ lower .metadata.labels.key }}",
			err:         false,
			expect:      "Hello value",
		},
		{
			name:        "replace name",
			paramString: "Hello {{ replace \"N\" \"G\" .metadata.name }}",
			err:         false,
			expect:      "Hello Game",
		},
		{
			name:        "replace label",
			paramString: "Hello {{ replace \"Va\" \"b\" .metadata.labels.key }}",
			err:         false,
			expect:      "Hello blue",
		},
		{
			name:        "index",
			paramString: "Hello {{ index .metadata.labels \"key\" }}",
			err:         false,
			expect:      "Hello Value",
		},
		{
			name:        "pipeline found key",
			paramString: "Hello {{ .metadata.labels.key | upper | replace \"VA\" \"B\"}}",
			err:         false,
			expect:      "Hello BLUE",
		},
		{
			name:        "pipeline default key not found",
			paramString: "Hello {{ getOrDefault .metadata.labels \"otherkey\" \"DEFAULT\" | lower | replace \"de\" \"my\"}}",
			err:         false,
			expect:      "Hello myfault",
		},
		{
			name:        "pipeline default key found",
			paramString: "Hello {{ getOrDefault .metadata.labels \"key\" \"DEFAULT\" | lower | replace \"de\" \"my\"}}",
			err:         false,
			expect:      "Hello value",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := fmt.Sprintf("test: %s", tt.name)
			tmpl, err := template.New("t").Option("missingkey=error").Funcs(GetGoTemplateFuncMap()).Parse(tt.paramString)
			require.NoError(err, msg)

			dev := &Device{
				Metadata: ObjectMeta{
					Name:   lo.ToPtr("Name"),
					Labels: &map[string]string{"key": "Value"},
				},
			}
			output, err := ExecuteGoTemplateOnDevice(tmpl, dev)
			if tt.err {
				require.Error(err, msg)
			} else {
				require.NoError(err, msg)
				require.Equal(tt.expect, output, msg)
			}
		})
	}
}

func TestDeviceSpecsAreEqual(t *testing.T) {
	tests := []struct {
		name   string
		spec1  DeviceSpec
		spec2  DeviceSpec
		expect bool
	}{
		{
			name:   "empty specs",
			spec1:  DeviceSpec{},
			spec2:  DeviceSpec{},
			expect: true,
		},
		// TODO a lot more cases should be added here
		{
			name: "differing update policies",
			spec1: DeviceSpec{
				UpdatePolicy: &DeviceUpdatePolicySpec{
					DownloadSchedule: &UpdateSchedule{
						At:                 "*/10 * * * *",
						StartGraceDuration: nil,
						TimeZone:           nil,
					},
					UpdateSchedule: nil,
				},
			},
			spec2: DeviceSpec{
				UpdatePolicy: &DeviceUpdatePolicySpec{
					DownloadSchedule: &UpdateSchedule{
						At:                 "*/1 * * * *",
						StartGraceDuration: nil,
						TimeZone:           nil,
					},
					UpdateSchedule: nil,
				},
			},
			expect: false,
		},
		{
			name: "same update policies",
			spec1: DeviceSpec{
				UpdatePolicy: &DeviceUpdatePolicySpec{
					DownloadSchedule: &UpdateSchedule{
						At:                 "*/10 * * * *",
						StartGraceDuration: nil,
						TimeZone:           nil,
					},
					UpdateSchedule: nil,
				},
			},
			spec2: DeviceSpec{
				UpdatePolicy: &DeviceUpdatePolicySpec{
					DownloadSchedule: &UpdateSchedule{
						At:                 "*/10 * * * *",
						StartGraceDuration: nil,
						TimeZone:           nil,
					},
					UpdateSchedule: nil,
				},
			},
			expect: true,
		},
		{
			name: "applications with volumes",
			spec1: DeviceSpec{
				Applications: createTestApplicationsWithVolumes(t),
			},
			spec2: DeviceSpec{
				Applications: createTestApplicationsWithVolumes(t),
			},
			expect: true,
		},
		{
			name: "nil applications vs non-nil applications",
			spec1: DeviceSpec{
				Applications: nil,
			},
			spec2: DeviceSpec{
				Applications: createTestApplicationsWithVolumes(t),
			},
			expect: false,
		},
		{
			name: "both nil applications",
			spec1: DeviceSpec{
				Applications: nil,
			},
			spec2: DeviceSpec{
				Applications: nil,
			},
			expect: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			req.Equal(tt.expect, DeviceSpecsAreEqual(tt.spec1, tt.spec2))
		})
	}
}

func TestDeviceSpecsAreEqualConsistency(t *testing.T) {
	// Test that both approaches give the same results
	tests := []struct {
		name  string
		spec1 DeviceSpec
		spec2 DeviceSpec
	}{
		{
			name:  "empty specs",
			spec1: DeviceSpec{},
			spec2: DeviceSpec{},
		},
		{
			name: "complex specs with all union types",
			spec1: DeviceSpec{
				Os: &DeviceOsSpec{
					Image: "quay.io/fedora/fedora-coreos:stable",
				},
				Applications: createTestApplicationsWithVolumes(t),
				Config:       createTestConfigs(t),
				Resources:    createTestResources(t),
			},
			spec2: DeviceSpec{
				Os: &DeviceOsSpec{
					Image: "quay.io/fedora/fedora-coreos:stable",
				},
				Applications: createTestApplicationsWithVolumes(t),
				Config:       createTestConfigs(t),
				Resources:    createTestResources(t),
			},
		},
		{
			name: "different specs",
			spec1: DeviceSpec{
				Os: &DeviceOsSpec{
					Image: "quay.io/fedora/fedora-coreos:stable",
				},
			},
			spec2: DeviceSpec{
				Os: &DeviceOsSpec{
					Image: "quay.io/fedora/fedora-coreos:latest",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)

			// Both methods should give the same result (now they're actually the same)
			result1 := DeviceSpecsAreEqual(tt.spec1, tt.spec2)
			result2 := DeviceSpecsAreEqual(tt.spec1, tt.spec2)

			req.Equal(result1, result2,
				"Multiple calls should give the same result")
		})
	}
}

func createTestApplicationsWithVolumes(t testing.TB) *[]ApplicationProviderSpec {
	require := require.New(t)

	// Create a volume
	imageVolumeProvider := ImageVolumeProviderSpec{
		Image: ImageVolumeSource{
			Reference:  "quay.io/test/volume:v1",
			PullPolicy: lo.ToPtr(PullIfNotPresent),
		},
	}

	volumeProvider := ApplicationVolume{Name: "test-volume"}
	require.NoError(volumeProvider.FromImageVolumeProviderSpec(imageVolumeProvider))

	// Create an application with the volume
	app := ApplicationProviderSpec{
		Name:    lo.ToPtr("test-app"),
		AppType: lo.ToPtr(AppTypeCompose),
	}

	provider := ImageApplicationProviderSpec{
		Image:   "quay.io/test/app:v1",
		Volumes: &[]ApplicationVolume{volumeProvider},
	}
	require.NoError(app.FromImageApplicationProviderSpec(provider))

	return &[]ApplicationProviderSpec{app}
}

func createTestConfigs(t testing.TB) *[]ConfigProviderSpec {
	require := require.New(t)

	var gitConfig ConfigProviderSpec
	err := gitConfig.FromGitConfigProviderSpec(GitConfigProviderSpec{
		Name: "test-git-config",
		GitRef: struct {
			Path           string `json:"path"`
			Repository     string `json:"repository"`
			TargetRevision string `json:"targetRevision"`
		}{
			Repository:     "test-repo",
			TargetRevision: "main",
			Path:           "/config",
		},
	})
	require.NoError(err)

	var inlineConfig ConfigProviderSpec
	err = inlineConfig.FromInlineConfigProviderSpec(InlineConfigProviderSpec{
		Name: "test-inline-config",
		Inline: []FileSpec{
			{
				Path:    "/etc/test.conf",
				Content: "test=value",
			},
		},
	})
	require.NoError(err)

	return &[]ConfigProviderSpec{gitConfig, inlineConfig}
}

func createTestResources(t testing.TB) *[]ResourceMonitor {
	var cpuMonitor ResourceMonitor
	err := cpuMonitor.FromCpuResourceMonitorSpec(CpuResourceMonitorSpec{
		MonitorType:      "CPU",
		SamplingInterval: "30s",
		AlertRules: []ResourceAlertRule{
			{
				Severity:    "Critical",
				Percentage:  90.0,
				Duration:    "5m",
				Description: "High CPU usage",
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create CPU monitor: %v", err)
	}

	var memoryMonitor ResourceMonitor
	err = memoryMonitor.FromMemoryResourceMonitorSpec(MemoryResourceMonitorSpec{
		MonitorType:      "Memory",
		SamplingInterval: "30s",
		AlertRules: []ResourceAlertRule{
			{
				Severity:    "Critical",
				Percentage:  85.0,
				Duration:    "5m",
				Description: "High memory usage",
			},
		},
	})
	if err != nil {
		t.Fatalf("Failed to create memory monitor: %v", err)
	}

	return &[]ResourceMonitor{cpuMonitor, memoryMonitor}
}

func TestDeviceSpecsAreEqual_IntegrationTestScenario(t *testing.T) {
	// This test reproduces the scenario from the failing integration test:
	// "CreateOrUpdateDevice update labels owned from API"
	// The test creates two devices with identical specs but different labels,
	// and the specs should be detected as equal.

	// Create the first device spec (mimicking what CreateTestDevice creates)
	// This simulates a device retrieved from the database
	spec1 := createReturnTestDeviceSpec(t)

	// Create the second device spec (mimicking what the integration test creates)
	// This simulates a new device with updated labels
	spec2 := createReturnTestDeviceSpec(t)

	// The specs should be identical (only labels differ, which are in metadata)
	require.Equal(t, true, DeviceSpecsAreEqual(spec1, spec2),
		"Two devices with same spec but different labels should have equal specs")

	// Test with JSON marshaling/unmarshaling to simulate database round-trip
	spec1JSON, err := json.Marshal(spec1)
	require.NoError(t, err)

	var spec1Unmarshaled DeviceSpec
	err = json.Unmarshal(spec1JSON, &spec1Unmarshaled)
	require.NoError(t, err)

	// This should still be equal after round-trip
	require.Equal(t, true, DeviceSpecsAreEqual(spec1, spec1Unmarshaled),
		"DeviceSpec should be equal to itself after JSON round-trip")
	require.Equal(t, true, DeviceSpecsAreEqual(spec1Unmarshaled, spec2),
		"DeviceSpec from JSON should be equal to freshly created spec")
}

func TestDeviceSpecsAreEqual_DatabaseRoundTrip(t *testing.T) {
	// This test specifically tests the scenario where one DeviceSpec comes from
	// the database (JSON unmarshaled) and another is freshly created

	// Create a fresh DeviceSpec
	freshSpec := createReturnTestDeviceSpec(t)

	// Simulate what happens when it goes through the database:
	// 1. Marshal to JSON (like storing in database)
	specJSON, err := json.Marshal(freshSpec)
	require.NoError(t, err)

	// 2. Unmarshal from JSON (like retrieving from database)
	var dbSpec DeviceSpec
	err = json.Unmarshal(specJSON, &dbSpec)
	require.NoError(t, err)

	// 3. Create another fresh spec (like what the API creates)
	anotherFreshSpec := createReturnTestDeviceSpec(t)

	// All three should be equal
	require.Equal(t, true, DeviceSpecsAreEqual(freshSpec, dbSpec),
		"Fresh spec should equal database round-trip spec")
	require.Equal(t, true, DeviceSpecsAreEqual(dbSpec, anotherFreshSpec),
		"Database spec should equal another fresh spec")
	require.Equal(t, true, DeviceSpecsAreEqual(freshSpec, anotherFreshSpec),
		"Two fresh specs should be equal")

	// Print JSON for debugging if they're not equal
	if !DeviceSpecsAreEqual(dbSpec, anotherFreshSpec) {
		dbJSON, _ := json.Marshal(dbSpec)
		freshJSON, _ := json.Marshal(anotherFreshSpec)
		t.Logf("DB JSON: %s", string(dbJSON))
		t.Logf("Fresh JSON: %s", string(freshJSON))
	}
}

// createReturnTestDeviceSpec creates a DeviceSpec exactly like test/util/create_utils.go ReturnTestDevice
func createReturnTestDeviceSpec(t testing.TB) DeviceSpec {
	require := require.New(t)

	// Create git config provider (exactly like in ReturnTestDevice)
	gitConfig := &GitConfigProviderSpec{
		Name: "param-git-config",
		GitRef: struct {
			Path           string `json:"path"`
			Repository     string `json:"repository"`
			TargetRevision string `json:"targetRevision"`
		}{
			Path:           "path-{{ device.metadata.labels[key] }}",
			Repository:     "repo",
			TargetRevision: "rev",
		},
	}
	gitItem := ConfigProviderSpec{}
	err := gitItem.FromGitConfigProviderSpec(*gitConfig)
	require.NoError(err)

	// Create inline config provider (exactly like in ReturnTestDevice)
	enc := EncodingBase64
	inlineConfig := &InlineConfigProviderSpec{
		Name: "param-inline-config",
		Inline: []FileSpec{
			// Unencoded: My version is {{ device.metadata.labels[version] }}
			{
				Path:            "/etc/withparams",
				ContentEncoding: &enc,
				Content:         "TXkgdmVyc2lvbiBpcyB7eyBkZXZpY2UubWV0YWRhdGEubGFiZWxzW3ZlcnNpb25dIH19",
			},
		},
	}
	inlineItem := ConfigProviderSpec{}
	err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
	require.NoError(err)

	// Create HTTP config provider (exactly like in ReturnTestDevice)
	httpConfig := &HttpConfigProviderSpec{
		Name: "param-http-config",
		HttpRef: struct {
			FilePath   string  `json:"filePath"`
			Repository string  `json:"repository"`
			Suffix     *string `json:"suffix,omitempty"`
		}{
			Repository: "http-repo",
			FilePath:   "/http-path-{{ device.metadata.labels[key] }}",
			Suffix:     lo.ToPtr("/http-suffix"),
		},
	}
	httpItem := ConfigProviderSpec{}
	err = httpItem.FromHttpConfigProviderSpec(*httpConfig)
	require.NoError(err)

	// Create the DeviceSpec exactly like in ReturnTestDevice
	return DeviceSpec{
		Os: &DeviceOsSpec{
			Image: "os",
		},
		Config: &[]ConfigProviderSpec{gitItem, inlineItem, httpItem},
	}
}

func TestDeviceSpecsAreEqual_AllUnionTypes(t *testing.T) {
	// This test ensures DeviceSpecsAreEqual correctly handles all union types
	// and doesn't break when new fields are added to DeviceSpec

	createComprehensiveDeviceSpec := func() DeviceSpec {
		// Create all types of config providers (union types)
		gitConfig := &GitConfigProviderSpec{
			Name: "git-config",
			GitRef: struct {
				Path           string `json:"path"`
				Repository     string `json:"repository"`
				TargetRevision string `json:"targetRevision"`
			}{
				Path:           "/config/git",
				Repository:     "test-repo",
				TargetRevision: "main",
			},
		}
		gitItem := ConfigProviderSpec{}
		err := gitItem.FromGitConfigProviderSpec(*gitConfig)
		require.NoError(t, err)

		inlineConfig := &InlineConfigProviderSpec{
			Name: "inline-config",
			Inline: []FileSpec{
				{
					Path:    "/etc/test-config",
					Content: "test configuration content",
				},
			},
		}
		inlineItem := ConfigProviderSpec{}
		err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
		require.NoError(t, err)

		// Create application volumes (union types)
		imageVolume := ApplicationVolume{
			Name: "test-volume",
		}
		err = imageVolume.FromImageVolumeProviderSpec(ImageVolumeProviderSpec{
			Image: ImageVolumeSource{
				Reference:  "quay.io/test/volume:latest",
				PullPolicy: lo.ToPtr(PullIfNotPresent),
			},
		})
		require.NoError(t, err)

		// Create application providers (union types)
		imageApp := &ImageApplicationProviderSpec{
			Image:   "quay.io/test/app:latest",
			Volumes: &[]ApplicationVolume{imageVolume},
		}
		imageAppItem := ApplicationProviderSpec{
			AppType: lo.ToPtr(AppTypeCompose),
			Name:    lo.ToPtr("test-app"),
		}
		err = imageAppItem.FromImageApplicationProviderSpec(*imageApp)
		require.NoError(t, err)

		// Create resource monitors (union types)
		cpuMonitor := ResourceMonitor{}
		err = cpuMonitor.FromCpuResourceMonitorSpec(CpuResourceMonitorSpec{
			MonitorType:      "CPU",
			SamplingInterval: "30s",
			AlertRules: []ResourceAlertRule{
				{
					Severity:    ResourceAlertSeverityTypeCritical,
					Percentage:  90.0,
					Duration:    "5m",
					Description: "High CPU usage",
				},
			},
		})
		require.NoError(t, err)

		memoryMonitor := ResourceMonitor{}
		err = memoryMonitor.FromMemoryResourceMonitorSpec(MemoryResourceMonitorSpec{
			MonitorType:      "Memory",
			SamplingInterval: "30s",
			AlertRules: []ResourceAlertRule{
				{
					Severity:    ResourceAlertSeverityTypeWarning,
					Percentage:  80.0,
					Duration:    "10m",
					Description: "High memory usage",
				},
			},
		})
		require.NoError(t, err)

		return DeviceSpec{
			Os: &DeviceOsSpec{
				Image: "quay.io/test/os:latest",
			},
			Config:       &[]ConfigProviderSpec{gitItem, inlineItem},
			Applications: &[]ApplicationProviderSpec{imageAppItem},
			Resources:    &[]ResourceMonitor{cpuMonitor, memoryMonitor},
			Consoles: &[]DeviceConsole{
				{
					SessionID:       "session-123",
					SessionMetadata: "terminal=xterm",
				},
			},
			Decommissioning: &DeviceDecommission{
				Target: DeviceDecommissionTargetTypeUnenroll,
			},
			Systemd: &struct {
				MatchPatterns *[]string `json:"matchPatterns,omitempty"`
			}{
				MatchPatterns: &[]string{"systemd-*", "docker.service"},
			},
			UpdatePolicy: &DeviceUpdatePolicySpec{
				DownloadSchedule: &UpdateSchedule{
					At:       "0 2 * * *",
					TimeZone: lo.ToPtr("UTC"),
				},
			},
		}
	}

	// Test that identical comprehensive specs are equal
	spec1 := createComprehensiveDeviceSpec()
	spec2 := createComprehensiveDeviceSpec()

	require.True(t, DeviceSpecsAreEqual(spec1, spec2),
		"Identical comprehensive DeviceSpecs should be equal")

	// Test that specs with different union content are not equal
	spec3 := createComprehensiveDeviceSpec()
	// Modify a union type
	if spec3.Applications != nil && len(*spec3.Applications) > 0 {
		(*spec3.Applications)[0].Name = lo.ToPtr("different-app-name")
	}

	require.False(t, DeviceSpecsAreEqual(spec1, spec3),
		"DeviceSpecs with different union content should not be equal")

	// Test database round-trip simulation
	spec4 := createComprehensiveDeviceSpec()

	// Simulate what happens during database storage/retrieval
	jsonData, err := json.Marshal(spec4)
	require.NoError(t, err)

	var spec5 DeviceSpec
	err = json.Unmarshal(jsonData, &spec5)
	require.NoError(t, err)

	require.True(t, DeviceSpecsAreEqual(spec4, spec5),
		"DeviceSpec should be equal after JSON round-trip")
}
