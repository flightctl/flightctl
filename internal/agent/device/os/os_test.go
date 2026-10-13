package os

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/container"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
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
	testCases := []struct {
		name              string
		caps              Capabilities
		fallbackReason    *string
		deltaOutcome      *v1beta1.DeviceDeltaApplyOutcomeType
		bootedImage       string
		bootedImageDigest string
		expectedImage     string
		expectedDigest    string
		expectedEligible  bool
		expectedReason    *string
		expectedOutcome   *v1beta1.DeviceDeltaApplyOutcomeType
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
			deltaOutcome:      lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeFallback),
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
			expectedReason:    lo.ToPtr(fallbackReasonApply),
			expectedOutcome:   lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeFallback),
		},
		{
			name:              "When delta apply succeeds it should report the applied outcome",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			deltaOutcome:      lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeApplied),
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
			expectedOutcome:   lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeApplied),
		},
		{
			name:              "When the delta is not used it should report NotUsed",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			deltaOutcome:      lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeNotUsed),
			bootedImage:       "quay.io/centos-bootc/centos-bootc:stream9",
			bootedImageDigest: "sha256:a0b1c2d3",
			expectedImage:     "quay.io/centos-bootc/centos-bootc:stream9",
			expectedDigest:    "sha256:a0b1c2d3",
			expectedEligible:  true,
			expectedOutcome:   lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeNotUsed),
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
			assert := assert.New(t)
			require := require.New(t)
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
				deltaOutcome:   tc.deltaOutcome,
			}

			ctx := context.Background()
			status := &v1beta1.DeviceStatus{}

			err := m.Status(ctx, status)
			require.NoError(err)
			assert.Equal(tc.expectedImage, status.Os.Image)
			assert.Equal(tc.expectedDigest, status.Os.ImageDigest)
			require.NotNil(status.Capabilities)
			require.NotNil(status.Capabilities.OsMode)
			assert.Equal(tc.caps.OsMode, *status.Capabilities.OsMode)
			require.NotNil(status.SystemInfo.DeltaEligible)
			assert.Equal(tc.expectedEligible, *status.SystemInfo.DeltaEligible)
			require.NotNil(status.SystemInfo.BootcVersion)
			assert.Equal("bootc 1.15.0", *status.SystemInfo.BootcVersion)
			require.NotNil(status.SystemInfo.OciDeltaVersion)
			require.Equal("oci-delta 0.2.1", *status.SystemInfo.OciDeltaVersion)
			require.NotNil(status.SystemInfo.OsMode)
			assert.Equal(tc.caps.OsMode, *status.SystemInfo.OsMode)
			require.Equal(tc.expectedReason, osLastDeltaFallback(status))
			require.Equal(tc.expectedOutcome, osLastDeltaOutcome(status))
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

func TestOSDeltaStatusPersistsAcrossRestartAndClearsForNewTarget(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockClient := NewMockClient(ctrl)
	mockClient.EXPECT().Status(gomock.Any()).
		Return(bootcStatus(testDesiredImage, testSourceDigest), nil).Times(3)

	firstManager := newTestManager(t, mockClient, executer.NewMockExecuter(ctrl), nil, Capabilities{})
	firstManager.deltaStatusStore = deltastatus.New(firstManager.readWriter, "/var/lib/flightctl", firstManager.log)

	target := &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testDeltaRef)}
	firstManager.startImageAttempt(target)
	firstManager.recordDeltaResult(v1beta1.DeviceDeltaApplyOutcomeApplied, "", false)

	// A new store and manager simulate restarting the agent process.
	restartedStore := deltastatus.New(firstManager.readWriter, "/var/lib/flightctl", firstManager.log)
	restartedManager := &manager{
		client:           mockClient,
		caps:             Capabilities{OsMode: v1beta1.OsModeImage},
		log:              firstManager.log,
		deltaStatusStore: restartedStore,
	}
	ctx := context.Background()
	require.NoError(t, restartedManager.BeforeUpdate(ctx, nil, &v1beta1.DeviceSpec{Os: target}))

	status := &v1beta1.DeviceStatus{}
	require.NoError(t, restartedManager.Status(ctx, status))
	require.NotNil(t, status.Os.LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)

	// A hint refresh for the same desired image does not make its persisted
	// application outcome stale.
	refreshedHint := &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr("quay.io/acme/os-delta:v3")}
	require.NoError(t, restartedManager.BeforeUpdate(ctx, &v1beta1.DeviceSpec{Os: target}, &v1beta1.DeviceSpec{Os: refreshedHint}))
	status = &v1beta1.DeviceStatus{}
	require.NoError(t, restartedManager.Status(ctx, status))
	require.NotNil(t, status.Os.LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)

	newTarget := &v1beta1.DeviceOsSpec{Image: "quay.io/acme/os:v3", DeltaImage: lo.ToPtr("quay.io/acme/os-delta:v3")}
	require.NoError(t, restartedManager.BeforeUpdate(ctx, &v1beta1.DeviceSpec{Os: refreshedHint}, &v1beta1.DeviceSpec{Os: newTarget}))
	status = &v1beta1.DeviceStatus{}
	require.NoError(t, restartedManager.Status(ctx, status))
	require.Nil(t, status.Os.LastDelta)
}

