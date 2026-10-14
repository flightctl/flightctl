package os

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/delta"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/opencontainers/go-digest"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestOSUnusableHintFallsBack(t *testing.T) {
	targetIndexDigest := digest.FromBytes([]byte(targetPlatformIndexRaw(testSourceDigest))).String()
	tests := []struct {
		name, hint, manifest, stderr, reason string
		checkTarget, targetInspectFailure    bool
		exitCode                             int
	}{
		{name: "When the hint omits source metadata it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1"}`, reason: "source"},
		{name: "When the hint is missing in the registry it should fall back", hint: testHintedDelta, stderr: "manifest unknown", exitCode: 1, reason: "inspect delta manifest"},
		{name: "When the hint is malformed it should fall back", hint: "not an image reference", reason: "invalid delta reference"},
		{name: "When the hinted artifact is not a delta it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.example.other"}`, reason: "not an OCI image delta"},
		{name: "When the hinted source digest is invalid it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","annotations":{"io.github.containers.delta.source":"sha256:invalid"}}`, reason: "source"},
		{name: "When artifact blobs are missing it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":"` + testSourceDigest + `","mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":"` + testSourceDigest + `"}}`, reason: "descriptors"},
		{name: "When the artifact declares a different target it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":"` + testDeltaDigest + `","mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":"` + testSourceDigest + `"},"config":{"digest":"` + testDeltaDigest + `","mediaType":"application/vnd.oci.image.config.v1+json","size":2},"layers":[{"digest":"` + testSourceDigest + `","mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":10}]}`, reason: "target", checkTarget: true},
		{name: "When the artifact targets the desired index instead of its platform manifest it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":"` + targetIndexDigest + `","mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":"` + testSourceDigest + `"},"config":{"digest":"` + testDeltaDigest + `","mediaType":"application/vnd.oci.image.config.v1+json","size":2},"layers":[{"digest":"` + testSourceDigest + `","mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":10}]}`, reason: "target digest does not match", checkTarget: true},
		{name: "When the desired target digest cannot be inspected it should reject the delta", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":"` + testDeltaDigest + `","mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":"` + testSourceDigest + `"},"config":{"digest":"` + testDeltaDigest + `","mediaType":"application/vnd.oci.image.config.v1+json","size":2},"layers":[{"digest":"` + testSourceDigest + `","mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":10}]}`, reason: "inspect delta target digest", checkTarget: true, targetInspectFailure: true},
		{name: "When an artifact declares a negative blob size it should fall back", hint: testHintedDelta, manifest: `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":"` + testSourceDigest + `","mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":"` + testSourceDigest + `"},"config":{"digest":"` + testDeltaDigest + `","mediaType":"application/vnd.oci.image.config.v1+json","size":2},"layers":[{"digest":"` + testSourceDigest + `","mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":-10}]}`, reason: "size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			if tt.checkTarget {
				if tt.targetInspectFailure {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+testDesiredImage, "--no-creds").Return("", "connection refused", 1)
				} else {
					expectPlatformIndex(mockExec, testDesiredImage, testSourceDigest)
				}
			}
			if tt.hint != "not an image reference" {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+tt.hint, "--no-creds").Return(tt.manifest, tt.stderr, tt.exitCode)
			}
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(tt.hint)}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Len(collection.Targets[v1beta1.CurrentProcessUsername], 1)
			require.Equal(testDesiredImage, collection.Targets[v1beta1.CurrentProcessUsername][0].Reference)
			expectFullOSStage(mockClient, testDesiredImage, testDeltaDigest)
			require.NoError(m.AfterUpdate(context.Background(), desired))
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			require.NotNil(status.Os.LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Os.LastDelta.Outcome)
			require.NotNil(status.Os.LastDelta.FallbackReason)
			require.Contains(*status.Os.LastDelta.FallbackReason, tt.reason)
		})
	}
}

func expectDeltaManifest(mockExec *executer.MockExecuter, reference, source string, size int64, targetImage ...string) {
	expectDeltaManifestSizes(mockExec, reference, source, size, 2, targetImage...)
}

