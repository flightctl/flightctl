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
	testClientOpts := []client.ClientOption{client.WithPullSecret("/tmp/test-pull-secret")}

	tests := []struct {
		name           string
		quad           *common.QuadletReferences
		setupMocks     func(*dependency.MockPullConfigResolver)
		expectedCount  int
		expectedRefs   []string
		expectedType   dependency.OCIType
		expectedPolicy v1beta1.ImagePullPolicy
		expectedOpts   int
	}{
		{
			name: "container with single OCI image",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("nginx:latest"),
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  1,
			expectedRefs:   []string{"nginx:latest"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "container with multiple mount images",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				MountImages: []string{"alpine:3.18", "busybox:latest"},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  2,
			expectedRefs:   []string{"alpine:3.18", "busybox:latest"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "container with both main image and mount images",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/myapp:v1.0"),
				MountImages: []string{"redis:7", "postgres:15"},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  3,
			expectedRefs:   []string{"quay.io/myapp:v1.0", "redis:7", "postgres:15"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "container with pull secret",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("private-registry.io/secure-app:latest"),
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return testClientOpts
				}).AnyTimes()
			},
			expectedCount:  1,
			expectedRefs:   []string{"private-registry.io/secure-app:latest"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   1,
		},
		{
			name: "empty quadlet with no images",
			quad: &common.QuadletReferences{
				Type: common.QuadletTypeContainer,
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "image reference to quadlet file should be filtered",
			quad: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("base.image"),
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "mount images ending with .image should be filtered",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				MountImages: []string{"config.image", "data.image"},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  0,
			expectedRefs:   []string{},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "mix of OCI images and .image references",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("nginx:alpine"),
				MountImages: []string{"base.image", "redis:7", "config.image", "postgres:15"},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  3,
			expectedRefs:   []string{"nginx:alpine", "redis:7", "postgres:15"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "nil quad.Image with valid mount images",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       nil,
				MountImages: []string{"alpine:latest", "busybox:latest"},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  2,
			expectedRefs:   []string{"alpine:latest", "busybox:latest"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "valid quad.Image with empty MountImages slice",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("ubuntu:22.04"),
				MountImages: []string{},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  1,
			expectedRefs:   []string{"ubuntu:22.04"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
		{
			name: "nil pullSecret should work fine",
			quad: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("fedora:39"),
				MountImages: []string{"alpine:3.18"},
			},
			setupMocks: func(m *dependency.MockPullConfigResolver) {
				m.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			},
			expectedCount:  2,
			expectedRefs:   []string{"fedora:39", "alpine:3.18"},
			expectedType:   dependency.OCITypePodmanImage,
			expectedPolicy: v1beta1.PullIfNotPresent,
			expectedOpts:   0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			tt.setupMocks(mockResolver)

			targets := extractQuadletTargets(tt.quad, mockResolver, v1beta1.CurrentProcessUsername)

			require.Equal(tt.expectedCount, len(targets), "unexpected number of targets")

			for i, target := range targets {
				require.Equal(tt.expectedRefs[i], target.Reference, "unexpected reference at index %d", i)
				require.Equal(tt.expectedType, target.Type, "unexpected type at index %d", i)
				require.Equal(tt.expectedPolicy, target.PullPolicy, "unexpected pull policy at index %d", i)
				if target.ClientOptsFn != nil {
					require.Equal(tt.expectedOpts, len(target.ClientOptsFn()), "unexpected client opts count at index %d", i)
				} else {
					require.Equal(0, tt.expectedOpts, "expected no client opts but expectedOpts was %d at index %d", tt.expectedOpts, i)
				}
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

type mockProvider struct {
	id   string
	name string
	spec *ApplicationSpec
}

func (m *mockProvider) ID() string                                 { return m.id }
func (m *mockProvider) Name() string                               { return m.name }
func (m *mockProvider) Spec() *ApplicationSpec                     { return m.spec }
func (m *mockProvider) Verify(_ context.Context) error             { return nil }
func (m *mockProvider) Install(_ context.Context) error            { return nil }
func (m *mockProvider) Remove(_ context.Context) error             { return nil }
func (m *mockProvider) IsEqual(other Provider) bool                { return m.id == other.ID() }
func (m *mockProvider) ActionSpec() interface{}                    { return nil }
func (m *mockProvider) EnsureDependencies(_ context.Context) error { return nil }

func newMockProvider(id, name string) *mockProvider {
	return &mockProvider{id: id, name: name, spec: &ApplicationSpec{ID: id, Name: name}}
}

func TestGetDiff_DeterministicOrdering(t *testing.T) {
	tests := []struct {
		name            string
		current         []Provider
		desired         []Provider
		expectedRemoved []string
		expectedEnsure  []string
		expectedChanged []string
	}{
		{
			name: "removed apps are sorted by ID",
			current: []Provider{
				newMockProvider("zebra", "Zebra App"),
				newMockProvider("alpha", "Alpha App"),
				newMockProvider("mike", "Mike App"),
			},
			desired:         []Provider{},
			expectedRemoved: []string{"alpha", "mike", "zebra"},
			expectedEnsure:  []string{},
			expectedChanged: []string{},
		},
		{
			name:    "ensured apps are sorted by ID",
			current: []Provider{},
			desired: []Provider{
				newMockProvider("zebra", "Zebra App"),
				newMockProvider("alpha", "Alpha App"),
				newMockProvider("mike", "Mike App"),
			},
			expectedRemoved: []string{},
			expectedEnsure:  []string{"alpha", "mike", "zebra"},
			expectedChanged: []string{},
		},
		{
			name: "mixed operations are each sorted by ID",
			current: []Provider{
				newMockProvider("charlie", "Charlie App"),
				newMockProvider("alpha", "Alpha App"),
			},
			desired: []Provider{
				newMockProvider("bravo", "Bravo App"),
				newMockProvider("alpha", "Alpha App"),
			},
			expectedRemoved: []string{"charlie"},
			expectedEnsure:  []string{"alpha", "bravo"},
			expectedChanged: []string{},
		},
		{
			name: "ordering is consistent across multiple runs",
			current: []Provider{
				newMockProvider("d", "D"),
				newMockProvider("b", "B"),
				newMockProvider("a", "A"),
				newMockProvider("c", "C"),
			},
			desired:         []Provider{},
			expectedRemoved: []string{"a", "b", "c", "d"},
			expectedEnsure:  []string{},
			expectedChanged: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			diff, err := GetDiff(tt.current, tt.desired)
			require.NoError(err)

			removedIDs := make([]string, len(diff.Removed))
			for i, p := range diff.Removed {
				removedIDs[i] = p.ID()
			}
			require.Equal(tt.expectedRemoved, removedIDs, "removed apps should be sorted by ID")

			ensureIDs := make([]string, len(diff.Ensure))
			for i, p := range diff.Ensure {
				ensureIDs[i] = p.ID()
			}
			require.Equal(tt.expectedEnsure, ensureIDs, "ensured apps should be sorted by ID")

			changedIDs := make([]string, len(diff.Changed))
			for i, p := range diff.Changed {
				changedIDs[i] = p.ID()
			}
			require.Equal(tt.expectedChanged, changedIDs, "changed apps should be sorted by ID")
		})
	}
}

func TestGetDiff_DeterministicOrdering_Consistency(t *testing.T) {
	require := require.New(t)

	current := []Provider{
		newMockProvider("z", "Z"),
		newMockProvider("m", "M"),
		newMockProvider("a", "A"),
		newMockProvider("f", "F"),
	}
	desired := []Provider{}

	for i := 0; i < 100; i++ {
		diff, err := GetDiff(current, desired)
		require.NoError(err)

		removedIDs := make([]string, len(diff.Removed))
		for j, p := range diff.Removed {
			removedIDs[j] = p.ID()
		}
		require.Equal([]string{"a", "f", "m", "z"}, removedIDs,
			"ordering should be consistent on iteration %d", i)
	}
}