func TestSystemInfoOsMode(t *testing.T) {
	testCases := []struct {
		name     string
		mode     v1beta1.OsModeType
		expected v1beta1.OsModeType
		wantOK   bool
	}{
		{
			name:     "When mode is image it should report image",
			mode:     v1beta1.OsModeImage,
			expected: v1beta1.OsModeImage,
			wantOK:   true,
		},
		{
			name:     "When mode is package it should report package",
			mode:     v1beta1.OsModePackage,
			expected: v1beta1.OsModePackage,
			wantOK:   true,
		},
		{
			name:   "When mode is unrecognized it should not be reported",
			mode:   v1beta1.OsModeType("bogus"),
			wantOK: false,
		},
		{
			name:   "When mode is empty it should not be reported",
			mode:   v1beta1.OsModeType(""),
			wantOK: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			got, ok := systemInfoOsMode(tc.mode)
			assert.Equal(tc.wantOK, ok)
			assert.Equal(tc.expected, got)
		})
	}
}

func TestApplyDeltaSystemInfoOsMode(t *testing.T) {
	testCases := []struct {
		name     string
		mode     v1beta1.OsModeType
		wantSet  bool
		wantMode v1beta1.OsModeType
	}{
		{
			name:     "When mode is image it should set OsMode to image",
			mode:     v1beta1.OsModeImage,
			wantSet:  true,
			wantMode: v1beta1.OsModeImage,
		},
		{
			name:     "When mode is package it should set OsMode to package",
			mode:     v1beta1.OsModePackage,
			wantSet:  true,
			wantMode: v1beta1.OsModePackage,
		},
		{
			name:    "When mode is empty it should omit OsMode",
			mode:    v1beta1.OsModeType(""),
			wantSet: false,
		},
		{
			name:    "When mode is unrecognized it should omit OsMode",
			mode:    v1beta1.OsModeType("bogus"),
			wantSet: false,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			info := &v1beta1.DeviceSystemInfo{}
			ApplyDeltaSystemInfo(info, Capabilities{OsMode: tc.mode})
			if tc.wantSet {
				require.NotNil(info.OsMode)
				assert.Equal(tc.wantMode, *info.OsMode)
			} else {
				assert.Nil(info.OsMode)
			}
		})
	}
}

func TestApplyDeltaSystemInfoClearsStaleOsMode(t *testing.T) {
	testCases := []struct {
		name      string
		staleMode v1beta1.OsModeType
	}{
		{
			name:      "When a recognized mode is followed by an empty mode it should clear OsMode",
			staleMode: v1beta1.OsModeType(""),
		},
		{
			name:      "When a recognized mode is followed by an unrecognized mode it should clear OsMode",
			staleMode: v1beta1.OsModeType("bogus"),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)

			info := &v1beta1.DeviceSystemInfo{}

			// A recognized mode sets the field.
			ApplyDeltaSystemInfo(info, Capabilities{OsMode: v1beta1.OsModeImage})
			require.NotNil(info.OsMode)
			assert.Equal(v1beta1.OsModeImage, *info.OsMode)

			// Re-applying with an unrecognized/empty mode must clear the stale value.
			ApplyDeltaSystemInfo(info, Capabilities{OsMode: tc.staleMode})
			assert.Nil(info.OsMode)
		})
	}
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
		stagedDelta    string
		setup          func(*testing.T, *executer.MockExecuter, *MockClient, *dependency.MockPullConfigResolver)
		wantRefs       []string
		wantReason     *string
		wantEmpty      bool
		wantAttempted  string
		wantStaged     *string
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
			name:        "When a delta is already staged for the desired image it should skip collection",
			caps:        Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired:     desiredSpec(testDesiredImage, nil),
			stagedDelta: testDesiredImage,
			setup: func(_ *testing.T, _ *executer.MockExecuter, _ *MockClient, _ *dependency.MockPullConfigResolver) {
			},
			wantEmpty:  true,
			wantStaged: lo.ToPtr(testDesiredImage),
		},
		{
			name:        "When a delta is staged for a different image it should collect the new desired image",
			caps:        Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: false},
			desired:     desiredSpec(testDesiredImage, nil),
			stagedDelta: testBootedImage,
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
			},
			wantRefs:      []string{testDesiredImage},
			wantAttempted: testDesiredImage,
			wantStaged:    lo.ToPtr(""),
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
			wantStaged:    lo.ToPtr(testDesiredImage),
		},
		{
			name:           "When a later delta attempt for the same image succeeds it should clear a previous fallback reason",
			caps:           Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired:        desiredSpec(testDesiredImage, lo.ToPtr(testHintedDelta)),
			fallbackReason: lo.ToPtr(fallbackReasonPull),
			lastAttempted:  testDesiredImage,
			setup: func(t *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, mockResolver *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, false)
				expectPullConfig(t, mockResolver)
				expectDeltaSuccess(t, mockExec, mockClient, testHintedDelta)
			},
			wantEmpty:     true,
			wantAttempted: testDesiredImage,
			wantStaged:    lo.ToPtr(testDesiredImage),
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
			wantStaged:    lo.ToPtr(testDesiredImage),
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
			m.stagedDeltaImage = tt.stagedDelta

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
			if tt.wantStaged != nil {
				require.Equal(t, *tt.wantStaged, m.stagedDeltaImage)
			}

			status := &v1beta1.DeviceStatus{}
			if tt.desired.Os == nil {
				return
			}
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
			require.NoError(t, m.Status(context.Background(), status))
			require.Equal(t, tt.wantReason, osLastDeltaFallback(status))
		})
	}
}

