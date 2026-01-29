package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestExtractQuadletTargets(t *testing.T) {
	testPullSecret := &client.PullConfig{
		Path:    "/tmp/test-pull-secret",
		Cleanup: func() {},
	}
	testPullSecretProvider := client.NewPullConfigProvider(map[client.ConfigType]*client.PullConfig{
		client.ConfigTypeContainerSecret: testPullSecret,
	})

	tests := []struct {
		name           string
		quad           *common.QuadletReferences
		pullSecret     client.PullConfigProvider
		expectedCount  int
		expectedRefs   []string
		expectedType   dependency.OCIType
		expectedPolicy v1beta1.ImagePullPolicy
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
		},
		{
			name: "container with pull secret",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("private-registry.io/secure-app:latest"),
			},
			pullSecret:     testPullSecretProvider,
			expectedCount:  1,
			expectedRefs:   []string{"private-registry.io/secure-app:latest"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
		},
		{
			name: "empty quadlet with no images",
			quad: &common.QuadletReferences{
				Type: common.QuadletTypeContainer,
			},
			pullSecret:     nil,
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
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
				require.Equal(tt.pullSecret, target.Configs, "unexpected configs at index %d", i)
			}
		})
	}
}

func TestCollectProviderTargetsDeferredDependencies(t *testing.T) {
	tests := []struct {
		name                string
		providers           func(ctrl *gomock.Controller) []appProvider
		expectedTargetCount int
		expectedTargetRefs  []string
		wantErr             bool
		wantErrContains     string
		wantErrEnsuringDeps bool
	}{
		{
			name: "all providers succeed",
			providers: func(ctrl *gomock.Controller) []appProvider {
				p1 := NewMockappProvider(ctrl)
				p1.EXPECT().Name().Return("app1").AnyTimes()
				p1.EXPECT().Spec().Return(&ApplicationSpec{User: v1beta1.CurrentProcessUsername}).AnyTimes()
				p1.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
				p1.EXPECT().collectOCITargets(gomock.Any(), gomock.Any()).Return(
					dependency.OCIPullTargetsByUser{
						v1beta1.CurrentProcessUsername: []dependency.OCIPullTarget{
							{Reference: "nginx:latest", Type: dependency.OCITypePodmanImage},
						},
					}, nil)
				p1.EXPECT().parentIsAvailable(gomock.Any()).Return("nginx:latest", "sha256:abc", false, nil)

				p2 := NewMockappProvider(ctrl)
				p2.EXPECT().Name().Return("app2").AnyTimes()
				p2.EXPECT().Spec().Return(&ApplicationSpec{User: v1beta1.CurrentProcessUsername}).AnyTimes()
				p2.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
				p2.EXPECT().collectOCITargets(gomock.Any(), gomock.Any()).Return(
					dependency.OCIPullTargetsByUser{
						v1beta1.CurrentProcessUsername: []dependency.OCIPullTarget{
							{Reference: "redis:7", Type: dependency.OCITypePodmanImage},
						},
					}, nil)
				p2.EXPECT().parentIsAvailable(gomock.Any()).Return("redis:7", "sha256:def", false, nil)

				return []appProvider{p1, p2}
			},
			expectedTargetCount: 2,
			expectedTargetRefs:  []string{"nginx:latest", "redis:7"},
			wantErr:             false,
		},
		{
			name: "one provider fails dependencies, others succeed",
			providers: func(ctrl *gomock.Controller) []appProvider {
				p1 := NewMockappProvider(ctrl)
				p1.EXPECT().Name().Return("helm-app").AnyTimes()
				p1.EXPECT().EnsureDependencies(gomock.Any()).Return(
					fmt.Errorf("%w: helm binary not found", errors.ErrAppDependency))

				p2 := NewMockappProvider(ctrl)
				p2.EXPECT().Name().Return("container-app").AnyTimes()
				p2.EXPECT().Spec().Return(&ApplicationSpec{User: v1beta1.CurrentProcessUsername}).AnyTimes()
				p2.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
				p2.EXPECT().collectOCITargets(gomock.Any(), gomock.Any()).Return(
					dependency.OCIPullTargetsByUser{
						v1beta1.CurrentProcessUsername: []dependency.OCIPullTarget{
							{Reference: "postgres:15", Type: dependency.OCITypePodmanImage},
						},
					}, nil)
				p2.EXPECT().parentIsAvailable(gomock.Any()).Return("postgres:15", "sha256:ghi", false, nil)

				return []appProvider{p1, p2}
			},
			expectedTargetCount: 1,
			expectedTargetRefs:  []string{"postgres:15"},
			wantErr:             true,
			wantErrContains:     "helm-app",
			wantErrEnsuringDeps: true,
		},
		{
			name: "multiple providers fail dependencies",
			providers: func(ctrl *gomock.Controller) []appProvider {
				p1 := NewMockappProvider(ctrl)
				p1.EXPECT().Name().Return("helm-app").AnyTimes()
				p1.EXPECT().EnsureDependencies(gomock.Any()).Return(
					fmt.Errorf("%w: helm binary not found", errors.ErrAppDependency))

				p2 := NewMockappProvider(ctrl)
				p2.EXPECT().Name().Return("quadlet-app").AnyTimes()
				p2.EXPECT().EnsureDependencies(gomock.Any()).Return(
					fmt.Errorf("%w: podman binary not found", errors.ErrAppDependency))

				p3 := NewMockappProvider(ctrl)
				p3.EXPECT().Name().Return("container-app").AnyTimes()
				p3.EXPECT().Spec().Return(&ApplicationSpec{User: v1beta1.CurrentProcessUsername}).AnyTimes()
				p3.EXPECT().EnsureDependencies(gomock.Any()).Return(nil)
				p3.EXPECT().collectOCITargets(gomock.Any(), gomock.Any()).Return(
					dependency.OCIPullTargetsByUser{
						v1beta1.CurrentProcessUsername: []dependency.OCIPullTarget{
							{Reference: "alpine:latest", Type: dependency.OCITypePodmanImage},
						},
					}, nil)
				p3.EXPECT().parentIsAvailable(gomock.Any()).Return("alpine:latest", "sha256:jkl", false, nil)

				return []appProvider{p1, p2, p3}
			},
			expectedTargetCount: 1,
			expectedTargetRefs:  []string{"alpine:latest"},
			wantErr:             true,
			wantErrContains:     "helm-app",
			wantErrEnsuringDeps: true,
		},
		{
			name: "all providers fail dependencies",
			providers: func(ctrl *gomock.Controller) []appProvider {
				p1 := NewMockappProvider(ctrl)
				p1.EXPECT().Name().Return("helm-app").AnyTimes()
				p1.EXPECT().EnsureDependencies(gomock.Any()).Return(
					fmt.Errorf("%w: helm binary not found", errors.ErrAppDependency))

				p2 := NewMockappProvider(ctrl)
				p2.EXPECT().Name().Return("quadlet-app").AnyTimes()
				p2.EXPECT().EnsureDependencies(gomock.Any()).Return(
					fmt.Errorf("%w: podman binary not found", errors.ErrAppDependency))

				return []appProvider{p1, p2}
			},
			expectedTargetCount: 0,
			expectedTargetRefs:  []string{},
			wantErr:             true,
			wantErrContains:     "helm-app",
			wantErrEnsuringDeps: true,
		},
		{
			name: "non-deferrable error stops processing",
			providers: func(ctrl *gomock.Controller) []appProvider {
				p1 := NewMockappProvider(ctrl)
				p1.EXPECT().Name().Return("app1").AnyTimes()
				p1.EXPECT().EnsureDependencies(gomock.Any()).Return(
					fmt.Errorf("critical error: permission denied"))

				return []appProvider{p1}
			},
			expectedTargetCount: 0,
			expectedTargetRefs:  []string{},
			wantErr:             true,
			wantErrContains:     "critical error",
			wantErrEnsuringDeps: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			providers := tt.providers(ctrl)
			ctx := context.Background()

			collection, err := collectProviderTargets(ctx, logger, providers, nil, NewOCITargetCache(), NewAppDataCache())

			if tt.wantErr {
				require.Error(err)
				require.Contains(err.Error(), tt.wantErrContains)
				if tt.wantErrEnsuringDeps {
					require.ErrorIs(err, errors.ErrAppDependency)
				}
			} else {
				require.NoError(err)
			}

			if collection != nil {
				allTargets := collection.Targets[v1beta1.CurrentProcessUsername]
				require.Equal(tt.expectedTargetCount, len(allTargets), "unexpected number of targets")

				for i, expectedRef := range tt.expectedTargetRefs {
					require.Equal(expectedRef, allTargets[i].Reference, "unexpected reference at index %d", i)
				}
			} else if tt.expectedTargetCount > 0 {
				require.Fail("expected collection to be non-nil")
			}
		})
	}
}
