package os

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	testDesiredImage  = "quay.io/acme/os:v2"
	testBootedImage   = "quay.io/acme/os:v1"
	testDeltaRef      = "quay.io/acme/os@" + testDeltaDigest
	testReferrersJSON = `{
		"schemaVersion": 2,
		"manifests": [
			{
				"digest": "` + testDeltaDigest + `",
				"artifactType": "application/vnd.io.github.containers.oci-delta.v1",
				"annotations": {
					"io.github.containers.delta.source": "` + testSourceDigest + `"
				}
			}
		]
	}`
)

func TestManagerStatus(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name              string
		caps              Capabilities
		fallbackReason    *string
		bootedImage       string
		bootedImageDigest string
		expectedImage     string
		expectedDigest    string
		expectedEligible  bool
		expectedReason    *string
	}{
		{
			name:              "When image mode and delta eligible it should populate os fields and DeltaEligible true",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
		},
		{
			name:              "When image mode and not delta eligible it should report DeltaEligible false",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: false, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  false,
		},
		{
			name:              "When package mode it should report empty os fields and DeltaEligible false",
			caps:              Capabilities{OsMode: v1beta1.OsModePackage, DeltaEligible: false, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			bootedImage:       "",
			bootedImageDigest: "",
			expectedImage:     "",
			expectedDigest:    "",
			expectedEligible:  false,
		},
		{
			name:              "When fallback reason is set it should copy it to status",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			fallbackReason:    lo.ToPtr(fallbackReasonApply),
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
			expectedReason:    lo.ToPtr(fallbackReasonApply),
		},
		{
			name:              "When fallback reason is nil it should omit lastDelta fallbackReason",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := NewMockClient(ctrl)

			bootcHost := container.BootcHost{}
			bootcHost.Status.Booted.Image.Image.Image = tc.bootedImage
			bootcHost.Status.Booted.Image.ImageDigest = tc.bootedImageDigest

			mockClient.EXPECT().Status(gomock.Any()).Return(&Status{BootcHost: bootcHost}, nil)

			m := &manager{
				client:         mockClient,
				caps:           tc.caps,
				fallbackReason: tc.fallbackReason,
			}

			ctx := context.Background()
			status := &v1beta1.DeviceStatus{}

			err := m.Status(ctx, status)
			require.NoError(err)
			require.Equal(tc.expectedImage, status.Os.Image)
			require.Equal(tc.expectedDigest, status.Os.ImageDigest)
			require.NotNil(status.Capabilities)
			require.NotNil(status.Capabilities.OsMode)
			require.Equal(tc.caps.OsMode, *status.Capabilities.OsMode)
			require.NotNil(status.SystemInfo.DeltaEligible)
			require.Equal(tc.expectedEligible, *status.SystemInfo.DeltaEligible)
			require.NotNil(status.SystemInfo.BootcVersion)
			require.Equal("bootc 1.15.0", *status.SystemInfo.BootcVersion)
			require.NotNil(status.SystemInfo.OciDeltaVersion)
			require.Equal("oci-delta 0.2.1", *status.SystemInfo.OciDeltaVersion)
			require.Equal(tc.expectedReason, osLastDeltaFallback(status))
		})
	}
}

func TestManagerStatusWhenClientFails(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockClient := NewMockClient(ctrl)
	clientErr := errors.New("status unavailable")
	mockClient.EXPECT().Status(gomock.Any()).Return(nil, clientErr)

	m := &manager{
		client: mockClient,
		caps:   Capabilities{OsMode: v1beta1.OsModePackage},
	}

	status := &v1beta1.DeviceStatus{}
	err := m.Status(context.Background(), status)
	require.ErrorIs(err, clientErr)
	require.Nil(status.Capabilities)
}

