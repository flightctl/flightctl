package labelsyncmapping

import (
	"errors"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/google/cel-go/common/types"
	"github.com/stretchr/testify/require"
)

func TestEvaluatorEvaluate(t *testing.T) {
	testCases := []struct {
		name        string
		expression  string
		device      v1beta1.Device
		expected    Result
		failureKind FailureKind
	}{
		{
			name:       "When evaluating a direct systemInfo field it should return its value",
			expression: "status.systemInfo.architecture",
			device:     testDevice("amd64", map[string]string{"site": "east"}),
			expected:   Result{Present: true, Value: "amd64"},
		},
		{
			name:       "When evaluating a customInfo field it should sanitize its string value",
			expression: "status.systemInfo.customInfo.site",
			device:     testDevice("amd64", map[string]string{"site": "east coast"}),
			expected:   Result{Present: true, Value: "east-coast"},
		},
		{
			name:       "When evaluating a conditional it should return the matching branch",
			expression: `status.systemInfo.architecture == "amd64" ? "x86" : "other"`,
			device:     testDevice("arm64", nil),
			expected:   Result{Present: true, Value: "other"},
		},
		{
			name:       "When evaluating boolean and numeric scalars it should use canonical strings",
			expression: `status.systemInfo.architecture == "amd64" ? 42 : 1`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "42"},
		},
		{
			name:       "When evaluating a boolean scalar it should use its canonical string",
			expression: `status.systemInfo.architecture == "amd64"`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "true"},
		},
		{
			name:       "When evaluating a floating-point scalar it should use its canonical string",
			expression: `1.5`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "1.5"},
		},
		{
			name:       "When evaluating comparisons and casts it should return the scalar result",
			expression: `status.systemInfo.architecture == "amd64" && int("42") >= 42 && double("1.5") > 1.0 && status.systemInfo.customInfo.site.startsWith("east") ? string(42) : "other"`,
			device:     testDevice("amd64", map[string]string{"site": "east-coast"}),
			expected:   Result{Present: true, Value: "42"},
		},
		{
			name:       "When evaluating semantic versions it should compare semantic precedence",
			expression: `isSemver(status.systemInfo.agentVersion) && semver(status.systemInfo.agentVersion).compareTo(semver("1.2.3")) >= 0`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "true"},
		},
		{
			name:       "When evaluating a metadata field it should use the full device activation",
			expression: `metadata.labels["environment"] == "production" ? metadata.name : "other"`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "edge-01"},
		},
		{
			name:       "When evaluating a nested spec field it should return its value",
			expression: "spec.os.image",
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "fedora"},
		},
		{
			name:       "When evaluating a spec list it should support indexing and standard functions",
			expression: `size(spec.systemd.matchPatterns) >= 2 ? spec.systemd.matchPatterns[0] : "other"`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "ssh.service"},
		},
		{
			name:       "When evaluating an additional systemInfo property it should use the full device activation",
			expression: "status.systemInfo.siteClass",
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "edge"},
		},
		{
			name:       "When evaluating a standard function it should return its scalar result",
			expression: `size(status.systemInfo.customInfo) >= 1 ? "known" : "unknown"`,
			device:     testDevice("amd64", map[string]string{"site": "east"}),
			expected:   Result{Present: true, Value: "known"},
		},
		{
			name:       "When a customInfo value is missing it should return an absent result",
			expression: "status.systemInfo.customInfo.site",
			device:     testDevice("amd64", map[string]string{}),
			expected:   Result{},
		},
		{
			name:       "When an expression evaluates to null it should return an absent result",
			expression: "null",
			device:     testDevice("amd64", nil),
			expected:   Result{},
		},
		{
			name:       "When a conditional selects a present optional it should return its value",
			expression: `status.systemInfo.architecture == "amd64" ? optional.of("matching") : optional.none()`,
			device:     testDevice("amd64", nil),
			expected:   Result{Present: true, Value: "matching"},
		},
		{
			name:       "When a conditional selects an absent optional it should return an absent result",
			expression: `status.systemInfo.architecture == "amd64" ? optional.of("matching") : optional.none()`,
			device:     testDevice("arm64", nil),
			expected:   Result{},
		},
		{
			name:        "When an expression evaluates to a list it should reject the complex value",
			expression:  `[status.systemInfo.architecture]`,
			device:      testDevice("amd64", nil),
			failureKind: FailureComplexValue,
		},
		{
			name:        "When an expression evaluates to a map it should reject the complex value",
			expression:  `{"site": status.systemInfo.customInfo.site}`,
			device:      testDevice("amd64", map[string]string{"site": "east"}),
			failureKind: FailureComplexValue,
		},
		{
			name:        "When an expression evaluates to the systemInfo object it should reject the complex value",
			expression:  "status.systemInfo",
			device:      testDevice("amd64", map[string]string{"site": "east"}),
			failureKind: FailureComplexValue,
		},
		{
			name:        "When a scalar sanitizes to empty it should reject the result",
			expression:  `"!!!"`,
			device:      testDevice("amd64", nil),
			failureKind: FailureSanitization,
		},
		{
			name:       "When reusing a compiled expression it should evaluate the current device activation",
			expression: "status.systemInfo.architecture",
			device:     testDevice("arm64", nil),
			expected:   Result{Present: true, Value: "arm64"},
		},
		{
			name:        "When an expression uses an undeclared root it should reject the activation",
			expression:  "device.status.systemInfo.architecture",
			device:      testDevice("amd64", nil),
			failureKind: FailureInvalidActivation,
		},
		{
			name:        "When an expression uses apiVersion it should reject the activation",
			expression:  "apiVersion",
			device:      testDevice("amd64", nil),
			failureKind: FailureInvalidActivation,
		},
		{
			name:        "When an expression uses kind it should reject the activation",
			expression:  "kind",
			device:      testDevice("amd64", nil),
			failureKind: FailureInvalidActivation,
		},
	}

	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(tt.expression, tt.device)

			if tt.failureKind == "" {
				require.NoError(t, err)
				require.Equal(t, tt.expected, result)
				return
			}

			require.Error(t, err)
			var evaluationError *EvaluationError
			require.True(t, errors.As(err, &evaluationError))
			require.Equal(t, tt.failureKind, evaluationError.Kind)
		})
	}
}

