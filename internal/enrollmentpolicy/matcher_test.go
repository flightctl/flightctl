package enrollmentpolicy_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/enrollmentpolicy"
	"github.com/stretchr/testify/require"
)

func TestMatchProductSerial(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	policy, err := matcher.Compile(`systemInfo.productSerial == "SN-001"`)
	require.NoError(t, err)

	for _, tc := range []struct {
		name   string
		serial string
		want   bool
	}{
		{"When the serial matches it should pass", "SN-001", true},
		{"When the serial differs it should fail", "SN-002", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := domain.EnrollmentRequestSpec{
				DeviceStatus: &domain.DeviceStatus{
					SystemInfo: domain.DeviceSystemInfo{
						AdditionalProperties: map[string]string{"productSerial": tc.serial},
					},
				},
			}
			matches, err := policy.Match(spec)
			require.NoError(t, err)
			require.Equal(t, tc.want, matches)
		})
	}
}

func TestCompile(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		expression string
		wantError  bool
	}{
		{"When a hardware UUID is compared it should compile", `systemInfo.productUuid == "uuid-1"`, false},
		{"When a custom key is compared it should compile", `systemInfo.customInfo["site.name"] == "west"`, false},
		{"When equality and membership are conjoined they should compile", `systemInfo.productSerial == "sn-1" && systemInfo.customInfo["site"] in ["east", "west"]`, false},
		{"When the expression is true it should compile", `true`, false},
		{"When the expression is false it should compile", `false`, false},
		{"When the membership list is empty it should compile", `systemInfo.productSerial in []`, false},
		{"When the expression is malformed it should fail", `systemInfo.productSerial ==`, true},
		{"When the expression is oversized it should fail", `true` + strings.Repeat(" ", 1000), true},
		{"When the expression produces a string it should fail", `systemInfo.productSerial`, true},
		{"When the expression produces a map it should fail", `systemInfo.customInfo`, true},
		{"When the expression uses an arbitrary root it should fail", `status.systemInfo.productSerial == "sn-1"`, true},
		{"When an unlisted system info field is used it should fail", `systemInfo.hostname == "device-1"`, true},
		{"When certificate identity is used it should fail", `certificateName == "credential"`, true},
		{"When TPM verification is used it should fail", `tpmVerified == true`, true},
		{"When regex is used it should fail", `systemInfo.productSerial.matches("^sn-")`, true},
		{"When a string method is used it should fail", `systemInfo.productSerial.startsWith("sn-")`, true},
		{"When inequality is used it should fail", `systemInfo.productSerial != "sn-1"`, true},
		{"When negation is used it should fail", `!false`, true},
		{"When disjunction is used it should fail", `false || true`, true},
		{"When a macro is used it should fail", `has(systemInfo.customInfo.site)`, true},
		{"When a comprehension is used it should fail", `["west"].exists(site, site == "west")`, true},
		{"When a constant is compared with a constant it should fail", `"a" == "a"`, true},
		{"When a field is compared with a field it should fail", `systemInfo.productSerial == systemInfo.productUuid`, true},
		{"When a field is compared with a number it should fail", `systemInfo.productSerial == 1`, true},
		{"When a list mixes strings and numbers it should fail", `systemInfo.productSerial in ["sn-1", 1]`, true},
		{"When a membership list includes a field it should fail", `systemInfo.productSerial in [systemInfo.productUuid]`, true},
		{"When a membership uses map keys it should fail", `systemInfo.productSerial in systemInfo.customInfo`, true},
		{"When custom info uses a computed key it should fail", `systemInfo.customInfo[systemInfo.productUuid] == "west"`, true},
		{"When custom info uses field selection it should fail", `systemInfo.customInfo.site == "west"`, true},
		{"When custom info uses an integer key it should fail", `systemInfo.customInfo[1] == "west"`, true},
		{"When a custom info field is nested it should fail", `systemInfo.customInfo["site"].name == "west"`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := matcher.Compile(tc.expression)
			if tc.wantError {
				require.ErrorContains(t, err, "spec.matchExpression")
				require.Nil(t, policy)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, policy)
		})
	}
}