func expectDeltaManifestSizes(mockExec *executer.MockExecuter, reference, source string, size, configSize int64, targetImage ...string) {
	if len(targetImage) != 0 && source != "" {
		image := targetImage[0]
		expectPlatformIndex(mockExec, image, testSourceDigest)
	}
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+reference, "--no-creds").Return(fmt.Sprintf(`{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":%q,"mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":%q},"config":{"digest":%q,"mediaType":"application/vnd.oci.image.config.v1+json","size":%d},"layers":[{"digest":%q,"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":%d}]}`, testSourceDigest, source, testSourceDigest, configSize, testDeltaDigest, size), "", 0)
}

func TestOSHintRequiresLocalSource(t *testing.T) {
	tests := []struct {
		name, stderr, reason string
		exitCode             int
	}{
		{name: "When the hinted source is missing locally it should recover with the full image", reason: "source image is unavailable"},
		{name: "When the local source probe fails it should recover with the full image", stderr: "repository unavailable", exitCode: 1, reason: "check delta source image"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			if tt.exitCode == 0 {
				expectDeltaManifest(mockExec, testHintedDelta, testDeltaDigest, 100, testDesiredImage)
			} else {
				expectDeltaManifest(mockExec, testHintedDelta, testDeltaDigest, 100)
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "refs", "--list", "--repo", "/ostree/repo", "ostree/container/image").Return("", tt.stderr, tt.exitCode)
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testHintedDelta)}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Len(collection.Targets[v1beta1.CurrentProcessUsername], 1)
			expectFullOSStage(mockClient, testDesiredImage, testDeltaDigest)
			require.NoError(m.AfterUpdate(context.Background(), desired))
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			require.NotNil(status.Os.LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Os.LastDelta.Outcome)
			require.Contains(*status.Os.LastDelta.FallbackReason, tt.reason)
		})
	}
}

func TestOSDiscoverySelectsSmallestLocalDelta(t *testing.T) {
	tests := []struct {
		name                                  string
		firstSize, secondSize                 int64
		firstManifestSize, secondManifestSize int64
		firstConfigSize                       int64
		firstUnavailable                      bool
		reverse                               bool
		want                                  string
	}{
		{name: "When a later candidate downloads fewer bytes it should select it", firstSize: 900, secondSize: 100, want: testHintedDeltaDigestRef},
		{name: "When payload sizes tie it should select the lexical reference", firstSize: 100, secondSize: 100, want: testDeltaRef},
		{name: "When equal sized referrers arrive in reverse order it should select the same reference", firstSize: 100, secondSize: 100, reverse: true, want: testDeltaRef},
		{name: "When a smaller manifest offsets a larger payload it should rank total bytes", firstSize: 100, secondSize: 200, firstManifestSize: 500, secondManifestSize: 50, want: testHintedDeltaDigestRef},
		{name: "When config bytes offset a smaller layer payload it should rank the complete artifact", firstSize: 100, secondSize: 200, firstConfigSize: 200, want: testHintedDeltaDigestRef},
		{name: "When the smallest delta has no local source it should select the usable candidate", firstSize: 10, secondSize: 100, firstUnavailable: true, want: testHintedDeltaDigestRef},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			firstManifestSize := tt.firstManifestSize
			if firstManifestSize == 0 {
				firstManifestSize = 1
			}
			secondManifestSize := tt.secondManifestSize
			if secondManifestSize == 0 {
				secondManifestSize = 1
			}
			refs := []client.OCIReferrer{{Digest: testDeltaDigest, ArtifactType: delta.ArtifactType, Size: firstManifestSize}, {Digest: testOtherDeltaDigest, ArtifactType: delta.ArtifactType, Size: secondManifestSize}}
			if tt.reverse {
				refs[0], refs[1] = refs[1], refs[0]
			}
			index, err := json.Marshal(client.OCIIndex{SchemaVersion: 2, Manifests: refs})
			require.NoError(err)
			expectListReferrers(mockExec, testDesiredImage, string(index), "", 0)
			firstSource := testSourceDigest
			if tt.firstUnavailable {
				firstSource = testDeltaDigest
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "refs", "--list", "--repo", "/ostree/repo", "ostree/container/image").Return("", "", 0)
			}
			configSize := int64(2)
			if tt.firstConfigSize != 0 {
				configSize = tt.firstConfigSize
			}
			expectDeltaManifestSizes(mockExec, testDeltaRef, firstSource, tt.firstSize, configSize)
			expectDeltaManifest(mockExec, testHintedDeltaDigestRef, testSourceDigest, tt.secondSize)
			expectDeltaSuccess(t, mockExec, mockClient, tt.want)
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Empty(collection.Targets)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			require.NotNil(status.Os.LastDelta)
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
		})
	}
}

