package labelsyncmapping

import (
	"fmt"
	"testing"

	"cel.dev/cel-go/common/types"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestEvaluatorEvaluate(t *testing.T) {
	testCases := []struct {
		name                  string
		expression            string
		device                domain.Device
		expectedScalar        bool
		expectedMap           map[string]string
		expectedErrorContains []string
	}{
		{
			name:           "When evaluating a direct systemInfo field in scalar mode it should return its value",
			expression:     "status.systemInfo.architecture",
			device:         testDevice("amd64", map[string]string{"site": "east"}),
			expectedScalar: true,
			expectedMap:    map[string]string{"architecture": "amd64"},
		},
		{
			name:           "When evaluating a customInfo field in scalar mode it should sanitize its string value",
			expression:     "status.systemInfo.customInfo.site",
			device:         testDevice("amd64", map[string]string{"site": "east coast"}),
			expectedScalar: true,
			expectedMap:    map[string]string{"site": "east-coast"},
		},
		{
			name:           "When evaluating a conditional it should return the matching branch",
			expression:     `status.systemInfo.architecture == "amd64" ? "x86" : "other"`,
			device:         testDevice("arm64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"architecture": "other"},
		},
		{
			name:           "When a conditional selects a non-null string it should return the sanitized value",
			expression:     `status.systemInfo.architecture == "amd64" ? "some value" : dyn(null)`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"conditional": "some-value"},
		},
		{
			name:           "When a conditional selects null it should return no value without an error",
			expression:     `status.systemInfo.architecture == "amd64" ? "some value" : dyn(null)`,
			device:         testDevice("arm64", nil),
			expectedScalar: true,
		},
		{
			name:           "When evaluating a numeric scalar it should use its canonical string",
			expression:     `status.systemInfo.architecture == "amd64" ? 42 : 1`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"attempts": "42"},
		},
		{
			name:           "When evaluating a boolean scalar it should use its canonical string",
			expression:     `status.systemInfo.architecture == "amd64"`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"supported": "true"},
		},
		{
			name:           "When evaluating a floating-point scalar it should use its canonical string",
			expression:     `1.5`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"ratio": "1.5"},
		},
		{
			name:           "When evaluating comparisons and casts it should return the scalar result",
			expression:     `status.systemInfo.architecture == "amd64" && int("42") >= 42 && double("1.5") > 1.0 && status.systemInfo.customInfo.site.startsWith("east") ? string(42) : "other"`,
			device:         testDevice("amd64", map[string]string{"site": "east-coast"}),
			expectedScalar: true,
			expectedMap:    map[string]string{"result": "42"},
		},
		{
			name:           "When evaluating semantic versions it should compare semantic precedence",
			expression:     `isSemver(status.systemInfo.agentVersion) && semver(status.systemInfo.agentVersion).compareTo(semver("1.2.3")) >= 0`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"version-match": "true"},
		},
		{
			name:           "When evaluating metadata it should use the normalized metadata root",
			expression:     `metadata.labels["environment"] == "production" ? metadata.name : "other"`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"device-name": "edge-01"},
		},
		{
			name:           "When evaluating a nested spec field it should return its value",
			expression:     "spec.os.image",
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"os-image": "fedora"},
		},
		{
			name:           "When evaluating a spec list it should support indexing and standard functions",
			expression:     `size(spec.systemd.matchPatterns) >= 2 ? spec.systemd.matchPatterns[0] : "other"`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"service": "ssh.service"},
		},
		{
			name:           "When evaluating an additional systemInfo property it should use the normalized status root",
			expression:     "status.systemInfo.siteClass",
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"site-class": "edge"},
		},
		{
			name:           "When evaluating a standard function it should return its scalar result",
			expression:     `size(status.systemInfo.customInfo) >= 1 ? "known" : "unknown"`,
			device:         testDevice("amd64", map[string]string{"site": "east"}),
			expectedScalar: true,
			expectedMap:    map[string]string{"custom-info-state": "known"},
		},
		{
			name:           "When a customInfo value is missing it should return no value",
			expression:     "status.systemInfo.customInfo.site",
			device:         testDevice("amd64", map[string]string{}),
			expectedScalar: true,
		},
		{
			name:           "When an expression evaluates to null it should return no value",
			expression:     "null",
			device:         testDevice("amd64", nil),
			expectedScalar: true,
		},
		{
			name:           "When a conditional selects a present optional it should return its value",
			expression:     `status.systemInfo.architecture == "amd64" ? optional.of("matching") : optional.none()`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"optional": "matching"},
		},
		{
			name:           "When a conditional selects an absent optional it should return no value",
			expression:     `status.systemInfo.architecture == "amd64" ? optional.of("matching") : optional.none()`,
			device:         testDevice("arm64", nil),
			expectedScalar: true,
		},
		{
			name:                  "When a statically known list is returned it should reject the expression",
			expression:            `[status.systemInfo.architecture]`,
			device:                testDevice("amd64", nil),
			expectedScalar:        true,
			expectedErrorContains: []string{"not a supported scalar or map result"},
		},
		{
			name:                  "When a nested object is returned it should reject the map entries",
			expression:            "status.systemInfo",
			device:                testDevice("amd64", map[string]string{"site": "east"}),
			expectedErrorContains: []string{"unsupported CEL scalar type"},
		},
		{
			name:        "When a dynamic map is returned it should produce a map result",
			expression:  `dyn({"site": "east"})`,
			device:      testDevice("amd64", nil),
			expectedMap: map[string]string{"site": "east"},
		},
		{
			name:                  "When a non-empty scalar sanitizes to empty it should return a failure without a value",
			expression:            `"!!!"`,
			device:                testDevice("amd64", nil),
			expectedScalar:        true,
			expectedErrorContains: []string{"sanitizes to an empty label value"},
		},
		{
			name:                  "When an expression uses an undeclared root it should reject the activation",
			expression:            "device.status.systemInfo.architecture",
			device:                testDevice("amd64", nil),
			expectedScalar:        true,
			expectedErrorContains: []string{"checking CEL expression", "undeclared reference"},
		},
		{
			name:                  "When an expression uses apiVersion it should reject the activation",
			expression:            "apiVersion",
			device:                testDevice("amd64", nil),
			expectedScalar:        true,
			expectedErrorContains: []string{"checking CEL expression", "undeclared reference"},
		},
		{
			name:                  "When an expression uses kind it should reject the activation",
			expression:            "kind",
			device:                testDevice("amd64", nil),
			expectedScalar:        true,
			expectedErrorContains: []string{"checking CEL expression", "undeclared reference"},
		},
		{
			name:        "When evaluating a map it should return complete keys and scalar values",
			expression:  `{"custominfo/site": "east coast", "attempts": 42, "enabled": true, "empty": null}`,
			device:      testDevice("amd64", nil),
			expectedMap: map[string]string{"custominfo/site": "east-coast", "attempts": "42", "enabled": "true"},
		},
		{
			name:        "When transformMapEntry rewrites customInfo keys it should return the rewritten keys",
			expression:  `(has(status.systemInfo.customInfo) && status.systemInfo.customInfo != null ? status.systemInfo.customInfo : {}).transformMapEntry(k, v, {"custominfo/" + k: v})`,
			device:      testDevice("amd64", map[string]string{"site": "east coast", "rack": "r2"}),
			expectedMap: map[string]string{"custominfo/site": "east-coast", "custominfo/rack": "r2"},
		},
		{
			name:        "When a conditional selects a map it should return its entries",
			expression:  `status.systemInfo.architecture == "amd64" ? {"site": "east"} : {}`,
			device:      testDevice("amd64", nil),
			expectedMap: map[string]string{"site": "east"},
		},
		{
			name:        "When an optional map contains a value it should return its entries",
			expression:  `optional.of({"site": "east"})`,
			device:      testDevice("amd64", nil),
			expectedMap: map[string]string{"site": "east"},
		},
		{
			name:       "When an optional map is absent it should return no entries",
			expression: `optional.none()`,
			device:     testDevice("amd64", nil),
		},
		{
			name:       "When a map expression evaluates to null it should return no entries",
			expression: `dyn(null)`,
			device:     testDevice("amd64", nil),
		},
		{
			name:       "When a map expression refers to a missing value it should return no entries",
			expression: "status.systemInfo.customInfo.missing",
			device:     testDevice("amd64", map[string]string{}),
		},
		{
			name:                  "When a dynamic list is returned it should reject the runtime value",
			expression:            `dyn(["not", "a", "map"])`,
			device:                testDevice("amd64", nil),
			expectedErrorContains: []string{"unsupported CEL scalar type"},
		},
		{
			name:           "When a dynamic scalar is returned it should produce a scalar result",
			expression:     `dyn("not a map")`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"value": "not-a-map"},
		},
		{
			name:           "When a statically known scalar is returned it should produce a scalar result",
			expression:     `"not a map"`,
			device:         testDevice("amd64", nil),
			expectedScalar: true,
			expectedMap:    map[string]string{"value": "not-a-map"},
		},
		{
			name:                  "When a map has non-string keys it should reject the expression",
			expression:            `{1: "not a string key"}`,
			device:                testDevice("amd64", nil),
			expectedErrorContains: []string{"not a supported scalar or map result"},
		},
		{
			name:                  "When a map has nested map values it should reject the expression",
			expression:            `{"outer": {"inner": "value"}}`,
			device:                testDevice("amd64", nil),
			expectedErrorContains: []string{"not a supported scalar or map result"},
		},
		{
			name:                  "When a dynamic map contains invalid entries it should reject the entire result and report each failure",
			expression:            `dyn({"good": "east coast", "number": 42, "empty": null, "bad key": "omitted", "nested": {"site": "omitted"}, "list": [1]})`,
			device:                testDevice("amd64", nil),
			expectedErrorContains: []string{`"bad key"`, `"list"`, `"nested"`},
		},
		{
			name:                  "When a map value sanitizes to empty it should reject the entire result and report the failure",
			expression:            `{"good": "east", "bad": "!!!"}`,
			device:                testDevice("amd64", nil),
			expectedErrorContains: []string{`"bad"`, "sanitizes to an empty label value"},
		},
	}

	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			activation := testActivation(t, tt.device)
			result, err := evaluator.Evaluate(tt.expression, activation)

			if tt.expectedErrorContains != nil {
				require.Error(t, err)
				require.Nil(t, result)
				for _, expected := range tt.expectedErrorContains {
					require.ErrorContains(t, err, expected)
				}
				return
			}

			require.NoError(t, err)
			if tt.expectedMap == nil {
				require.Equal(t, NoResult{}, result)
				return
			}
			if tt.expectedScalar {
				require.Len(t, tt.expectedMap, 1)
				for _, value := range tt.expectedMap {
					require.Equal(t, ScalarResult(value), result)
				}
				return
			}
			require.Equal(t, MapResult(tt.expectedMap), result)
		})
	}
}

