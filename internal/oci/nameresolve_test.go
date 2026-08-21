package oci

import (
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestResolveDeltaPushPath(t *testing.T) {
	tests := []struct {
		name            string
		spec            *domain.OciRepoSpec
		imageRepository string
		want            string
		wantErr         bool
	}{
		{
			name: "When neither repository nor namespace is set it should use registry plus image path",
			spec: &domain.OciRepoSpec{
				Registry: "my-registry.com",
				Type:     domain.OciRepoSpecTypeOci,
			},
			imageRepository: "quay.io/nginx/nginx",
			want:            "my-registry.com/nginx/nginx",
		},
		{
			name: "When repository is set it should use registry plus repository",
			spec: &domain.OciRepoSpec{
				Registry:   "my-registry.com",
				Type:       domain.OciRepoSpecTypeOci,
				Repository: lo.ToPtr("my-org/diffs"),
			},
			imageRepository: "quay.io/nginx/nginx",
			want:            "my-registry.com/my-org/diffs",
		},
		{
			name: "When namespace is set it should use registry plus namespace plus last path segment",
			spec: &domain.OciRepoSpec{
				Registry:  "my-registry.com",
				Type:      domain.OciRepoSpecTypeOci,
				Namespace: lo.ToPtr("my-org"),
			},
			imageRepository: "quay.io/nginx/nginx",
			want:            "my-registry.com/my-org/nginx",
		},
		{
			name: "When namespace is set on a two-segment path it should use the last segment",
			spec: &domain.OciRepoSpec{
				Registry:  "my-registry.com",
				Type:      domain.OciRepoSpecTypeOci,
				Namespace: lo.ToPtr("my-org"),
			},
			imageRepository: "quay.io/team-a/os",
			want:            "my-registry.com/my-org/os",
		},
		{
			name: "When repository and namespace are both set it should return an error",
			spec: &domain.OciRepoSpec{
				Registry:   "my-registry.com",
				Type:       domain.OciRepoSpecTypeOci,
				Repository: lo.ToPtr("my-org/diffs"),
				Namespace:  lo.ToPtr("my-org"),
			},
			imageRepository: "quay.io/nginx/nginx",
			wantErr:         true,
		},
		{
			name: "When registry is empty it should return an error",
			spec: &domain.OciRepoSpec{
				Type: domain.OciRepoSpecTypeOci,
			},
			imageRepository: "quay.io/nginx/nginx",
			wantErr:         true,
		},
		{
			name:            "When spec is nil it should return an error",
			spec:            nil,
			imageRepository: "quay.io/nginx/nginx",
			wantErr:         true,
		},
		{
			name: "When imageRepository uses index.docker.io it should strip that host",
			spec: &domain.OciRepoSpec{
				Registry: "my-registry.com",
				Type:     domain.OciRepoSpecTypeOci,
			},
			imageRepository: "index.docker.io/nginx/nginx",
			want:            "my-registry.com/nginx/nginx",
		},
		{
			name: "When imageRepository has no host it should return an error",
			spec: &domain.OciRepoSpec{
				Registry: "my-registry.com",
				Type:     domain.OciRepoSpecTypeOci,
			},
			imageRepository: "nginx/nginx",
			wantErr:         true,
		},
		{
			name: "When imageRepository is unparseable it should return an error",
			spec: &domain.OciRepoSpec{
				Registry: "my-registry.com",
				Type:     domain.OciRepoSpecTypeOci,
			},
			imageRepository: ":::not-a-ref",
			wantErr:         true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveDeltaPushPath(tt.spec, tt.imageRepository)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestSelectWriteTarget(t *testing.T) {
	org := &domain.OciRepoSpec{Registry: "org-registry.com", Type: domain.OciRepoSpecTypeOci}
	def := &domain.OciRepoSpec{Registry: "default-registry.com", Type: domain.OciRepoSpecTypeOci}
	emptyDefault := &domain.OciRepoSpec{Type: domain.OciRepoSpecTypeOci}

	t.Run("When an org target is set it should return the org target", func(t *testing.T) {
		require.Equal(t, org, SelectWriteTarget(org, def))
	})

	t.Run("When the org target is nil it should return the default target", func(t *testing.T) {
		require.Equal(t, def, SelectWriteTarget(nil, def))
	})

	t.Run("When both are nil it should return nil", func(t *testing.T) {
		require.Nil(t, SelectWriteTarget(nil, nil))
	})

	t.Run("When the default has no registry it should return nil", func(t *testing.T) {
		require.Nil(t, SelectWriteTarget(nil, emptyDefault))
	})
}

func TestImageDestRef(t *testing.T) {
	require.Equal(t, "my-registry.com/nginx/nginx:latest", ImageDestRef("my-registry.com", "nginx/nginx", "latest"))
}

func TestRepoDestRef(t *testing.T) {
	require.Equal(t, "my-registry.com/nginx/nginx", RepoDestRef("my-registry.com", "nginx/nginx"))
}

func TestSelectWriteTargetUsesDefaultRepository(t *testing.T) {
	spec := (&config.DefaultRepositoryConfig{
		Registry:   "my-registry.com",
		Repository: lo.ToPtr("my-org/diffs"),
		Username:   "delta-user",
		Password:   "delta-pass",
	}).OciRepoSpec()
	require.NotNil(t, spec)

	selected := SelectWriteTarget(nil, spec)
	require.Equal(t, spec, selected)

	path, err := ResolveDeltaPushPath(selected, "quay.io/nginx/nginx")
	require.NoError(t, err)
	require.Equal(t, "my-registry.com/my-org/diffs", path)

	docker, err := selected.OciAuth.AsDockerAuth()
	require.NoError(t, err)
	require.Equal(t, "delta-user", docker.Username)
	require.Equal(t, "delta-pass", docker.Password)
}