func TestCollectOCITargets(t *testing.T) {
	desiredSpec := func(image string, hint *string) *v1beta1.DeviceSpec {
		return &v1beta1.DeviceSpec{
			Os: &v1beta1.DeviceOsSpec{
				Image:      image,
				DeltaImage: hint,
			},
		}
	}

	tests := []struct {
		name           string
		caps           Capabilities
		desired        *v1beta1.DeviceSpec
		fallbackReason *string
		lastAttempted  string
		setup          func(*testing.T, *executer.MockExecuter, *MockClient, *dependency.MockPullConfigResolver)
		wantRefs       []string
		wantReason     *string
		wantEmpty      bool
		wantAttempted  string
	}{
		{
			name:    "When there is no OS spec it should return an empty collection",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: &v1beta1.DeviceSpec{},
			setup: func(_ *testing.T, _ *executer.MockExecuter, _ *MockClient, _ *dependency.MockPullConfigResolver) {
			},
			wantEmpty: true,
		},
		{
			name:    "When the desired image is already booted it should return an empty collection",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, nil),
			setup: func(_ *testing.T, _ *executer.MockExecuter, mockClient *MockClient, _ *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testSourceDigest), nil)
			},
			wantEmpty: true,
		},
		{
			name:    "When the desired image already exists it should return an empty collection",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, nil),
			setup: func(_ *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, _ *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, true)
			},
			wantEmpty: true,
		},
		{
			name:    "When not delta eligible it should emit a full-image target without Referrers",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: false},
			desired: desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
			},
			wantRefs:      []string{testDesiredImage},
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When hint is set it should pull and apply the hint without Referrers",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectDeltaSuccess(t, mockExec, mockClient, testHintedDelta)
			},
			wantEmpty:     true,
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When no hint and a matching referrer exists it should pull that digest",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, nil),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectListReferrers(mockExec, testDesiredImage, testReferrersJSON, "", 0)
				expectDeltaSuccess(t, mockExec, mockClient, testDeltaRef)
			},
			wantEmpty:     true,
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When no matching referrer exists it should full pull and leave the fallback reason unset",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, nil),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectListReferrers(mockExec, testDesiredImage, `{"schemaVersion":2,"manifests":[]}`, "", 0)
			},
			wantRefs:      []string{testDesiredImage},
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When Referrers lookup fails it should full pull and leave the fallback reason unset",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, nil),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectListReferrers(mockExec, testDesiredImage, "", "Error: connection refused", 1)
			},
			wantRefs:      []string{testDesiredImage},
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When Copy of the delta artifact fails it should set delta pull failed and emit a full-image target",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+testHintedDelta, gomock.Any(), "--src-no-creds").
					Return("", "Error: unauthorized", 1)
			},
			wantRefs:      []string{testDesiredImage},
			wantReason:    lo.ToPtr(fallbackReasonPull),
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When apply fails it should set delta apply failed and emit a full-image target",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectDeltaCopy(mockExec, testHintedDelta)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--ostree-repo", "/ostree/repo", gomock.Any(), gomock.Any()).
					Return("", "Error: diff_id mismatch", 1)
			},
			wantRefs:      []string{testDesiredImage},
			wantReason:    lo.ToPtr(fallbackReasonApply),
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When bootc switch from the reconstructed OCI layout fails it should treat it as apply failure and emit a full-image target",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectDeltaCopy(mockExec, testHintedDelta)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--ostree-repo", "/ostree/repo", gomock.Any(), gomock.Any()).
					Return("", "", 0)
				mockClient.EXPECT().SwitchOCI(gomock.Any(), gomock.Any()).
					Return(errors.New("bootc switch failed"))
			},
			wantRefs:      []string{testDesiredImage},
			wantReason:    lo.ToPtr(fallbackReasonApply),
			wantAttempted: testDesiredImage,
		},
		{
			name:    "When the registry switch after OCI stage fails it should treat it as apply failure and emit a full-image target",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectDeltaCopy(mockExec, testHintedDelta)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--ostree-repo", "/ostree/repo", gomock.Any(), gomock.Any()).
					Return("", "", 0)
				mockClient.EXPECT().SwitchOCI(gomock.Any(), gomock.Any()).Return(nil)
				mockClient.EXPECT().SwitchRegistry(gomock.Any(), testDesiredImage).
					Return(errors.New("bootc registry switch failed"))
			},
			wantRefs:      []string{testDesiredImage},
			wantReason:    lo.ToPtr(fallbackReasonApply),
			wantAttempted: testDesiredImage,
		},
		{
			name:           "When the desired image changes it should clear a previous fallback reason before a no-candidate path",
			caps:           Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired:        desiredSpec(testDesiredImage, nil),
			fallbackReason: lo.ToPtr(fallbackReasonApply),
			lastAttempted:  testBootedImage,
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectListReferrers(mockExec, testDesiredImage, `{"schemaVersion":2,"manifests":[]}`, "", 0)
			},
			wantRefs:      []string{testDesiredImage},
			wantAttempted: testDesiredImage,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			tt.setup(t, mockExec, mockClient, mockResolver)

			m := newTestManager(t, mockClient, mockExec, mockResolver, tt.caps)
			m.fallbackReason = tt.fallbackReason
			m.lastAttemptedImage = tt.lastAttempted

			collection, err := m.CollectOCITargets(context.Background(), nil, tt.desired)
			require.NoError(t, err)
			require.NotNil(t, collection)

			if tt.wantEmpty {
				require.Empty(t, collection.Targets)
			} else {
				got := collection.Targets[v1beta1.CurrentProcessUsername]
				require.Len(t, got, len(tt.wantRefs))
				for i, ref := range tt.wantRefs {
					require.Equal(t, dependency.OCITypePodmanImage, got[i].Type)
					require.Equal(t, ref, got[i].Reference)
				}
			}
			require.Equal(t, tt.wantReason, m.fallbackReason)
			if tt.wantAttempted != "" {
				require.Equal(t, tt.wantAttempted, m.lastAttemptedImage)
			}

			status := &v1beta1.DeviceStatus{}
			if tt.desired.Os != nil && !tt.wantEmpty || tt.wantReason != nil || tt.fallbackReason != nil {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).MaxTimes(1)
				if err := m.Status(context.Background(), status); err == nil {
					require.Equal(t, tt.wantReason, osLastDeltaFallback(status))
				}
			}
		})
	}
}

