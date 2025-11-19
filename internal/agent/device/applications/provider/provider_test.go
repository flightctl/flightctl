package provider

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestExtractQuadletTargets(t *testing.T) {
	testPullSecret := &client.PullSecret{
		Path:    "/tmp/test-pull-secret",
		Cleanup: func() {},
	}

	tests := []struct {
		name           string
		quad           *common.QuadletReferences
		pullSecret     *client.PullSecret
		expectedCount  int
		expectedRefs   []string
		expectedType   dependency.OCIType
		expectedPolicy v1alpha1.ImagePullPolicy
	}{
		{
			name: "container with single OCI image",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("nginx:latest"),
			},
			pullSecret:     nil,
			expectedCount:  1,
			expectedRefs:   []string{"nginx:latest"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "container with multiple mount images",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				MountImages: []string{"alpine:3.18", "busybox:latest"},
			},
			pullSecret:     nil,
			expectedCount:  2,
			expectedRefs:   []string{"alpine:3.18", "busybox:latest"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "container with both main image and mount images",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/myapp:v1.0"),
				MountImages: []string{"redis:7", "postgres:15"},
			},
			pullSecret:     nil,
			expectedCount:  3,
			expectedRefs:   []string{"quay.io/myapp:v1.0", "redis:7", "postgres:15"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "container with pull secret",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("private-registry.io/secure-app:latest"),
			},
			pullSecret:     testPullSecret,
			expectedCount:  1,
			expectedRefs:   []string{"private-registry.io/secure-app:latest"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "empty quadlet with no images",
			quad: &common.QuadletReferences{
				Type: common.QuadletTypeContainer,
			},
			pullSecret:     nil,
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "image reference to quadlet file should be filtered",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("base.image"),
			},
			pullSecret:     nil,
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "mount images ending with .image should be filtered",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				MountImages: []string{"config.image", "data.image"},
			},
			pullSecret:     nil,
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "mix of OCI images and .image references",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("nginx:alpine"),
				MountImages: []string{"base.image", "redis:7", "config.image", "postgres:15"},
			},
			pullSecret:     nil,
			expectedCount:  3,
			expectedRefs:   []string{"nginx:alpine", "redis:7", "postgres:15"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "nil quad.Image with valid mount images",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       nil,
				MountImages: []string{"alpine:latest", "busybox:latest"},
			},
			pullSecret:     nil,
			expectedCount:  2,
			expectedRefs:   []string{"alpine:latest", "busybox:latest"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "valid quad.Image with empty MountImages slice",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("ubuntu:22.04"),
				MountImages: []string{},
			},
			pullSecret:     nil,
			expectedCount:  1,
			expectedRefs:   []string{"ubuntu:22.04"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
		{
			name: "nil pullSecret should work fine",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("fedora:39"),
				MountImages: []string{"alpine:3.18"},
			},
			pullSecret:     nil,
			expectedCount:  2,
			expectedRefs:   []string{"fedora:39", "alpine:3.18"},
			expectedType:   dependency.OCITypeImage,
			expectedPolicy: v1alpha1.PullIfNotPresent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			targets := extractQuadletTargets(tt.quad, tt.pullSecret)

			require.Equal(tt.expectedCount, len(targets), "unexpected number of targets")

			for i, target := range targets {
				require.Equal(tt.expectedRefs[i], target.Reference, "unexpected reference at index %d", i)
				require.Equal(tt.expectedType, target.Type, "unexpected type at index %d", i)
				require.Equal(tt.expectedPolicy, target.PullPolicy, "unexpected pull policy at index %d", i)
				require.Equal(tt.pullSecret, target.PullSecret, "unexpected pull secret at index %d", i)
			}
		})
	}
}