func TestMatchSystemInfo(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)

	spec := domain.EnrollmentRequestSpec{DeviceStatus: &domain.DeviceStatus{}}
	require.NoError(t, json.Unmarshal([]byte(`{
		"systemInfo": {
			"productUuid": "uuid-1",
			"hostname": "unlisted",
			"customInfo": {"site.name": "west", "empty": ""}
		}
	}`), spec.DeviceStatus))
	for _, tc := range []struct {
		name       string
		expression string
		want       bool
		wantError  bool
	}{
		{"When UUID and custom info match it should pass", `systemInfo.productUuid == "uuid-1" && systemInfo.customInfo["site.name"] in ["east", "west"]`, true, false},
		{"When custom info falls outside the list it should fail", `systemInfo.customInfo["site.name"] in ["east", "north"]`, false, false},
		{"When a reported value is empty it should compare as a string", `systemInfo.customInfo["empty"] == ""`, true, false},
		{"When a custom key is missing it should fail closed", `systemInfo.customInfo["missing"] == ""`, false, true},
		{"When the serial is missing it should fail closed", `systemInfo.productSerial == ""`, false, true},
		{"When a false conjunct resolves a missing value it should return false", `systemInfo.productSerial == "" && false`, false, false},
		{"When an empty list is used it should fail", `systemInfo.productUuid in []`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := matcher.Compile(tc.expression)
			require.NoError(t, err)
			matches, err := policy.Match(spec)
			require.Equal(t, tc.want, matches)
			if tc.wantError {
				require.ErrorIs(t, err, enrollmentpolicy.ErrEvaluation)
			} else {
				require.NoError(t, err)
			}
		})
	}
	t.Run("When device status is missing it should fail closed", func(t *testing.T) {
		policy, err := matcher.Compile(`systemInfo.productUuid == "uuid-1"`)
		require.NoError(t, err)
		matches, err := policy.Match(domain.EnrollmentRequestSpec{})
		require.Error(t, err)
		require.False(t, matches)
	})
	t.Run("When custom info is missing it should fail closed", func(t *testing.T) {
		policy, err := matcher.Compile(`systemInfo.customInfo["site"] == ""`)
		require.NoError(t, err)
		matches, err := policy.Match(domain.EnrollmentRequestSpec{DeviceStatus: &domain.DeviceStatus{}})
		require.Error(t, err)
		require.False(t, matches)
	})
}

func TestMatchIdentifierSize(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	policy, err := matcher.Compile(`systemInfo.customInfo["site"] == "west"`)
	require.NoError(t, err)

	for _, tc := range []struct {
		name    string
		site    string
		matches bool
		wantErr error
	}{
		{"When unrelated identifiers exceed the limit they should be ignored", "west", true, nil},
		{"When a referenced identifier has 4096 bytes it should be evaluated", strings.Repeat("é", 2048), false, nil},
		{"When a referenced identifier exceeds 4096 bytes it should fail evaluation", strings.Repeat("é", 2049), false, enrollmentpolicy.ErrIdentifierTooLong},
	} {
		t.Run(tc.name, func(t *testing.T) {
			largeValue := strings.Repeat("ignored", 4096)
			custom := domain.CustomDeviceInfo{"site": tc.site, "unreferenced": largeValue}
			status := &domain.DeviceStatus{SystemInfo: domain.DeviceSystemInfo{CustomInfo: &custom}}
			status.SystemInfo.Set("productSerial", largeValue)
			matches, err := policy.Match(domain.EnrollmentRequestSpec{DeviceStatus: status})
			require.ErrorIs(t, err, tc.wantErr)
			require.Equal(t, tc.matches, matches)
		})
	}
}
