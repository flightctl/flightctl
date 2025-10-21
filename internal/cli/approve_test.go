package cli

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseAndValidateKindNameFromArgsApprove(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		expectedKind  string
		expectedName  string
		expectError   bool
		errorContains string
	}{
		// EnrollmentRequest tests - slash format
		{
			name:         "enrollment_request_slash_format",
			args:         []string{"enrollmentrequest/test1"},
			expectedKind: EnrollmentRequestKind,
			expectedName: "test1",
			expectError:  false,
		},
		{
			name:         "er_short_slash_format",
			args:         []string{"er/test1"},
			expectedKind: EnrollmentRequestKind,
			expectedName: "test1",
			expectError:  false,
		},
		{
			name:         "plural_enrollmentrequest_slash_format",
			args:         []string{"enrollmentrequests/test1"},
			expectedKind: EnrollmentRequestKind,
			expectedName: "test1",
			expectError:  false,
		},

		// EnrollmentRequest tests - space format (new functionality)
		{
			name:         "enrollment_request_space_format",
			args:         []string{"enrollmentrequest", "test1"},
			expectedKind: EnrollmentRequestKind,
			expectedName: "test1",
			expectError:  false,
		},
		{
			name:         "er_short_space_format",
			args:         []string{"er", "test1"},
			expectedKind: EnrollmentRequestKind,
			expectedName: "test1",
			expectError:  false,
		},
		{
			name:         "plural_enrollmentrequest_space_format",
			args:         []string{"enrollmentrequests", "test1"},
			expectedKind: EnrollmentRequestKind,
			expectedName: "test1",
			expectError:  false,
		},

		// CertificateSigningRequest tests - slash format
		{
			name:         "csr_slash_format",
			args:         []string{"certificatesigningrequest/test1"},
			expectedKind: CertificateSigningRequestKind,
			expectedName: "test1",
			expectError:  false,
		},
		{
			name:         "csr_short_slash_format",
			args:         []string{"csr/test1"},
			expectedKind: CertificateSigningRequestKind,
			expectedName: "test1",
			expectError:  false,
		},

		// CertificateSigningRequest tests - space format (new functionality)
		{
			name:         "csr_space_format",
			args:         []string{"certificatesigningrequest", "test1"},
			expectedKind: CertificateSigningRequestKind,
			expectedName: "test1",
			expectError:  false,
		},
		{
			name:         "csr_short_space_format",
			args:         []string{"csr", "test1"},
			expectedKind: CertificateSigningRequestKind,
			expectedName: "test1",
			expectError:  false,
		},

		// Error cases
		{
			name:          "no_arguments",
			args:          []string{},
			expectError:   true,
			errorContains: "no arguments provided",
		},
		{
			name:          "slash_format_empty_name",
			args:          []string{"er/"},
			expectError:   true,
			errorContains: "resource name cannot be empty when using TYPE/NAME format",
		},
		{
			name:          "slash_format_with_extra_args",
			args:          []string{"er/test1", "extra"},
			expectError:   true,
			errorContains: "cannot mix TYPE/NAME syntax with additional arguments",
		},
		{
			name:          "space_format_too_many_args",
			args:          []string{"er", "test1", "test2"},
			expectError:   true,
			errorContains: "exactly one resource name must be specified",
		},
		{
			name:          "space_format_no_name",
			args:          []string{"er"},
			expectError:   true,
			errorContains: "exactly one resource name must be specified",
		},
		{
			name:          "invalid_resource_type",
			args:          []string{"invalidtype", "test1"},
			expectError:   true,
			errorContains: "invalid resource kind: invalidtype",
		},
		{
			name:          "invalid_resource_type_slash",
			args:          []string{"invalidtype/test1"},
			expectError:   true,
			errorContains: "invalid resource kind: invalidtype",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)

			kind, name, err := parseAndValidateKindNameFromArgsSingle(tc.args)
			if tc.expectError {
				require.Error(err, "expected an error but got none")
				if tc.errorContains != "" {
					require.Contains(err.Error(), tc.errorContains,
						"expected error to contain %q", tc.errorContains)
				}
				return
			}

			require.NoError(err, "unexpected error")
			require.Equal(tc.expectedKind, kind, "unexpected kind")
			require.Equal(tc.expectedName, name, "unexpected name")
		})
	}
}