const (
	testOtherDeltaDigest     = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testHintedDeltaDigestRef = testTargetRepo + "@" + testOtherDeltaDigest
)

func TestOSDiscoveryCollectsBootcSourcesOnce(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	mockClient := NewMockClient(ctrl)
	mockResolver := dependency.NewMockPullConfigResolver(ctrl)
	mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
	expectImageExists(mockExec, testDesiredImage, false)
	expectPullConfig(t, mockResolver)
	firstSource := "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	secondSource := "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	refs := []client.OCIReferrer{
		{Digest: testDeltaDigest, ArtifactType: delta.ArtifactType, Size: 10},
		{Digest: testOtherDeltaDigest, ArtifactType: delta.ArtifactType, Size: 20},
	}
	index, err := json.Marshal(client.OCIIndex{SchemaVersion: 2, Manifests: refs})
	require.NoError(err)
	expectListReferrers(mockExec, testDesiredImage, string(index), "", 0)
	expectDeltaManifestSizes(mockExec, testDeltaRef, firstSource, 100, 2)
	expectDeltaManifest(mockExec, testHintedDeltaDigestRef, secondSource, 200)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "refs", "--list", "--repo", "/ostree/repo", "ostree/container/image").
		Return("ostree/container/image/first ostree/container/image/second", "", 0).Times(1)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "show", "--repo", "/ostree/repo", "--print-metadata-key=ostree.manifest-digest", "ostree/container/image/first").
		Return("'"+firstSource+"'\n", "", 0).Times(1)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "show", "--repo", "/ostree/repo", "--print-metadata-key=ostree.manifest-digest", "ostree/container/image/second").
		Return("'"+secondSource+"'\n", "", 0).Times(1)
	expectDeltaSuccess(t, mockExec, mockClient, testDeltaRef)

	m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
	desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}
	collection, err := m.CollectOCITargets(context.Background(), nil, desired)
	require.NoError(err)
	require.Empty(collection.Targets)
	mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
	status := &v1beta1.DeviceStatus{}
	require.NoError(m.Status(context.Background(), status))
	require.NotNil(status.Os.LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
}

func TestOSDiscoveryPreservesConfirmedSourcesWhenCacheScanFails(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	mockExec := executer.NewMockExecuter(ctrl)
	mockClient := NewMockClient(ctrl)
	mockResolver := dependency.NewMockPullConfigResolver(ctrl)
	mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
	expectImageExists(mockExec, testDesiredImage, false)
	expectPullConfig(t, mockResolver)
	unavailableSource := "sha256:3333333333333333333333333333333333333333333333333333333333333333"
	refs := []client.OCIReferrer{
		{Digest: testDeltaDigest, ArtifactType: delta.ArtifactType, Size: 10},
		{Digest: testOtherDeltaDigest, ArtifactType: delta.ArtifactType, Size: 20},
	}
	index, err := json.Marshal(client.OCIIndex{SchemaVersion: 2, Manifests: refs})
	require.NoError(err)
	expectListReferrers(mockExec, testDesiredImage, string(index), "", 0)
	expectDeltaManifestSizes(mockExec, testDeltaRef, testSourceDigest, 100, 2)
	expectDeltaManifest(mockExec, testHintedDeltaDigestRef, unavailableSource, 10)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "refs", "--list", "--repo", "/ostree/repo", "ostree/container/image").
		Return("", "repository unavailable", 1)
	expectDeltaSuccess(t, mockExec, mockClient, testDeltaRef)

	m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
	desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}
	collection, err := m.CollectOCITargets(context.Background(), nil, desired)
	require.NoError(err)
	require.Empty(collection.Targets)
	mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
	status := &v1beta1.DeviceStatus{}
	require.NoError(m.Status(context.Background(), status))
	require.NotNil(status.Os.LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
}

