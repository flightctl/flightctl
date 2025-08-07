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
