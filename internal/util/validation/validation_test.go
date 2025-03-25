package validation

import (
	"strings"
	"testing"

	"github.com/samber/lo"
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
