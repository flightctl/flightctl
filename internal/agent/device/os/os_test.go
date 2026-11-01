package os

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
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
	testTargetImage  = "quay.io/acme/os@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testTargetRepo   = "quay.io/acme/os"
	testSourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testDeltaDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testHintedDelta  = "quay.io/acme-deltas/os@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"

	testDesiredImage  = "quay.io/acme/os:v2"
	testBootedImage   = "quay.io/acme/os:v1"
	testDeltaRef      = "quay.io/acme/os@" + testDeltaDigest
	testReferrersJSON = `{
		"schemaVersion": 2,
		"manifests": [
			{
				"digest": "` + testDeltaDigest + `",
				"size": 100,
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
			name:              "When bootc observes a platform digest it should report it separately from the index reference and delta outcome",
			caps:              Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0", OCIDeltaVersion: "oci-delta 0.2.1"},
			bootedImage:       "quay.io/acme/os@sha256:index",
			bootedImageDigest: "sha256:platform",
			deltaOutcome:      lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeApplied),
			expectedImage:     "quay.io/acme/os@sha256:index",
			expectedDigest:    "sha256:platform",
			expectedEligible:  true,
			expectedOutcome:   lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeApplied),
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

			if tc.deltaOutcome != nil {
				root := t.TempDir()
				rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
				m.deltaStatusStore = deltastatus.New(rw, "/state", log.NewPrefixLogger("test"))
				require.NoError(m.deltaStatusStore.SetOSTarget("attempt"))
				require.NoError(m.deltaStatusStore.RecordOSImageResult("attempt", tc.bootedImage, tc.bootedImageDigest, v1beta1.DeviceDeltaApplyStatus{Outcome: *tc.deltaOutcome, FallbackReason: tc.fallbackReason}))
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
			if status.Os.LastDelta != nil {
				data, err := json.Marshal(status.Os.LastDelta)
				require.NoError(err)
				var delta map[string]any
				require.NoError(json.Unmarshal(data, &delta))
				expected := map[string]any{"outcome": string(*tc.expectedOutcome)}
				if tc.expectedReason != nil {
					expected["fallbackReason"] = *tc.expectedReason
				}
				require.Equal(expected, delta)
			}
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

func TestWhenAnotherOSIsStagedItShouldReportTheBootedOutcomeAcrossRestart(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	mockResolver := dependency.NewMockPullConfigResolver(ctrl)
	mockClient := NewMockClient(ctrl)
	observed := bootcStatus(testBootedImage, testSourceDigest)
	observed.Status.Staged.Image.Image.Image = testDesiredImage
	observed.Status.Staged.Image.ImageDigest = testDeltaDigest
	m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
	m.deltaStatusStore = deltastatus.New(m.readWriter, "/var/lib/flightctl", m.log)
	require.NoError(m.deltaStatusStore.SetOSTarget("old-attempt"))
	require.NoError(m.deltaStatusStore.RecordOSImageResult("old-attempt", testBootedImage, testSourceDigest, v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback}))
	target := &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testDeltaRef)}
	expectImageExists(mockExec, testDesiredImage, false)
	expectPullConfig(t, mockResolver)
	expectDeltaManifest(mockExec, testDeltaRef, testSourceDigest, 100, testDesiredImage)
	mockClient.EXPECT().Status(gomock.Any()).Return(observed, nil)
	expectDeltaSuccess(t, mockExec, mockClient, testDeltaRef)
	mockClient.EXPECT().Status(gomock.Any()).DoAndReturn(func(context.Context) (*Status, error) { return observed, nil }).AnyTimes()
	_, err := m.CollectOCITargets(context.Background(), nil, &v1beta1.DeviceSpec{Os: target})
	require.NoError(err)
	status := &v1beta1.DeviceStatus{}
	require.NoError(m.Status(context.Background(), status))
	require.Equal(testSourceDigest, status.Os.ImageDigest)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Os.LastDelta.Outcome)

	restarted := &manager{client: mockClient, log: m.log, deltaStatusStore: deltastatus.New(m.readWriter, "/var/lib/flightctl", m.log)}
	require.NoError(restarted.BeforeUpdate(context.Background(), nil, &v1beta1.DeviceSpec{Os: target}))
	require.NoError(restarted.Status(context.Background(), status))
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Os.LastDelta.Outcome)
	observed.Status.Booted = observed.Status.Staged
	collection, err := restarted.CollectOCITargets(context.Background(), nil, &v1beta1.DeviceSpec{Os: target})
	require.NoError(err)
	require.Empty(collection.Targets)
	require.NoError(restarted.Status(context.Background(), status))
	require.Equal(testDeltaDigest, status.Os.ImageDigest)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
	refreshedHint := &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr("quay.io/acme/os-delta:v3")}
	require.NoError(restarted.BeforeUpdate(context.Background(), nil, &v1beta1.DeviceSpec{Os: refreshedHint}))
	require.NoError(restarted.Status(context.Background(), status))
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
	require.NoError(restarted.BeforeUpdate(context.Background(), nil, &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: "quay.io/acme/os:v3"}}))
	require.NoError(restarted.Status(context.Background(), status))
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
	observed.Status.Booted.Image.ImageDigest = "sha256:unknown"
	require.NoError(restarted.Status(context.Background(), status))
	require.Nil(status.Os.LastDelta)
}

func TestStagedOSVerificationRecoversBeforeReboot(t *testing.T) {
	tests := []struct {
		name       string
		setupMocks func(*MockClient, *Status)
	}{
		{
			name: "When staging verification fails transiently it should retain Applied after reboot and restart",
			setupMocks: func(mockClient *MockClient, observed *Status) {
				gomock.InOrder(
					mockClient.EXPECT().Status(gomock.Any()).Return(observed, nil),
					mockClient.EXPECT().Status(gomock.Any()).Return(nil, errors.New("bootc status temporarily unavailable")),
					mockClient.EXPECT().Status(gomock.Any()).DoAndReturn(func(context.Context) (*Status, error) { return observed, nil }).AnyTimes(),
				)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient := NewMockClient(ctrl)
			observed := bootcStatus(testBootedImage, testSourceDigest)
			observed.Status.Staged.Image.Image.Image = testDesiredImage
			observed.Status.Staged.Image.ImageDigest = testDeltaDigest
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			m.deltaStatusStore = deltastatus.New(m.readWriter, "/var/lib/flightctl", m.log)
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testDeltaRef)}}
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			expectDeltaManifest(mockExec, testDeltaRef, testSourceDigest, 100, testDesiredImage)
			expectDeltaCopy(mockExec, testDeltaRef)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--ostree-repo", "/ostree/repo", gomock.Any(), gomock.Any()).Return("", "", 0)
			mockClient.EXPECT().SwitchOCI(gomock.Any(), gomock.Any()).Return(nil)
			mockClient.EXPECT().SwitchRegistry(gomock.Any(), testDesiredImage).Return(nil)
			tt.setupMocks(mockClient, observed)
			mockClient.EXPECT().RebootStaged(gomock.Any()).DoAndReturn(func(context.Context) error {
				observed.Status.Booted = observed.Status.Staged
				return nil
			})

			_, err := m.CollectOCITargets(t.Context(), nil, desired)
			require.NoError(err)
			require.NoError(m.AfterUpdate(t.Context(), desired))
			require.NoError(m.Reboot(t.Context(), desired))

			restarted := NewManager(m.log, mockClient, Capabilities{}, m.readWriter, nil, nil, nil, nil, 0,
				WithDeltaStatusStore(deltastatus.New(m.readWriter, "/var/lib/flightctl", m.log)))
			_, err = restarted.CollectOCITargets(t.Context(), nil, desired)
			require.NoError(err)
			status := &v1beta1.DeviceStatus{}
			require.NoError(restarted.Status(t.Context(), status))
			require.Equal(testDesiredImage, status.Os.Image)
			require.Equal(testDeltaDigest, status.Os.ImageDigest)
			require.Equal(&v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied}, status.Os.LastDelta)
		})
	}
}

func TestBootedOSOutcomeAfterRestart(t *testing.T) {
	tests := []struct {
		name         string
		previous     v1beta1.DeviceDeltaApplyStatus
		verified     bool
		bootedDigest string
		expected     *v1beta1.DeviceDeltaApplyStatus
	}{
		{
			name:     "When the same tag boots different content after restart it should not inherit Applied",
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			verified: true, bootedDigest: testDeltaDigest,
			expected: &v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		},
		{
			name:     "When the same tag boots different content after restart it should not inherit Fallback",
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
			verified: true, bootedDigest: testDeltaDigest,
			expected: &v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		},
		{
			name:     "When the verified image and digest match after restart it should retain Applied",
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			verified: true, bootedDigest: testSourceDigest,
			expected: &v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
		},
		{
			name:     "When the verified image and digest match after restart it should retain Fallback and its reason",
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
			verified: true, bootedDigest: testSourceDigest,
			expected: &v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr(fallbackReasonApply)},
		},
		{
			name:         "When the saved attempt has no verified identity it should not inherit Applied after restart",
			previous:     v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			bootedDigest: testSourceDigest,
			expected:     &v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		},
		{
			name:     "When the booted digest is unknown after restart it should omit the saved outcome",
			previous: v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			verified: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			bootc := NewMockClient(ctrl)
			observed := bootcStatus(testDesiredImage, tt.bootedDigest)
			bootc.EXPECT().Status(gomock.Any()).DoAndReturn(func(context.Context) (*Status, error) { return observed, nil }).AnyTimes()
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := deltastatus.New(rw, "/state", logger)
			targetKey := deltastatus.Fingerprint(testDesiredImage)
			require.NoError(store.SetOSTarget(targetKey))
			require.NoError(store.RecordOSResult(targetKey, tt.previous))
			if tt.verified {
				require.NoError(store.RecordOSImageResult(targetKey, testDesiredImage, testSourceDigest, tt.previous))
			}

			for range 2 {
				restarted := NewManager(logger, bootc, Capabilities{}, rw, nil, nil, nil, nil, 0,
					WithDeltaStatusStore(deltastatus.New(rw, "/state", logger)))
				collection, err := restarted.CollectOCITargets(t.Context(), nil, &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}})
				require.NoError(err)
				require.Empty(collection.Targets)
				status := &v1beta1.DeviceStatus{}
				require.NoError(restarted.Status(t.Context(), status))
				require.Equal(tt.expected, status.Os.LastDelta)
				if tt.verified {
					observed.Status.Booted.Image.ImageDigest = testSourceDigest
					require.NoError(restarted.Status(t.Context(), status))
					require.Equal(&tt.previous, status.Os.LastDelta)
					observed.Status.Booted.Image.ImageDigest = tt.bootedDigest
				}
			}
		})
	}
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
		wantOutcome    *v1beta1.DeviceDeltaApplyOutcomeType
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
			wantEmpty:   true,
			wantOutcome: lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeNotRequired),
		},
		{
			name:    "When the desired image already exists it should return an empty collection",
			caps:    Capabilities{OsMode: v1beta1.OsModeImage, DeltaEligible: true, BootcVersion: "bootc 1.15.0"},
			desired: desiredSpec(testDesiredImage, nil),
			setup: func(_ *testing.T, mockExec *executer.MockExecuter, mockClient *MockClient, _ *dependency.MockPullConfigResolver) {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
				expectImageExists(mockExec, testDesiredImage, true)
			},
			wantEmpty:   true,
			wantOutcome: lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeNotRequired),
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
			wantOutcome:   lo.ToPtr(v1beta1.DeviceDeltaApplyOutcomeFallback),
			wantReason:    lo.ToPtr("OS delta application is unavailable"),
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
				expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
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
				expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
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
				expectDeltaManifest(mockExec, testDeltaRef, testSourceDigest, 100)
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
				expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
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
				expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
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
				expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
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
				expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
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
			require := require.New(t)
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
			require.NoError(err)
			require.NotNil(collection)

			if tt.wantEmpty {
				require.Empty(collection.Targets)
			} else {
				got := collection.Targets[v1beta1.CurrentProcessUsername]
				require.Len(got, len(tt.wantRefs))
				for i, ref := range tt.wantRefs {
					require.Equal(dependency.OCITypePodmanImage, got[i].Type)
					require.Equal(v1beta1.PullIfNotPresent, got[i].PullPolicy)
					require.Equal(ref, got[i].Reference)
				}
			}
			require.Equal(tt.wantReason, m.fallbackReason)
			if tt.wantAttempted != "" {
				require.Equal(tt.wantAttempted, m.lastAttemptedImage)
			}
			if tt.wantStaged != nil {
				require.Equal(*tt.wantStaged, m.stagedDeltaImage)
			}

			status := &v1beta1.DeviceStatus{}
			if tt.desired.Os == nil {
				return
			}
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil)
			require.NoError(m.Status(context.Background(), status))
			// Collection outcomes are provisional until the matching image is booted.
			require.Nil(status.Os.LastDelta)
			if m.deltaOutcome != nil {
				if m.stagedDeltaImage != tt.desired.Os.Image {
					expectFullOSStage(mockClient, tt.desired.Os.Image, testDeltaDigest)
					require.NoError(m.AfterUpdate(context.Background(), tt.desired))
				}
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(tt.desired.Os.Image, testDeltaDigest), nil)
				require.NoError(m.Status(context.Background(), status))
				require.Equal(tt.wantReason, osLastDeltaFallback(status))
				if tt.wantOutcome != nil {
					require.Equal(tt.wantOutcome, osLastDeltaOutcome(status))
				}
			}

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
	expectPlatformIndex(mockExec, image, testSourceDigest)
	referrerImage := delta.Repository(image) + "@" + testSourceDigest
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "flightctl-agent", "registry-referrers", "--image", referrerImage, "--timeout", "2m0s", "--no-creds").
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
	staged := bootcStatus(testBootedImage, testSourceDigest)
	staged.Status.Staged.Image.Image.Image = testDesiredImage
	staged.Status.Staged.Image.ImageDigest = testDeltaDigest
	mockClient.EXPECT().Status(gomock.Any()).Return(staged, nil)
}

func expectFullOSStage(mockClient *MockClient, image, digest string) {
	mockClient.EXPECT().Switch(gomock.Any(), image).Return(nil)
	staged := bootcStatus(testBootedImage, testSourceDigest)
	staged.Status.Staged.Image.Image.Image = image
	staged.Status.Staged.Image.ImageDigest = digest
	mockClient.EXPECT().Status(gomock.Any()).Return(staged, nil)
}
