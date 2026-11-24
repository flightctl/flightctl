package validation

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateHostIPOrFQDNWithOptionalPort(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		valid          bool
	}{
		{"When a hostname is supplied it should accept", "localhost", true},
		{"When a numeric DNS label is supplied it should accept", "123.example.com", true},
		{"When a hostname has a port it should accept", "registry.example.com:5000", true},
		{"When IPv4 is supplied it should accept", "192.0.2.1", true},
		{"When IPv4 has a port it should accept", "192.0.2.1:65535", true},
		{"When bracketed IPv6 is supplied it should accept", "[2001:db8::1]", true},
		{"When bracketed IPv6 has a port it should accept", "[2001:db8::1]:5000", true},
		{"When every IPv4 octet overflows it should reject", "999.999.999.999", false},
		{"When invalid IPv4 has a port it should reject", "999.999.999.999:5000", false},
		{"When the first IPv4 octet overflows it should reject", "256.1.2.3", false},
		{"When the last IPv4 octet overflows it should reject", "192.0.2.256", false},
		{"When IPv4 has a leading zero it should reject", "192.168.001.1", false},
		{"When IPv6 is malformed it should reject", "[2001:::1]:5000", false},
		{"When the port is zero it should reject", "192.0.2.1:0", false},
		{"When the port overflows it should reject", "registry.example.com:65536", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			errs := ValidateHostIPOrFQDNWithOptionalPort(&tc.endpoint, "registry")
			if tc.valid {
				require.Empty(t, errs)
			} else {
				require.NotEmpty(t, errs)
			}
		})
	}
}
