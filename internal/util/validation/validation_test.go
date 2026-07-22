package validation

import (
	"errors"
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	k8sutilvalidation "k8s.io/apimachinery/pkg/util/validation"
)

func TestValidateRelativePath(t *testing.T) {
	tests := []struct {
		name          string
		input         *string
		path          string
		maxLength     int
		wanteErrCount int
	}{
		{
			name:      "valid relative path",
			input:     lo.ToPtr("valid/path"),
			path:      "testPath",
			maxLength: 100,
		},
		{
			name:      "nil input",
			input:     nil,
			path:      "testPath",
			maxLength: 100,
		},
		{
			name:          "absolute path",
			input:         lo.ToPtr("/abs/path"),
			path:          "testPath",
			maxLength:     100,
			wanteErrCount: 1,
		},
		{
			name:          "path exceeds max length",
			input:         lo.ToPtr(strings.Repeat("a", 101)),
			path:          "testPath",
			maxLength:     100,
			wanteErrCount: 1,
		},
		{
			name:          "unclean path",
			input:         lo.ToPtr("unclean//path/../to"),
			path:          "testPath",
			maxLength:     100,
			wanteErrCount: 2,
		},
		{
			name:          "path with parent directory references",
			input:         lo.ToPtr("../forbidden/path"),
			path:          "testPath",
			maxLength:     100,
			wanteErrCount: 1,
		},
		{
			name:      "valid relative path with leading dot",
			input:     lo.ToPtr("./valid/relative/path"),
			path:      "testPath",
			maxLength: 100,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			errs := ValidateRelativePath(test.input, test.path, test.maxLength)
			if len(errs) != test.wanteErrCount {
				t.Errorf("%s: expected %d errors, got %d", test.name, test.wanteErrCount, len(errs))
			}
		})
	}
}

func TestDenyForbiddenDevicePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		// Forbidden roots
		{"reject /var/lib/flightctl", "/var/lib/flightctl", true},
		{"reject /var/lib/flightctl subpath", "/var/lib/flightctl/data.txt", true},
		{"reject /usr/lib/flightctl", "/usr/lib/flightctl/binary", true},
		{"reject /etc/flightctl/certs", "/etc/flightctl/certs/ca.crt", true},
		{"reject /etc/flightctl/config.yaml", "/etc/flightctl/config.yaml", true},
		{"reject /etc/flightctl/config.yml", "/etc/flightctl/config.yml", true},

		// Allowed paths
		{"allow /etc/myapp", "/etc/myapp/config.txt", false},
		{"allow /etc/flightctl custom", "/etc/flightctl/custom.txt", false},
		{"allow /var/lib/myapp", "/var/lib/myapp/data", false},
		{"allow /usr/lib/myapp", "/usr/lib/myapp/binary", false},

		// Invalid paths
		{"reject empty path", "", true},
		{"reject relative path", "relative/path", true},
		{"reject path with colon", "/etc/myapp:/etc/other", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DenyForbiddenDevicePath(tt.path)
			if tt.wantErr {
				require.Error(t, err)
				require.True(t, errors.Is(err, ErrForbiddenDevicePath), "all rejections must wrap ErrForbiddenDevicePath")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestValidateLabelKey(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		wantValid bool
	}{
		{
			name:      "valid simple key",
			key:       "environment",
			wantValid: true,
		},
		{
			name:      "valid key with hyphens",
			key:       "my-label-key",
			wantValid: true,
		},
		{
			name:      "valid key with dots",
			key:       "example.com/my-key",
			wantValid: true,
		},
		{
			name:      "valid key with underscores",
			key:       "my_key",
			wantValid: true,
		},
		{
			name:      "invalid key with spaces",
			key:       "bad key",
			wantValid: false,
		},
		{
			name:      "invalid key with special chars",
			key:       "key@value",
			wantValid: false,
		},
		{
			name:      "invalid key starting with hyphen",
			key:       "-badkey",
			wantValid: false,
		},
		{
			name:      "invalid key ending with hyphen",
			key:       "badkey-",
			wantValid: false,
		},
		{
			name:      "empty key",
			key:       "",
			wantValid: false, // K8s does not allow empty keys
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateLabelKey(tt.key)
			isValid := len(errs) == 0
			require.Equal(t, tt.wantValid, isValid, "key %q validation mismatch. Errors: %v", tt.key, errs)
		})
	}
}

func TestSanitizeLabelValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
		{
			name:     "valid label unchanged",
			input:    "valid-label",
			expected: "valid-label",
		},
		{
			name:     "valid with underscores and dots",
			input:    "test_value.123",
			expected: "test_value.123",
		},
		{
			name:     "spaces replaced with hyphens",
			input:    "CentOS Stream",
			expected: "CentOS-Stream",
		},
		{
			name:     "multiple spaces",
			input:    "Red Hat Enterprise Linux",
			expected: "Red-Hat-Enterprise-Linux",
		},
		{
			name:     "special characters replaced",
			input:    "version@2.0!test",
			expected: "version-2.0-test",
		},
		{
			name:     "leading special chars trimmed",
			input:    "!!!valid-label",
			expected: "valid-label",
		},
		{
			name:     "trailing special chars trimmed",
			input:    "valid-label!!!",
			expected: "valid-label",
		},
		{
			name:     "leading and trailing special chars trimmed",
			input:    "---valid-label---",
			expected: "valid-label",
		},
		{
			name:     "only special characters",
			input:    "!!!",
			expected: "",
		},
		{
			name:     "only hyphens",
			input:    "---",
			expected: "",
		},
		{
			name:     "IP address is valid",
			input:    "127.0.0.1",
			expected: "127.0.0.1",
		},
		{
			name:     "IPv6 colons replaced",
			input:    "::1",
			expected: "1",
		},
		{
			name:     "single character",
			input:    "a",
			expected: "a",
		},
		{
			name:     "truncate long value",
			input:    strings.Repeat("a", 100),
			expected: strings.Repeat("a", 63),
		},
		{
			name:     "truncate and trim trailing hyphens",
			input:    strings.Repeat("a", 60) + "---" + strings.Repeat("b", 10),
			expected: strings.Repeat("a", 60), // trailing hyphens are trimmed
		},
		{
			name:     "parentheses replaced",
			input:    "version(1.2.3)",
			expected: "version-1.2.3",
		},
		{
			name:     "mixed valid and invalid chars",
			input:    "Test_Label-123.v2",
			expected: "Test_Label-123.v2",
		},
		{
			name:     "unicode replaced",
			input:    "label™",
			expected: "label",
		},
		{
			name:     "slashes replaced",
			input:    "path/to/value",
			expected: "path-to-value",
		},
		{
			name:     "realistic distro name",
			input:    "Red Hat Enterprise Linux 9.5 (Plow)",
			expected: "Red-Hat-Enterprise-Linux-9.5--Plow",
		},
		{
			name:     "realistic product name",
			input:    "Dell PowerEdge R640",
			expected: "Dell-PowerEdge-R640",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeLabelValue(tt.input)
			require.Equal(t, tt.expected, result)

			// Verify result is valid if non-empty
			if result != "" {
				errs := k8sutilvalidation.IsValidLabelValue(result)
				require.Empty(t, errs, "sanitized value should be valid: %q produced %v", result, errs)
			}
		})
	}
}

func TestValidateComposePath(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name    string
		paths   []string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "no paths provided",
			paths:   []string{},
			wantErr: false,
		},
		{
			name:    "valid base only",
			paths:   []string{"docker-compose.yaml"},
			wantErr: false,
		},
		{
			name:    "valid base and override",
			paths:   []string{"docker-compose.yaml", "docker-compose.override.yaml"},
			wantErr: false,
		},
		{
			name:    "override only",
			paths:   []string{"docker-compose.override.yaml"},
			wantErr: true,
			errMsg:  "override path",
		},
		{
			name:    "too many paths",
			paths:   []string{"docker-compose.yaml", "docker-compose.override.yaml", "extra.yaml"},
			wantErr: true,
			errMsg:  "too many",
		},
		{
			name:    "invalid file name",
			paths:   []string{"weird-file.yaml"},
			wantErr: true,
			errMsg:  "invalid compose path",
		},
		{
			name:    "mismatched tool types",
			paths:   []string{"docker-compose.yaml", "podman-compose.override.yaml"},
			wantErr: true,
			errMsg:  "mismatched tool types",
		},
		{
			name:    "multiple base paths",
			paths:   []string{"docker-compose.yaml", "podman-compose.yaml"},
			wantErr: true,
			errMsg:  "multiple compose paths",
		},
		{
			name:    "nested path should be invalid",
			paths:   []string{"foo/docker-compose.yaml"},
			wantErr: true,
			errMsg:  "compose file must be at root level",
		},
		{
			name:    "typo in file name",
			paths:   []string{"docker-composee.yaml"},
			wantErr: true,
			errMsg:  "invalid compose path",
		},
		{
			name:    "typo in file extension",
			paths:   []string{"docker-composee.yl"},
			wantErr: true,
			errMsg:  ".yaml or .yml extension",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateComposePaths(tt.paths)
			if tt.wantErr {
				require.Error(err)
				require.Contains(err.Error(), tt.errMsg)
				return
			}
			require.NoError(err)
		})
	}
}