func TestEvaluatorEvaluateReusesActivation(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	activation, err := ActivateDevice(testDevice("amd64", map[string]string{"site": "west"}))
	require.NoError(t, err)

	testCases := []struct {
		name       string
		expression string
		expected   Result
	}{
		{
			name:       "When evaluating metadata from a reused activation it should return the scalar",
			expression: "metadata.name",
			expected:   ScalarResult("edge-01"),
		},
		{
			name:       "When evaluating spec from a reused activation it should return the scalar",
			expression: "spec.os.image",
			expected:   ScalarResult("fedora"),
		},
		{
			name:       "When evaluating status from a reused activation it should return the scalar",
			expression: "status.systemInfo.customInfo.site",
			expected:   ScalarResult("west"),
		},
		{
			name:       "When evaluating a map from a reused activation it should return the map",
			expression: `{"site": status.systemInfo.customInfo.site}`,
			expected:   MapResult{"site": "west"},
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			result, err := evaluator.Evaluate(tt.expression, activation)
			require.NoError(t, err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestEvaluatorEvaluateRejectsNilActivation(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	result, err := evaluator.Evaluate(`"value"`, nil)
	require.Error(t, err)
	require.Nil(t, result)
	require.ErrorContains(t, err, "activation is nil")
}

func TestEvaluatorValidateExpressionIs(t *testing.T) {
	testCases := []struct {
		name                  string
		expression            string
		expectedKind          ResultKind
		expectedErrorContains []string
	}{
		{
			name:         "When a scalar expression is validated as scalar it should accept the output",
			expression:   "status.systemInfo.architecture",
			expectedKind: ResultKindScalar,
		},
		{
			name:         "When a map expression is validated as map it should accept string keys and scalar values",
			expression:   `{"site": "east", "enabled": true, "attempts": 3, "empty": null}`,
			expectedKind: ResultKindMap,
		},
		{
			name:         "When an optional scalar expression is validated as scalar it should accept the output",
			expression:   `optional.of("east")`,
			expectedKind: ResultKindScalar,
		},
		{
			name:                  "When an optional scalar expression is validated as map it should reject the mismatch",
			expression:            `optional.of("east")`,
			expectedKind:          ResultKindMap,
			expectedErrorContains: []string{"produces scalar, expected map"},
		},
		{
			name:         "When an absent optional is validated as map it should accept the unknown output shape",
			expression:   `optional.none()`,
			expectedKind: ResultKindMap,
		},
		{
			name:         "When a dynamic scalar expression is validated as scalar it should accept the unknown shape",
			expression:   "status.systemInfo.customInfo.site",
			expectedKind: ResultKindScalar,
		},
		{
			name:         "When a dynamic scalar expression is validated as map it should accept the unknown shape",
			expression:   "status.systemInfo.customInfo.site",
			expectedKind: ResultKindMap,
		},
		{
			name:         "When a dynamic map expression is validated as map it should accept the unknown shape",
			expression:   "status.systemInfo.customInfo",
			expectedKind: ResultKindMap,
		},
		{
			name:         "When a field under an allowed root is validated it should not require a device activation",
			expression:   "metadata.someField",
			expectedKind: ResultKindScalar,
		},
		{
			name:         "When null is validated against a result kind it should accept no output",
			expression:   "null",
			expectedKind: ResultKindMap,
		},
		{
			name:                  "When invalid CEL syntax is validated it should return an expression error",
			expression:            "status..systemInfo",
			expectedKind:          ResultKindScalar,
			expectedErrorContains: []string{"parsing CEL expression"},
		},
		{
			name:                  "When an undeclared root is validated it should return an activation error",
			expression:            "device.status.systemInfo",
			expectedKind:          ResultKindMap,
			expectedErrorContains: []string{"checking CEL expression", "undeclared reference"},
		},
		{
			name:                  "When a known scalar expression is validated as map it should reject the mismatch",
			expression:            `"east"`,
			expectedKind:          ResultKindMap,
			expectedErrorContains: []string{"produces scalar, expected map"},
		},
		{
			name:                  "When a known map expression is validated as scalar it should reject the mismatch",
			expression:            `{"site": "east"}`,
			expectedKind:          ResultKindScalar,
			expectedErrorContains: []string{"produces map, expected scalar"},
		},
		{
			name:                  "When a statically known list expression is validated it should reject the output shape",
			expression:            `["east"]`,
			expectedKind:          ResultKindMap,
			expectedErrorContains: []string{"not a supported scalar or map result"},
		},
		{
			name:                  "When a map with nested values is validated it should reject the output shape",
			expression:            `{"nested": {"site": "east"}}`,
			expectedKind:          ResultKindMap,
			expectedErrorContains: []string{"not a supported scalar or map result"},
		},
		{
			name:                  "When an unsupported result kind is requested it should return an error",
			expression:            `"east"`,
			expectedKind:          ResultKind("other"),
			expectedErrorContains: []string{"unsupported expected result kind"},
		},
	}

	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			err := evaluator.ValidateExpressionIs(tt.expression, tt.expectedKind)
			if tt.expectedErrorContains == nil {
				require.NoError(t, err)
				return
			}

			require.Error(t, err)
			for _, expected := range tt.expectedErrorContains {
				require.ErrorContains(t, err, expected)
			}
		})
	}
}

func TestEvaluatorProgramCache(t *testing.T) {
	evaluatorInterface, err := NewEvaluator()
	require.NoError(t, err)

	implementation := evaluatorInterface.(*evaluator)
	device := testDevice("amd64", nil)
	activation := testActivation(t, device)
	for index := range maxCachedPrograms {
		_, err := evaluatorInterface.Evaluate(fmt.Sprintf(`"%d"`, index), activation)
		require.NoError(t, err)
	}

	_, err = evaluatorInterface.Evaluate(`"0"`, activation)
	require.NoError(t, err)
	_, err = evaluatorInterface.Evaluate(fmt.Sprintf(`"%d"`, maxCachedPrograms), activation)
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

func TestEvaluatorProgramCacheSupportsDynamicResultShapes(t *testing.T) {
	evaluatorInterface, err := NewEvaluator()
	require.NoError(t, err)
	expression := `status.systemInfo.architecture == "amd64" ? dyn("scalar") : dyn({"site": "east"})`

	scalarActivation := testActivation(t, testDevice("amd64", nil))
	scalarResult, err := evaluatorInterface.Evaluate(expression, scalarActivation)
	require.NoError(t, err)
	require.Equal(t, ScalarResult("scalar"), scalarResult)

	mapActivation := testActivation(t, testDevice("arm64", nil))
	mapResult, err := evaluatorInterface.Evaluate(expression, mapActivation)
	require.NoError(t, err)
	require.Equal(t, MapResult{"site": "east"}, mapResult)
	require.Len(t, evaluatorInterface.(*evaluator).programs, 1)
}

func TestEvaluatorEnforcesCostLimit(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	device := testDevice("amd64", nil)
	patterns := make([]string, maxExpressionCost+100)
	device.Spec.Systemd.MatchPatterns = &patterns
	activation := testActivation(t, device)
	_, err = evaluator.Evaluate(`spec.systemd.matchPatterns.exists(pattern, pattern == "not-present")`, activation)
	require.Error(t, err)
	require.ErrorContains(t, err, "evaluating CEL expression")
}

func TestEvaluatorEnforcesMapCardinalityLimit(t *testing.T) {
	evaluator, err := NewEvaluator()
	require.NoError(t, err)

	customInfo := make(map[string]string, maxMapEntries+1)
	for index := range maxMapEntries + 1 {
		customInfo[fmt.Sprintf("site-%d", index)] = "east"
	}
	activation := testActivation(t, testDevice("amd64", customInfo))
	_, err = evaluator.Evaluate("status.systemInfo.customInfo", activation)
	require.Error(t, err)
	require.ErrorContains(t, err, "map result has 51 entries")
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

func testDevice(architecture string, customInfo map[string]string) domain.Device {
	name := "edge-01"
	labels := map[string]string{"environment": "production"}
	device := domain.Device{
		Metadata: domain.ObjectMeta{
			Name:   &name,
			Labels: &labels,
		},
		Status: &domain.DeviceStatus{},
	}
	device.Status.SystemInfo.Architecture = architecture
	device.Status.SystemInfo.AgentVersion = "v1.3.0"
	device.Status.SystemInfo.AdditionalProperties = map[string]string{"siteClass": "edge"}
	matchPatterns := []string{"ssh.service", "podman.service"}
	device.Spec = &domain.DeviceSpec{
		Os: &domain.DeviceOsSpec{Image: "fedora"},
		Systemd: &struct {
			MatchPatterns *[]string `json:"matchPatterns,omitempty"`
		}{
			MatchPatterns: &matchPatterns,
		},
	}
	if customInfo != nil {
		info := domain.CustomDeviceInfo(customInfo)
		device.Status.SystemInfo.CustomInfo = &info
	}
	return device
}

func testActivation(t *testing.T, device domain.Device) Activation {
	t.Helper()
	activation, err := ActivateDevice(device)
	require.NoError(t, err)
	return activation
}