func TestAfterUpdateAndReboot(t *testing.T) {
	desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}

	tests := []struct {
		name        string
		desired     *v1beta1.DeviceSpec
		stagedDelta string
		setup       func(*MockClient)
		runAfter    bool
		runReboot   bool
	}{
		{
			name:        "When a delta is staged for the desired image AfterUpdate should skip Switch",
			desired:     desired,
			stagedDelta: testDesiredImage,
			setup:       func(_ *MockClient) {},
			runAfter:    true,
		},
		{
			name:    "When no delta is staged AfterUpdate should Switch to the desired image",
			desired: desired,
			setup: func(mockClient *MockClient) {
				mockClient.EXPECT().Switch(gomock.Any(), testDesiredImage).Return(nil)
			},
			runAfter: true,
		},
		{
			name:        "When a delta is staged for a different image AfterUpdate should Switch to the desired image",
			desired:     desired,
			stagedDelta: testBootedImage,
			setup: func(mockClient *MockClient) {
				mockClient.EXPECT().Switch(gomock.Any(), testDesiredImage).Return(nil)
			},
			runAfter: true,
		},
		{
			name:        "When a delta is staged for the desired image Reboot should use RebootStaged",
			desired:     desired,
			stagedDelta: testDesiredImage,
			setup: func(mockClient *MockClient) {
				mockClient.EXPECT().RebootStaged(gomock.Any()).Return(nil)
			},
			runReboot: true,
		},
		{
			name:    "When no delta is staged Reboot should Apply",
			desired: desired,
			setup: func(mockClient *MockClient) {
				mockClient.EXPECT().Apply(gomock.Any()).Return(nil)
			},
			runReboot: true,
		},
		{
			name:        "When a delta is staged for a different image Reboot should Apply",
			desired:     desired,
			stagedDelta: testBootedImage,
			setup: func(mockClient *MockClient) {
				mockClient.EXPECT().Apply(gomock.Any()).Return(nil)
			},
			runReboot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockClient := NewMockClient(ctrl)
			tt.setup(mockClient)
			m := &manager{client: mockClient, stagedDeltaImage: tt.stagedDelta}

			if tt.runAfter {
				require.NoError(t, m.AfterUpdate(context.Background(), tt.desired))
			}
			if tt.runReboot {
				require.NoError(t, m.Reboot(context.Background(), tt.desired))
			}
		})
	}
}

func newTestManager(t *testing.T, bootcClient Client, mockExec *executer.MockExecuter, resolver dependency.PullConfigResolver, caps Capabilities) *manager {
	t.Helper()
	logger := log.NewPrefixLogger("test")
	tmpDir := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(tmpDir)),
		fileio.NewWriter(fileio.WithWriterRootDir(tmpDir)),
	)
	backoff := poll.Config{BaseDelay: time.Millisecond, Factor: 1.5, MaxSteps: 1}
	return NewManager(
		logger,
		bootcClient,
		caps,
		rw,
		client.NewPodman(logger, mockExec, rw, backoff),
		resolver,
		client.NewOCIDelta(logger, mockExec, time.Minute),
		client.NewSkopeo(logger, mockExec, rw),
		time.Minute,
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

func osLastDeltaOutcome(status *v1beta1.DeviceStatus) *v1beta1.DeviceDeltaApplyOutcomeType {
	if status.Os.LastDelta == nil {
		return nil
	}
	return &status.Os.LastDelta.Outcome
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
