package v1beta1

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRegistryEndpointValidation(t *testing.T) {
	for _, tc := range []struct {
		name, endpoint string
		valid          bool
	}{
		{"When a registry hostname is supplied it should accept", "registry.example.com", true},
		{"When a numeric DNS label is supplied it should accept", "123.example.com:5000", true},
		{"When IPv4 is supplied it should accept", "192.0.2.1", true},
		{"When IPv4 has a port it should accept", "192.0.2.1:5000", true},
		{"When IPv6 has a port it should accept", "[2001:db8::1]:5000", true},
		{"When IPv4 octets overflow it should reject", "999.999.999.999", false},
		{"When invalid IPv4 has a port it should reject", "999.999.999.999:5000", false},
		{"When one IPv4 octet overflows it should reject", "256.1.2.3", false},
		{"When IPv4 has a leading zero it should reject", "192.168.001.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("repository", func(t *testing.T) {
				var spec RepositorySpec
				require.NoError(t, spec.FromOciRepoSpec(OciRepoSpec{Type: OciRepoSpecTypeOci, Registry: tc.endpoint}))
				name := "registry-endpoint"
				repository := Repository{
					ApiVersion: "v1beta1", Kind: "Repository",
					Metadata: ObjectMeta{Name: &name}, Spec: spec,
				}
				errs := repository.Validate()
				if tc.valid {
					require.Empty(t, errs)
				} else {
					require.NotEmpty(t, errs)
				}
			})
			t.Run("config resource", func(t *testing.T) {
				var resource ConfigResource
				require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(registryConfigJSON, "quay.io:5000", tc.endpoint)), &resource))
				errs := ValidateConfigResources([]ConfigResource{resource})
				if tc.valid {
					require.Empty(t, errs)
				} else {
					require.NotEmpty(t, errs)
				}
			})
		})
	}
}
