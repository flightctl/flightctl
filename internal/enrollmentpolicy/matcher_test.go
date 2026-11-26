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
		{"When a conjunction uses equality and membership it should compile", `systemInfo.productSerial == "sn-1" && systemInfo.customInfo["site"] in ["east", "west"]`, false},
		{"When the expression is true it should compile", `true`, false},
		{"When the expression is false it should compile", `false`, false},
		{"When the membership list is empty it should compile", `systemInfo.productSerial in []`, false},
		{"When the expression is malformed it should fail", `systemInfo.productSerial ==`, true},
		{"When the expression is oversized it should fail", `true` + strings.Repeat(" ", 1000), true},
		{"When a conjunction exceeds the node limit it should fail", strings.Repeat("true&&", 128) + "true", true},
		{"When the expression produces a string it should fail", `systemInfo.productSerial`, true},
		{"When the expression produces a map it should fail", `systemInfo.customInfo`, true},
		{"When the expression uses an arbitrary root it should fail", `status.systemInfo.productSerial == "sn-1"`, true},
		{"When a reported field is outside the original set it should compile", `systemInfo.hostname == "device-1"`, false},
		{"When a reported key needs quoting it should compile", `systemInfo["asset.name"] == "west"`, false},
		{"When an object has a boolean field it should compile", `systemInfo.kvm.enabled == true`, false},
		{"When a list item has a numeric field it should compile", `systemInfo.gpus[0].index == 0`, false},
		{"When an integer field is compared with a double it should fail", `systemInfo.gpus[0].memoryBytes == 4096.0`, true},
		{"When integer membership contains a double it should fail", `systemInfo.gpus[0].memoryBytes in [4096.0]`, true},
		{"When a signed integer field is compared with an unsigned integer it should fail", `systemInfo.gpus[0].index == 0u`, true},
		{"When an object literal uses a double for an integer field it should fail", `systemInfo.gpus[0] == {"index": 0.0}`, true},
		{"When a list field is compared with a list literal it should compile", `systemInfo.gpus[0].features == ["cuda"]`, false},
		{"When an object is compared with a literal of the same shape it should compile", `systemInfo.kvm == {"enabled": true}`, false},
		{"When an enrollment request label is used it should fail", `labels["site"] == "west"`, true},
		{"When an enrollment request spec path is used it should fail", `spec.labels["site"] == "west"`, true},
		{"When a list uses a negative index it should fail", `systemInfo.gpus[-1].index == 0`, true},
		{"When an object uses an unknown field it should fail", `systemInfo.kvm.unknown == "yes"`, true},
		{"When a boolean field is compared with a string it should fail", `systemInfo.kvm.enabled == "true"`, true},
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
		{"When a simple custom key uses field selection it should compile", `systemInfo.customInfo.site == "west"`, false},
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
			"hostname": "device-1",
			"asset.name": "west",
			"kvm": {"enabled": false},
			"gpus": [{"index": 0, "memoryBytes": 9007199254740993, "features": ["cuda"]}],
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
		{"When a reported string field was previously unsupported it should still match", `systemInfo.hostname == "device-1"`, true, false},
		{"When a quoted key contains a dot it should match the reported key", `systemInfo["asset.name"] == "west"`, true, false},
		{"When a nested boolean is explicitly false it should compare as a boolean", `systemInfo.kvm.enabled == false`, true, false},
		{"When a large reported integer matches it should preserve precision", `systemInfo.gpus[0].memoryBytes == 9007199254740993`, true, false},
		{"When a large reported integer differs it should not match the membership list", `systemInfo.gpus[0].memoryBytes in [9007199254740992]`, false, false},
		{"When a list matches a literal it should compare the elements", `systemInfo.gpus[0].features == ["cuda"]`, true, false},
		{"When a nested object matches a literal it should compare the fields", `systemInfo.kvm == {"enabled": false}`, true, false},
		{"When a nested report field is absent it should report an evaluation failure", `systemInfo.gpus[0].vendor == "west"`, false, true},
		{"When a list index is absent it should report an evaluation failure", `systemInfo.gpus[1].index == 0`, false, true},
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
}

func TestMatchMissingInput(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	for _, tc := range []struct {
		name       string
		expression string
		status     *domain.DeviceStatus
	}{
		{"When device status is missing it should fail closed", `systemInfo.productUuid == "uuid-1"`, nil},
		{"When custom info is missing it should fail closed", `systemInfo.customInfo["site"] == ""`, &domain.DeviceStatus{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy, err := matcher.Compile(tc.expression)
			require.NoError(t, err)
			matches, err := policy.Match(domain.EnrollmentRequestSpec{DeviceStatus: tc.status})
			require.ErrorIs(t, err, enrollmentpolicy.ErrEvaluation)
			require.False(t, matches)
		})
	}
}

func TestMatchReportedStringSize(t *testing.T) {
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
		{"When only unread strings exceed the limit it should match", "west", true, nil},
		{"When a referenced string has 4096 bytes it should be evaluated", strings.Repeat("é", 2048), false, nil},
		{"When a referenced string exceeds 4096 bytes it should fail evaluation", strings.Repeat("é", 2049), false, enrollmentpolicy.ErrReportedStringTooLong},
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

func TestMatchOversizedCollectionElement(t *testing.T) {
	matcher, err := enrollmentpolicy.NewMatcher()
	require.NoError(t, err)
	policy, err := matcher.Compile(`systemInfo.customInfo == {"site": "west"}`)
	require.NoError(t, err)
	info := domain.CustomDeviceInfo{"site": strings.Repeat("a", 4097)}
	spec := domain.EnrollmentRequestSpec{DeviceStatus: &domain.DeviceStatus{
		SystemInfo: domain.DeviceSystemInfo{CustomInfo: &info},
	}}
	matches, err := policy.Match(spec)
	require.ErrorIs(t, err, enrollmentpolicy.ErrReportedStringTooLong)
	require.False(t, matches)
}
