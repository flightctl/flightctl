package service

import (
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestValidateImageDestOciSpec(t *testing.T) {
	tests := []struct {
		name      string
		spec      *domain.OciRepoSpec
		imageName string
		wantErr   string
	}{
		{
			name: "When namespace is set it should reject",
			spec: &domain.OciRepoSpec{
				Registry:  "my-registry.com",
				Type:      domain.OciRepoSpecTypeOci,
				Namespace: lo.ToPtr("my-org"),
			},
			imageName: "nginx/nginx",
			wantErr:   "namespace",
		},
		{
			name: "When repository does not equal imageName it should reject",
			spec: &domain.OciRepoSpec{
				Registry:   "my-registry.com",
				Type:       domain.OciRepoSpecTypeOci,
				Repository: lo.ToPtr("my-org/diffs"),
			},
			imageName: "nginx/nginx",
			wantErr:   "imageName",
		},
		{
			name: "When repository equals imageName it should accept",
			spec: &domain.OciRepoSpec{
				Registry:   "my-registry.com",
				Type:       domain.OciRepoSpecTypeOci,
				Repository: lo.ToPtr("nginx/nginx"),
			},
			imageName: "nginx/nginx",
		},
		{
			name: "When repository and namespace are omitted it should accept",
			spec: &domain.OciRepoSpec{
				Registry: "my-registry.com",
				Type:     domain.OciRepoSpecTypeOci,
			},
			imageName: "nginx/nginx",
		},
		{
			name:      "When spec is nil it should accept",
			spec:      nil,
			imageName: "nginx/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageDestOciSpec(tt.spec, tt.imageName, "spec.destination.repository")
			if tt.wantErr == "" {
				require.Empty(t, errs)
				return
			}
			require.NotEmpty(t, errs)
			found := false
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.wantErr) {
					found = true
					break
				}
			}
			require.True(t, found, "expected error containing %q, got %v", tt.wantErr, errs)
		})
	}
}

func TestValidateImageSourceOciSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    *domain.OciRepoSpec
		wantErr string
	}{
		{
			name: "When namespace is set it should reject",
			spec: &domain.OciRepoSpec{
				Registry:  "my-registry.com",
				Type:      domain.OciRepoSpecTypeOci,
				Namespace: lo.ToPtr("my-org"),
			},
			wantErr: "namespace",
		},
		{
			name: "When repository is set it should reject",
			spec: &domain.OciRepoSpec{
				Registry:   "my-registry.com",
				Type:       domain.OciRepoSpecTypeOci,
				Repository: lo.ToPtr("upstream/os"),
			},
			wantErr: "repository",
		},
		{
			name: "When repository and namespace are omitted it should accept",
			spec: &domain.OciRepoSpec{
				Registry: "my-registry.com",
				Type:     domain.OciRepoSpecTypeOci,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateImageSourceOciSpec(tt.spec, "spec.source.repository")
			if tt.wantErr == "" {
				require.Empty(t, errs)
				return
			}
			require.NotEmpty(t, errs)
			found := false
			for _, err := range errs {
				if strings.Contains(err.Error(), tt.wantErr) {
					found = true
					break
				}
			}
			require.True(t, found, "expected error containing %q, got %v", tt.wantErr, errs)
		})
	}
}