func TestOSDeltaSourceUsesBootcStore(t *testing.T) {
	tests := []struct {
		name     string
		rollback bool
		discover bool
		cached   bool
	}{
		{name: "When the declared source is the rollback deployment it should apply the delta", rollback: true},
		{name: "When the declared source is another cached bootc image it should apply the delta", cached: true},
		{name: "When a discovered delta declares another cached source it should apply it", cached: true, discover: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			booted := bootcStatus(testBootedImage, testSourceDigest)
			if tt.rollback {
				booted.BootcHost.Status.Rollback.Image.ImageDigest = testDeltaDigest
			}
			mockClient.EXPECT().Status(gomock.Any()).Return(booted, nil).Times(1)
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			reference := testHintedDelta
			var hint *string = lo.ToPtr(reference)
			if tt.discover {
				reference = testDeltaRef
				hint = nil
				expectListReferrers(mockExec, testDesiredImage, testReferrersJSON, "", 0)
			}
			if tt.discover {
				expectDeltaManifest(mockExec, reference, testDeltaDigest, 100)
			} else {
				expectDeltaManifest(mockExec, reference, testDeltaDigest, 100, testDesiredImage)
			}
			if tt.cached {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "refs", "--list", "--repo", "/ostree/repo", "ostree/container/image").Return("ostree/container/image/cached\n", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "show", "--repo", "/ostree/repo", "--print-metadata-key=ostree.manifest-digest", "ostree/container/image/cached").Return("'"+testDeltaDigest+"'\n", "", 0)
			}
			expectDeltaSuccess(t, mockExec, mockClient, reference)
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: hint}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Empty(collection.Targets)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
		})
	}
}

func TestOSDiscoveryIgnoresUnusableCandidates(t *testing.T) {
	tests := []struct {
		name, artifactType, digest, source, targetDigest, manifestError string
		inspect, missingManifestSize, probe                             bool
	}{
		{name: "When the referrer has another artifact type it should use the full image", artifactType: "application/vnd.example.other", digest: testDeltaDigest},
		{name: "When the referrer digest is malformed it should use the full image", artifactType: delta.ArtifactType, digest: "sha256:invalid"},
		{name: "When the referrer descriptor size is missing it should use the full image", artifactType: delta.ArtifactType, digest: testDeltaDigest, missingManifestSize: true},
		{name: "When the artifact source metadata is missing it should use the full image", artifactType: delta.ArtifactType, digest: testDeltaDigest, inspect: true},
		{name: "When the artifact source is absent from bootc storage it should use the full image", artifactType: delta.ArtifactType, digest: testDeltaDigest, source: testOtherDeltaDigest, inspect: true, probe: true},
		{name: "When the referrer subject does not match the resolved platform digest it should use the full image", artifactType: delta.ArtifactType, digest: testDeltaDigest, source: testSourceDigest, targetDigest: testOtherDeltaDigest, inspect: true},
		{name: "When the artifact cannot be inspected it should use the full image", artifactType: delta.ArtifactType, digest: testDeltaDigest, inspect: true, manifestError: "manifest unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			// Referrer annotations claim a booted source; the inspected artifact is authoritative.
			referrers := client.OCIReferrer{Digest: tt.digest, ArtifactType: tt.artifactType, Size: 100, Annotations: map[string]string{ociDeltaSourceAnnotation: testSourceDigest}}
			if tt.missingManifestSize {
				referrers.Size = 0
			}
			index, err := json.Marshal(client.OCIIndex{Manifests: []client.OCIReferrer{referrers}})
			require.NoError(err)
			expectListReferrers(mockExec, testDesiredImage, string(index), "", 0)
			if tt.inspect {
				if tt.manifestError != "" {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+testDeltaRef, "--no-creds").Return("", tt.manifestError, 1)
				} else if tt.targetDigest != "" {
					manifest := fmt.Sprintf(`{"artifactType":%q,"subject":{"digest":%q,"mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":%q},"config":{"digest":%q,"mediaType":"application/vnd.oci.image.config.v1+json","size":2},"layers":[{"digest":%q,"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":100}]}`, delta.ArtifactType, tt.targetDigest, tt.source, testDeltaDigest, testDeltaDigest)
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+testDeltaRef, "--no-creds").Return(manifest, "", 0)
				} else {
					expectDeltaManifest(mockExec, testDeltaRef, tt.source, 100)
				}
			}
			if tt.probe {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "ostree", "refs", "--list", "--repo", "/ostree/repo", "ostree/container/image").Return("", "", 0)
			}
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Len(collection.Targets[v1beta1.CurrentProcessUsername], 1)
			expectFullOSStage(mockClient, testDesiredImage, testDeltaDigest)
			require.NoError(m.AfterUpdate(context.Background(), desired))
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, status.Os.LastDelta.Outcome)
			require.Nil(status.Os.LastDelta.FallbackReason)
		})
	}
}