func newTestManager(t *testing.T, bootcClient Client, mockExec *executer.MockExecuter, resolver dependency.PullConfigResolver, caps Capabilities) *manager {
	t.Helper()
	logger := log.NewPrefixLogger("test")
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	backoff := poll.Config{BaseDelay: time.Millisecond, Factor: 1.5, MaxSteps: 1}
	return NewManager(
		logger,
		bootcClient,
		caps,
		rw,
		client.NewPodman(logger, mockExec, rw, backoff),
		resolver,
		client.NewOCIDelta(logger, mockExec),
		client.NewSkopeo(logger, mockExec, rw),
	).(*manager)
}

func bootcStatus(image, digest string) *Status {
	host := container.BootcHost{}
	host.Status.Booted.Image.Image.Image = image
	host.Status.Booted.Image.ImageDigest = digest
	return &Status{BootcHost: host}
}

func osLastDeltaFallback(status *v1beta1.DeviceStatus) *string {
	if status.Os.LastDelta == nil {
		return nil
	}
	return status.Os.LastDelta.FallbackReason
}

func expectPullConfig(t *testing.T, mockResolver *dependency.MockPullConfigResolver) {
	t.Helper()
	mockResolver.EXPECT().Options(gomock.Any()).DoAndReturn(func(specs ...dependency.PullConfigSpec) dependency.ClientOptsFn {
		require.NotEmpty(t, specs)
		require.Equal(t, []string{authPath}, specs[0].Paths)
		return func() []client.ClientOption { return nil }
	}).AnyTimes()
}

func expectImageExists(mockExec *executer.MockExecuter, image string, exists bool) {
	exitCode := 1
	if exists {
		exitCode = 0
	}
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).
		Return("", "", exitCode)
}

func expectListReferrers(mockExec *executer.MockExecuter, image, stdout, stderr string, exitCode int) {
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "list-referrers", "docker://"+image, "--no-creds").
		Return(stdout, stderr, exitCode)
}

func expectDeltaCopy(mockExec *executer.MockExecuter, candidate string) {
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(), "--src-no-creds").
		Return("", "", 0)
}

func expectDeltaSuccess(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, candidate string) {
	t.Helper()
	expectDeltaCopy(mockExec, candidate)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--ostree-repo", "/ostree/repo", gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, args ...string) (string, string, int) {
			require.True(t, strings.HasPrefix(args[len(args)-1], "oci:"))
			return "", "", 0
		})
	mockClient.EXPECT().SwitchOCI(gomock.Any(), gomock.Any()).Return(nil)
	mockClient.EXPECT().SwitchRegistry(gomock.Any(), testDesiredImage).Return(nil)
}
