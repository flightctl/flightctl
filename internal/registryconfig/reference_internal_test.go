package registryconfig

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseNeedReferencePath(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tt := range []struct {
		name, ref, wantRegistry, wantPath string
		consumer                          Consumer
	}{
		{"single-label chart host", "oci://registry/team/chart:1", "registry", "team/chart", ConsumerHelmChart},
		{"chart without transport", "registry/team/chart:1", "registry", "team/chart", ConsumerHelmChart},
		{"chart digest", "oci://registry/team/chart@" + digest, "registry", "team/chart", ConsumerHelmChart},
		{"chart tag and digest", "oci://registry/team/chart:1@" + digest, "registry", "team/chart", ConsumerHelmChart},
		{"chart path repeating a single-label host", "oci://registry/registry/team/chart:1", "registry", "registry/team/chart", ConsumerHelmChart},
		{"dotted chart host", "oci://registry.example/team/chart:1", "registry.example", "team/chart", ConsumerHelmChart},
		{"chart path repeating a dotted host", "oci://registry.example/registry.example/team/chart:1", "registry.example", "registry.example/team/chart", ConsumerHelmChart},
		{"chart host with port", "oci://registry:5000/team/chart:1", "registry:5000", "team/chart", ConsumerHelmChart},
		{"localhost chart host", "oci://localhost/team/chart:1", "localhost", "team/chart", ConsumerHelmChart},
		{"IPv6 chart host", "oci://[::1]:5000/team/chart:1", "[::1]:5000", "team/chart", ConsumerHelmChart},
		{"uppercase chart host", "oci://REGISTRY/team/chart:1", "registry", "team/chart", ConsumerHelmChart},
		{"Docker Hub chart alias", "oci://index.docker.io/team/chart:1", "docker.io", "team/chart", ConsumerHelmChart},
		{"chart path repeating a Docker Hub alias", "oci://index.docker.io/index.docker.io/team/chart:1", "docker.io", "index.docker.io/team/chart", ConsumerHelmChart},
		{"Docker Hub chart library", "oci://docker.io/chart:1", "docker.io", "library/chart", ConsumerHelmChart},
		{"Docker Hub registry alias chart", "oci://registry-1.docker.io/team/chart:1", "docker.io", "team/chart", ConsumerHelmChart},
		{"chart path repeating a registry alias", "oci://registry-1.docker.io/registry-1.docker.io/team/chart:1", "docker.io", "registry-1.docker.io/team/chart", ConsumerHelmChart},
		{"Docker Hub registry alias chart library", "oci://registry-1.docker.io/chart:1", "docker.io", "library/chart", ConsumerHelmChart},
		{"Docker Hub registry alias image library", "docker://registry-1.docker.io/nginx:1", "docker.io", "library/nginx", ConsumerContainer},
		{"Docker Hub registry alias image digest", "registry-1.docker.io/nginx@" + digest, "docker.io", "library/nginx", ConsumerContainer},
		{"container image namespace", "docker://registry/team/chart:1", "docker.io", "registry/team/chart", ConsumerContainer},
		{"Helm workload image namespace", "docker://registry/team/chart:1", "docker.io", "registry/team/chart", ConsumerHelmImage},
		{"OS image namespace", "docker://registry/team/chart:1", "docker.io", "registry/team/chart", ConsumerOS},
		{"short container image", "nginx:latest", "docker.io", "library/nginx", ConsumerContainer},
	} {
		t.Run("When parsing a "+tt.name+" it should preserve the registry-relative path", func(t *testing.T) {
			registry, path, err := parseNeed(PullNeed{Reference: tt.ref, Consumer: tt.consumer, Destination: "root"})
			require.NoError(t, err)
			require.Equal(t, tt.wantRegistry, registry)
			require.Equal(t, tt.wantPath, path)
		})
	}
}