func TestOSFullTargetRecoveryFailure(t *testing.T) {
	tests := []struct {
		name          string
		recoveryError error
	}{
		{name: "When the full target recovers from a failed delta it should complete staging"},
		{name: "When full target staging also fails it should fail reconciliation", recoveryError: errors.New("full image staging failed")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(1)
			expectImageExists(mockExec, testDesiredImage, false)
			expectPullConfig(t, mockResolver)
			expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, testDesiredImage)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", "docker://"+testHintedDelta, gomock.Any(), "--src-no-creds").Return("", "download failed", 1)
			mockClient.EXPECT().Switch(gomock.Any(), testDesiredImage).Return(tt.recoveryError)
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: testDesiredImage, DeltaImage: lo.ToPtr(testHintedDelta)}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Len(collection.Targets[v1beta1.CurrentProcessUsername], 1)
			if tt.recoveryError == nil {
				staged := bootcStatus(testBootedImage, testSourceDigest)
				staged.Status.Staged.Image.Image.Image = testDesiredImage
				staged.Status.Staged.Image.ImageDigest = testDeltaDigest
				mockClient.EXPECT().Status(gomock.Any()).Return(staged, nil)
			}
			require.ErrorIs(m.AfterUpdate(context.Background(), desired), tt.recoveryError)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testDesiredImage, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			if tt.recoveryError != nil {
				require.Nil(status.Os.LastDelta)
				return
			}
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Os.LastDelta.Outcome)
			require.Equal(fallbackReasonPull, *status.Os.LastDelta.FallbackReason)
		})
	}
}

