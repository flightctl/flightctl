package validation

import (
	"strings"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
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
			} else {
				require.NoError(t, err)
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
