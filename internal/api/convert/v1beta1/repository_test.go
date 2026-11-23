package v1beta1

import (
	"encoding/json"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestRepositoryDevicePullAccessRoundTrip(t *testing.T) {
	for _, mode := range []*api.OciRepoSpecAccessMode{nil, lo.ToPtr(api.Read), lo.ToPtr(api.ReadWrite)} {
		for _, flag := range []*bool{nil, lo.ToPtr(false), lo.ToPtr(true)} {
			state, err := json.Marshal(flag)
			require.NoError(t, err)
			t.Run(fmt.Sprintf("When accessMode is %s and delivery is %s it should round trip", lo.FromPtr(mode), state), func(t *testing.T) {
				spec := api.OciRepoSpec{Type: api.OciRepoSpecTypeOci, Registry: "quay.io", AccessMode: mode, DevicePullAccess: flag}
				var repo api.Repository
				repo.ApiVersion = "flightctl.io/v1beta1"
				repo.Kind = api.RepositoryKind
				repo.Metadata.Name = lo.ToPtr("registry")
				require.NoError(t, repo.Spec.FromOciRepoSpec(spec))
				require.Empty(t, repo.Validate())
				converter := NewRepositoryConverter()
				domainRepo := converter.ToDomain(repo)
				encoded, err := json.Marshal(converter.FromDomain(&domainRepo))
				require.NoError(t, err)
				var result api.Repository
				require.NoError(t, json.Unmarshal(encoded, &result))
				got, err := result.Spec.AsOciRepoSpec()
				require.NoError(t, err)
				require.Equal(t, spec, got)
				var raw map[string]any
				require.NoError(t, json.Unmarshal(encoded, &raw))
				value, present := raw["spec"].(map[string]any)["devicePullAccess"]
				require.Equal(t, flag != nil, present)
				if present {
					require.Equal(t, *flag, value)
				}
			})
		}
	}
}