func TestOSDeltaReportsAppliedAfterCanonicalStaging(t *testing.T) {
	tests := []struct{ name, image string }{
		{name: "When the desired OS image uses a tag it should report Applied only after canonical staging", image: testDesiredImage},
		{name: "When the desired OS image pins a digest it should report Applied only after canonical staging", image: testTargetImage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(testBootedImage, testSourceDigest), nil).Times(2)
			expectImageExists(mockExec, tt.image, false)
			expectPullConfig(t, mockResolver)
			expectDeltaManifest(mockExec, testHintedDelta, testSourceDigest, 100, tt.image)
			expectDeltaCopy(mockExec, testHintedDelta)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "apply", "--ostree-repo", "/ostree/repo", gomock.Any(), gomock.Any()).Return("", "", 0)
			mockClient.EXPECT().SwitchOCI(gomock.Any(), gomock.Any()).Return(nil)
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			mockClient.EXPECT().SwitchRegistry(gomock.Any(), tt.image).DoAndReturn(func(ctx context.Context, _ string) error {
				status := &v1beta1.DeviceStatus{}
				require.NoError(m.Status(ctx, status))
				require.Nil(status.Os.LastDelta)
				return nil
			})
			staged := bootcStatus(testBootedImage, testSourceDigest)
			staged.Status.Staged.Image.Image.Image = tt.image
			staged.Status.Staged.Image.ImageDigest = testDeltaDigest
			mockClient.EXPECT().Status(gomock.Any()).Return(staged, nil).Times(2)
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: tt.image, DeltaImage: lo.ToPtr(testHintedDelta)}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Empty(collection.Targets)
			require.NoError(m.AfterUpdate(context.Background(), desired))
			mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(tt.image, testDeltaDigest), nil)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeApplied, status.Os.LastDelta.Outcome)
		})
	}
}

func TestOSMultiPlatformTargetDigest(t *testing.T) {
	tests := []struct {
		name, image string
		hint        bool
	}{
		{name: "When a booted platform manifest belongs to the requested index it should report NotRequired", image: testDesiredImage},
		{name: "When the desired image pins an index and is booted it should reuse it", image: testTargetImage},
		{name: "When a delta targets the selected platform manifest it should apply it", image: testDesiredImage, hint: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			mockClient := NewMockClient(ctrl)
			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			booted := bootcStatus(tt.image, testSourceDigest)
			if tt.hint {
				booted = bootcStatus(testBootedImage, testSourceDigest)
			}
			mockClient.EXPECT().Status(gomock.Any()).Return(booted, nil).Times(1)
			if tt.hint {
				expectPullConfig(t, mockResolver)
				expectImageExists(mockExec, tt.image, false)
				raw := fmt.Sprintf(`{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":%q,"mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":%q},"config":{"digest":%q,"mediaType":"application/vnd.oci.image.config.v1+json","size":2},"layers":[{"digest":%q,"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":10}]}`, testSourceDigest, testSourceDigest, testDeltaDigest, testDeltaDigest)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+testHintedDelta, "--no-creds").Return(raw, "", 0)
				expectDeltaSuccess(t, mockExec, mockClient, testHintedDelta)
			}
			if tt.hint {
				expectPlatformIndex(mockExec, tt.image, testSourceDigest)
			}
			m := newTestManager(t, mockClient, mockExec, mockResolver, Capabilities{DeltaEligible: true, BootcVersion: "bootc 1.15.0"})
			var hint *string
			if tt.hint {
				hint = lo.ToPtr(testHintedDelta)
			}
			desired := &v1beta1.DeviceSpec{Os: &v1beta1.DeviceOsSpec{Image: tt.image, DeltaImage: hint}}
			collection, err := m.CollectOCITargets(context.Background(), nil, desired)
			require.NoError(err)
			require.Empty(collection.Targets)
			if tt.hint {
				mockClient.EXPECT().Status(gomock.Any()).Return(bootcStatus(tt.image, testDeltaDigest), nil)
			} else {
				mockClient.EXPECT().Status(gomock.Any()).Return(booted, nil)
			}
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(context.Background(), status))
			outcome := v1beta1.DeviceDeltaApplyOutcomeNotRequired
			if tt.hint {
				outcome = v1beta1.DeviceDeltaApplyOutcomeApplied
			}
			require.Equal(outcome, status.Os.LastDelta.Outcome)
		})
	}
}

func expectPlatformIndex(mockExec *executer.MockExecuter, image, platformDigest string) {
	raw := targetPlatformIndexRaw(platformDigest)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image, "--no-creds").Return(raw, "", 0)
}

func targetPlatformIndexRaw(platformDigest string) string {
	return fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"size":100,"platform":{"os":%q,"architecture":%q}}]}`, platformDigest, runtime.GOOS, runtime.GOARCH)
}