func TestEvaluatorProgramCache(t *testing.T) {
	evaluatorInterface, err := NewEvaluator()
	require.NoError(t, err)

	implementation := evaluatorInterface.(*evaluator)
	device := testDevice("amd64", nil)
	for index := range maxCachedPrograms {
		_, err := evaluatorInterface.Evaluate(fmt.Sprintf(`"%d"`, index), device)
		require.NoError(t, err)
	}

	_, err = evaluatorInterface.Evaluate(`"0"`, device)
	require.NoError(t, err)
	_, err = evaluatorInterface.Evaluate(fmt.Sprintf(`"%d"`, maxCachedPrograms), device)
	require.NoError(t, err)

	implementation.mu.Lock()
	defer implementation.mu.Unlock()
	require.Len(t, implementation.programs, maxCachedPrograms)
	_, found := implementation.programs[`"0"`]
	require.True(t, found)
	_, found = implementation.programs[`"1"`]
	require.False(t, found)
	_, found = implementation.programs[fmt.Sprintf(`"%d"`, maxCachedPrograms)]
	require.True(t, found)
}

func TestParseSemver(t *testing.T) {
	testCases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "When a version has a lowercase v prefix it should normalize it",
			input:    "v1.2.3",
			expected: "1.2.3",
		},
		{
			name:     "When a version has an uppercase V prefix it should normalize it",
			input:    "V1.2.3",
			expected: "1.2.3",
		},
		{
			name:     "When a version has a release prefix it should normalize it",
			input:    "release-1.2.3",
			expected: "1.2.3",
		},
		{
			name:     "When a version has surrounding whitespace it should normalize it",
			input:    "  1.2.3\t",
			expected: "1.2.3",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			version, err := parseSemver(types.String(tt.input))

			require.NoError(t, err)
			require.Equal(t, tt.expected, version.String())
		})
	}
}

func testDevice(architecture string, customInfo map[string]string) v1beta1.Device {
	name := "edge-01"
	labels := map[string]string{"environment": "production"}
	device := v1beta1.Device{
		Metadata: v1beta1.ObjectMeta{
			Name:   &name,
			Labels: &labels,
		},
		Status: &v1beta1.DeviceStatus{},
	}
	device.Status.SystemInfo.Architecture = architecture
	device.Status.SystemInfo.AgentVersion = "v1.3.0"
	device.Status.SystemInfo.AdditionalProperties = map[string]string{"siteClass": "edge"}
	matchPatterns := []string{"ssh.service", "podman.service"}
	device.Spec = &v1beta1.DeviceSpec{
		Os: &v1beta1.DeviceOsSpec{Image: "fedora"},
		Systemd: &struct {
			MatchPatterns *[]string `json:"matchPatterns,omitempty"`
		}{
			MatchPatterns: &matchPatterns,
		},
	}
	if customInfo != nil {
		info := v1beta1.CustomDeviceInfo(customInfo)
		device.Status.SystemInfo.CustomInfo = &info
	}
	return device
}
