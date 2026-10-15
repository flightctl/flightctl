package dependency

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/opencontainers/go-digest"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApplicationDeltaPrefetch(t *testing.T) {
	const image = "quay.io/acme/app@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const candidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const refreshImage = "quay.io/acme/app@" + podmanDeltaTargetDigest

	tests := []struct {
		name               string
		delta              *OCIDeltaTarget
		setup              func(*executer.MockExecuter)
		retryAfterFallback bool
		wantFallback       bool
		wantOutcome        v1beta1.DeviceDeltaApplyOutcomeType
		fallbackContains   string
		disableDeltaClient bool
		deltaFactory       client.OCIDeltaFactory
	}{
		{
			name:               "When no delta client exists it should skip discovery and full-pull with NotUsed",
			delta:              &OCIDeltaTarget{SourceDigest: "sha256:source", Application: "app"},
			disableDeltaClient: true,
			wantOutcome:        v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name:        "hinted image imports into container storage",
			delta:       &OCIDeltaTarget{Hint: candidate, Application: "app"},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
			setup: func(exec *executer.MockExecuter) {
				copyDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", gomock.Any(), gomock.Any()).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
				refreshRegistryReference := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", refreshImage).Return("", "", 0)
				gomock.InOrder(copyDelta, importDelta, refreshRegistryReference)
			},
		},
		{
			name:  "registry reference refresh failure falls back to a full image pull",
			delta: &OCIDeltaTarget{Hint: candidate, Application: "app"},
			setup: func(exec *executer.MockExecuter) {
				copyDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", gomock.Any(), gomock.Any()).Return("", "", 0)
				importDelta := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
				refreshRegistryReference := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", refreshImage).Return("", "registry refresh failed", 1)
				fullPull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				gomock.InOrder(copyDelta, importDelta, refreshRegistryReference, fullPull)
			},
			wantFallback:     true,
			wantOutcome:      v1beta1.DeviceDeltaApplyOutcomeFallback,
			fallbackContains: "registry refresh failed",
		},
		{
			name:               "missing candidate full-pulls without initializing the delta client",
			delta:              &OCIDeltaTarget{Application: "app"},
			disableDeltaClient: true,
			deltaFactory: func(v1beta1.Username) (*client.OCIDelta, error) {
				return nil, fmt.Errorf("factory should not be called without a candidate")
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name:               "When a hinted delta client is unavailable it should fall back to a full pull",
			delta:              &OCIDeltaTarget{Hint: candidate, Application: "app"},
			disableDeltaClient: true,
			wantOutcome:        v1beta1.DeviceDeltaApplyOutcomeFallback,
			wantFallback:       true,
			fallbackContains:   "OCI delta client is unavailable",
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name:               "hinted candidate falls back when delta client creation fails",
			delta:              &OCIDeltaTarget{Hint: candidate, Application: "app"},
			disableDeltaClient: true,
			deltaFactory: func(v1beta1.Username) (*client.OCIDelta, error) {
				return nil, fmt.Errorf("delta client creation failed")
			},
			wantFallback:     true,
			wantOutcome:      v1beta1.DeviceDeltaApplyOutcomeFallback,
			fallbackContains: "delta client creation failed",
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
		},
		{
			name: "When the source digest matches but the image is absent it should full-pull and report NotUsed",
			delta: &OCIDeltaTarget{
				SourceDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				Application:  "app",
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			setup: func(exec *executer.MockExecuter) {
				inspectTarget := exec.EXPECT().ExecuteWithContext(
					gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image,
				).Return("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "", 0)
				fullPull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
				gomock.InOrder(inspectTarget, fullPull)
			},
		},
		{
			name:  "failed delta falls back to full pull",
			delta: &OCIDeltaTarget{Hint: candidate, Application: "app"},
			setup: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", gomock.Any(), gomock.Any()).Return("", "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "import failed", 1)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			},
			wantFallback:     true,
			wantOutcome:      v1beta1.DeviceDeltaApplyOutcomeFallback,
			fallbackContains: "import failed",
		},
		{
			name:               "successful retry delta import clears previous fallback",
			delta:              &OCIDeltaTarget{Hint: candidate, Application: "app"},
			retryAfterFallback: true,
			wantOutcome:        v1beta1.DeviceDeltaApplyOutcomeApplied,
			setup: func(exec *executer.MockExecuter) {
				firstCopy := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", gomock.Any(), gomock.Any()).Return("", "", 0)
				firstImport := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "import failed", 1)
				firstFullPull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "pull failed", 1)
				secondCopy := exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "copy", gomock.Any(), gomock.Any()).Return("", "", 0)
				secondImport := exec.EXPECT().ExecuteWithContext(gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any()).Return("", "", 0)
				secondRegistryReferenceRefresh := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", refreshImage).Return("", "", 0)
				gomock.InOrder(firstCopy, firstImport, firstFullPull, secondCopy, secondImport, secondRegistryReferenceRefresh)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			if tt.delta.Hint != "" && (!tt.disableDeltaClient || tt.deltaFactory != nil) {
				attempts := 1
				if tt.retryAfterFallback {
					attempts = 2
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate).Return(podmanDeltaManifest, "", 0).Times(attempts)
				expectPodmanDeltaTarget(t, exec, image, attempts)
				expectPodmanDeltaSource(exec, attempts)
			}
			if tt.delta.Hint == "" && (!tt.disableDeltaClient || tt.deltaFactory != nil) {
				expectPlatformReferrers(t, exec, image, podmanDeltaTargetDigest, `{"manifests":[]}`)
			}
			tt.setup(exec)

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			var ociDelta *client.OCIDelta
			if !tt.disableDeltaClient {
				ociDelta = client.NewOCIDelta(logger, exec, time.Minute)
			}
			target := imageRef{image: image, ociType: OCITypePodmanImage}
			task := &prefetchTask{delta: tt.delta, deltaGeneration: 1, applicationTargetKey: applicationImageTargetKeyFor(target, "", OCITypePodmanImage)}
			manager := &prefetchManager{
				log:                   logger,
				readWriter:            rw,
				pullTimeout:           time.Minute,
				ociDelta:              ociDelta,
				ociDeltaFactory:       tt.deltaFactory,
				tasks:                 map[imageRef]*prefetchTask{target: task},
				deltaGeneration:       1,
				deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
				deltaTargetsScheduled: true,
				deltaTargetRefs:       map[string]imageRef{target.targetID(): target},
				deltaAppTargetKeys: map[string]map[string]string{
					"app": {target.targetID(): task.applicationTargetKey},
				},
			}

			pull := func() error {
				return manager.pullApplicationImage(context.Background(), target, task, podman, skopeo, client.Timeout(time.Minute))
			}
			if tt.retryAfterFallback {
				require.Error(t, pull())
			}
			err := pull()
			task.done = true
			deviceStatus := &v1beta1.DeviceStatus{
				Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}},
			}
			require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
			if tt.wantOutcome != "" {
				require.NotNil(t, deviceStatus.Applications[0].LastDelta)
				require.Equal(t, tt.wantOutcome, deviceStatus.Applications[0].LastDelta.Outcome)
			} else {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
			}
			if tt.wantFallback {
				require.NotNil(t, deviceStatus.Applications[0].LastDelta)
				require.NotNil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
				require.NotEmpty(t, *deviceStatus.Applications[0].LastDelta.FallbackReason)
				if tt.fallbackContains != "" {
					require.Contains(t, *deviceStatus.Applications[0].LastDelta.FallbackReason, tt.fallbackContains)
				}
			}
			require.NoError(t, err)
			matches, globErr := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
			require.NoError(t, globErr)
			require.Empty(t, matches)
		})
	}
}

func TestPresentApplicationImageIsReusedWithoutFollowingMutableTag(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/app:v2"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		staleDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	)

	tests := []struct {
		name            string
		deltaHint       string
		sourceDigest    string
		targetDigest    string
		previousOutcome v1beta1.DeviceDeltaApplyOutcomeType
		wantOutcome     v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{
			name:         "When a cached image has a source digest but no hint it should report NotRequired",
			sourceDigest: digest,
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:        "When a cached image has a delta hint it should report NotRequired",
			deltaHint:   "quay.io/acme/delta:target",
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:        "When a cached image has no delta metadata it should report NotRequired",
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:         "When the cached image differs from target digest metadata it should withhold NotRequired",
			targetDigest: staleDigest,
		},
		{
			name:            "When a cached image matches without delta metadata it should upgrade NotUsed to NotRequired",
			previousOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
			wantOutcome:     v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name:            "When a cached image has prior delta history it should preserve Applied",
			sourceDigest:    digest,
			previousOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
			wantOutcome:     v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
			requestedDigestValue := tt.targetDigest
			if requestedDigestValue == "" {
				requestedDigestValue = digest
			}

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			target := imageRef{image: image, ociType: OCITypePodmanImage}
			targetID := target.targetID()
			targetKey := applicationImageTargetKeyFor(target, requestedDigestValue, OCITypePodmanImage)
			task := &prefetchTask{
				ociType:              OCITypePodmanImage,
				targetDigest:         requestedDigestValue,
				applicationTargetKey: targetKey,
				targetPresent:        true,
				delta:                &OCIDeltaTarget{Hint: tt.deltaHint, SourceDigest: tt.sourceDigest, Application: application},
				deltaGeneration:      1,
			}
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{application: "spec"}))
			lifetime := store.ApplicationLifetime(application)
			require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, true))
			task.applicationAttempts = map[string]deltastatus.ApplicationAttempt{application: {Application: application, Lifetime: lifetime, SpecKey: "spec", TargetID: targetID, TargetKey: targetKey}}
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).
				Return(fmt.Sprintf(`{"Id":"immutable-config","Digest":%q}`, digest), "", 0)
			manager := &prefetchManager{
				deltaStatusStore:  store,
				deltaAppSpecKeys:  map[string]string{application: "spec"},
				deltaAppLifetimes: map[string]string{application: lifetime},
				log:               logger,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
					return podman, nil
				},
				pullTimeout:           time.Minute,
				tasks:                 map[imageRef]*prefetchTask{target: task},
				deltaGeneration:       1,
				deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
				deltaTargetsScheduled: true,
				deltaTargetRefs:       map[string]imageRef{targetID: target},
				deltaAppTargetKeys: map[string]map[string]string{
					application: {targetID: targetKey},
				},
			}
			if tt.previousOutcome != "" {
				manager.deltaApplyResults[application] = map[imageRef]applicationDeltaApplyResult{
					target: {outcome: tt.previousOutcome, targetKey: targetKey},
				}
			}

			manager.mu.Lock()
			needsQueue := manager.checkCachedApplicationTask(context.Background(), target, task)
			manager.mu.Unlock()
			require.False(t, needsQueue)

			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
			if tt.wantOutcome == "" {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
				return
			}
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, tt.wantOutcome, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestOCIArtifactWithDeltaHintFallsBackToFullArtifactPull(t *testing.T) {
	const (
		application    = "app"
		image          = "quay.io/acme/artifact:v2"
		candidate      = "quay.io/acme/delta:target"
		artifactDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	for _, hint := range []string{"", candidate} {
		t.Run("When an artifact has hint "+hint+" it should keep full artifact handling", func(t *testing.T) {

			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			if hint != "" {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("", "artifact not found", 1)
			}
			versionBeforePull := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0)
			pullArtifact := exec.EXPECT().ExecuteWithContext(
				gomock.Any(), "podman", "artifact", "pull", image,
			).Return("", "", 0)
			versionBeforeInspect := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0)
			inspectArtifact := exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).
				Return(fmt.Sprintf(`{"Digest":%q}`, artifactDigest), "", 0)
			gomock.InOrder(versionBeforePull, pullArtifact, versionBeforeInspect, inspectArtifact)

			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			specKey := "app-spec"
			lifetime, err := store.EnsureApplicationLifetime(application)
			require.NoError(err)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{application: specKey}))
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			target := imageRef{image: image, owner: v1beta1.CurrentProcessUsername, ociType: OCITypePodmanArtifact}
			targetID := target.targetID()
			targetKey := applicationImageTargetKeyFor(target, "sha256:target", OCITypePodmanArtifact)
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{application: {targetID: targetKey}}, true))
			task := &prefetchTask{
				ociType:              OCITypePodmanArtifact,
				targetDigest:         "sha256:target",
				applicationTargetKey: targetKey,
				applicationAttempts:  map[string]deltastatus.ApplicationAttempt{application: {Application: application, Lifetime: lifetime, SpecKey: specKey, TargetID: targetID, TargetKey: targetKey}},
				delta:                &OCIDeltaTarget{Hint: hint, Application: application},
				deltaGeneration:      1,
			}
			manager := &prefetchManager{
				log: logger,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
					return podman, nil
				},
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) {
					return skopeo, nil
				},
				pullTimeout:           time.Minute,
				tasks:                 map[imageRef]*prefetchTask{target: task},
				deltaGeneration:       1,
				deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
				deltaStatusStore:      store,
				deltaAppSpecKeys:      map[string]string{application: specKey},
				deltaAppLifetimes:     map[string]string{application: lifetime},
				deltaTargetsScheduled: true,
				deltaTargetRefs:       map[string]imageRef{targetID: target},
				deltaAppTargetKeys: map[string]map[string]string{
					application: {targetID: targetKey},
				},
			}

			require.NoError(manager.pull(context.Background(), target, task))
			task.done = true

			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
			require.NotNil(deviceStatus.Applications[0].LastDelta)
			if hint == "" {
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, deviceStatus.Applications[0].LastDelta.Outcome)
				require.Nil(deviceStatus.Applications[0].LastDelta.FallbackReason)
			} else {
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
				require.NotNil(deviceStatus.Applications[0].LastDelta.FallbackReason)
				require.Contains(*deviceStatus.Applications[0].LastDelta.FallbackReason, "not supported for OCI artifacts")
			}
			observedArtifact := deltastatus.ApplicationImage{
				Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanArtifact), Digest: artifactDigest,
			}
			persisted := store.ObservedApplicationResultForLifetime(application, lifetime, []deltastatus.ApplicationImage{observedArtifact}, true)
			require.NotNil(persisted, "a full artifact pull must bind its result to the inspected artifact manifest")
			require.Equal(deviceStatus.Applications[0].LastDelta.Outcome, persisted.Outcome)
			require.Nil(store.ObservedApplicationResultForLifetime(application, lifetime, []deltastatus.ApplicationImage{{
				Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanArtifact), Digest: "sha256:other",
			}}, true), "an artifact outcome must not match another manifest digest")
		})
	}
}

func TestWhenPresentHintedArtifactIsObservedItShouldPersistFallbackHistory(t *testing.T) {
	const (
		application    = "compose"
		image          = "quay.io/acme/compose:target"
		hint           = "quay.io/acme/delta:target"
		artifactDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).
		Return(fmt.Sprintf(`{"Digest":%q}`, artifactDigest), "", 0).Times(3)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0)
	logger := log.NewPrefixLogger("test")
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
	manager := NewPrefetchManager(
		logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.NewConfig(time.Millisecond, 2),
		WithDeltaStatusStore(store),
	)
	defer manager.Cleanup()
	compose := v1beta1.ComposeApplication{Name: lo.ToPtr(application), AppType: v1beta1.AppTypeCompose}
	require.NoError(t, compose.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
	var appSpec v1beta1.ApplicationProviderSpec
	require.NoError(t, appSpec.FromComposeApplication(compose))
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{appSpec}}
	target := OCIPullTarget{
		Type: OCITypePodmanArtifact, Reference: image, Digest: artifactDigest,
		PullPolicy: v1beta1.PullIfNotPresent,
		Delta:      &OCIDeltaTarget{Hint: hint, Application: application},
	}
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {target}}}, nil
	}))
	require.ErrorIs(t, manager.BeforeUpdate(context.Background(), nil, desired), errors.ErrPrefetchNotReady)
	stopWorker := startPrefetchWorker(t, manager)
	defer stopWorker()
	require.Eventually(t, func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond)
	require.Len(t, manager.tasks, 1)
	require.NoError(t, manager.BeforeUpdate(context.Background(), desired, desired))
	lifetime := store.ApplicationLifetime(application)
	observed := deltastatus.ApplicationImage{
		Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanArtifact), Digest: artifactDigest,
	}
	persisted := store.ObservedApplicationResultForLifetime(application, lifetime, []deltastatus.ApplicationImage{observed}, true)
	require.NotNil(t, persisted)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, persisted.Outcome)
}

func TestWhenPresentMutableApplicationImageIsCollectedItShouldFollowPullIfMissing(t *testing.T) {
	const (
		application = "compose"
		localDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	for _, tc := range []struct {
		name            string
		reference       string
		desiredDigest   string
		wantNotRequired bool
	}{
		{name: "When a mutable tag has no desired digest it should withhold NotRequired", reference: "quay.io/acme/compose:stable"},
		{name: "When a cached tag has a different desired digest it should withhold NotRequired", reference: "quay.io/acme/compose:stable", desiredDigest: "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{name: "When a cached tag matches the desired digest it should report NotRequired", reference: "quay.io/acme/compose:stable", desiredDigest: localDigest, wantNotRequired: true},
		{name: "When a cached pinned reference matches it should report NotRequired", reference: "quay.io/acme/compose@" + localDigest, wantNotRequired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			image := tc.reference
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(3)
			localMetadata, err := json.Marshal(client.PodmanImageMetadata{
				ID:     "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				Digest: localDigest,
			})
			require.NoError(t, err)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).
				Return(string(localMetadata), "", 0).Times(2)

			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			podman := client.NewPodman(logger, exec, rw, poll.Config{})
			skopeo := client.NewSkopeo(logger, exec, rw)
			resources := resource.NewMockManager(ctrl)
			resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(2)
			manager := NewPrefetchManager(
				logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{},
				WithDeltaStatusStore(store),
			)
			defer manager.Cleanup()

			compose := v1beta1.ComposeApplication{Name: lo.ToPtr(application), AppType: v1beta1.AppTypeCompose}
			require.NoError(t, compose.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
			var appSpec v1beta1.ApplicationProviderSpec
			require.NoError(t, appSpec.FromComposeApplication(compose))
			desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{appSpec}}
			target := OCIPullTarget{
				Type: OCITypePodmanImage, Reference: image, Digest: tc.desiredDigest, PullPolicy: v1beta1.PullIfNotPresent,
				Delta: &OCIDeltaTarget{Hint: "quay.io/acme/delta:target", SourceDigest: localDigest, Application: application},
			}
			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {target}}}, nil
			}))

			require.NoError(t, manager.BeforeUpdate(context.Background(), nil, desired))
			require.NoError(t, manager.BeforeUpdate(context.Background(), desired, desired))
			lifetime := store.ApplicationLifetime(application)
			persisted := store.ObservedApplicationResultForLifetime(application, lifetime, []deltastatus.ApplicationImage{{
				Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanImage), Digest: localDigest,
			}}, true)
			require.True(t, manager.IsReady(context.Background()))
			if tc.wantNotRequired {
				require.NotNil(t, persisted)
				require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, persisted.Outcome)
			} else {
				require.Nil(t, persisted, "local presence does not verify the desired digest behind a mutable tag")
			}
		})
	}
}

func TestWhenAQueuedTargetSurvivesANewGenerationItShouldPullOnlyOnce(t *testing.T) {
	const (
		blockerImage = "quay.io/acme/blocker:v1"
		image        = "quay.io/acme/workload:v1"
		fenceImage   = "quay.io/acme/fence:v1"
	)

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", gomock.Any()).Return("", "", 1).AnyTimes()
	blockerStarted := make(chan struct{}, 1)
	fenceStarted := make(chan struct{}, 1)
	releaseBlocker := make(chan struct{})
	var releaseBlockerOnce sync.Once
	unblock := func() { releaseBlockerOnce.Do(func() { close(releaseBlocker) }) }
	defer unblock()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", blockerImage).DoAndReturn(
		func(context.Context, string, ...string) (string, string, int) {
			blockerStarted <- struct{}{}
			<-releaseBlocker
			return "", "", 0
		},
	)
	var imagePulls atomic.Int32
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).DoAndReturn(
		func(context.Context, string, ...string) (string, string, int) {
			imagePulls.Add(1)
			return "", "", 0
		},
	).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", fenceImage).DoAndReturn(
		func(context.Context, string, ...string) (string, string, int) {
			fenceStarted <- struct{}{}
			return "", "", 0
		},
	)

	logger := log.NewPrefixLogger("test")
	rw := fileio.NewReadWriter(fileio.NewReader(), fileio.NewWriter())
	pollConfig := poll.NewConfig(time.Millisecond, 2)
	podman := client.NewPodman(logger, exec, rw, pollConfig)
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
	manager := NewPrefetchManager(
		logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		nil,
		client.NewCLIClients(), rw, util.Duration(time.Second), resources, pollConfig,
	)
	targets := []OCIPullTarget{
		{Type: OCITypePodmanImage, Reference: blockerImage, PullPolicy: v1beta1.PullIfNotPresent},
		{Type: OCITypePodmanImage, Reference: image, PullPolicy: v1beta1.PullIfNotPresent},
		{Type: OCITypePodmanImage, Reference: fenceImage, PullPolicy: v1beta1.PullIfNotPresent},
	}
	collection := 0
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		var selected []OCIPullTarget
		switch collection {
		case 0:
			selected = targets[:1]
		case 1:
			selected = targets[:2]
		case 2:
			selected = targets[1:2]
		default:
			selected = targets[1:]
		}
		collection++
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: selected}}, nil
	}))

	noApplications := &v1beta1.DeviceSpec{}
	emptyApplications := []v1beta1.ApplicationProviderSpec{}
	withEmptyApplications := &v1beta1.DeviceSpec{Applications: &emptyApplications}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	go func() {
		manager.Run(workerCtx)
		close(workerDone)
	}()
	defer func() {
		unblock()
		cancelWorker()
		select {
		case <-workerDone:
		case <-time.After(2 * time.Second):
			t.Error("prefetch worker did not stop")
		}
		manager.Cleanup()
	}()

	require.ErrorIs(t, manager.BeforeUpdate(context.Background(), nil, noApplications), errors.ErrPrefetchNotReady)
	select {
	case <-blockerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("blocker pull did not start")
	}
	require.ErrorIs(t, manager.BeforeUpdate(context.Background(), noApplications, withEmptyApplications), errors.ErrPrefetchNotReady)
	require.ErrorIs(t, manager.BeforeUpdate(context.Background(), withEmptyApplications, noApplications), errors.ErrPrefetchNotReady)
	require.ErrorIs(t, manager.BeforeUpdate(context.Background(), noApplications, withEmptyApplications), errors.ErrPrefetchNotReady)

	unblock()
	select {
	case <-fenceStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fence pull did not start")
	}
	require.EqualValues(t, 1, imagePulls.Load())
}

func TestWhenObservedApplicationOutcomesAreAggregatedItShouldApplySharedPrecedence(t *testing.T) {
	testCases := []struct {
		name         string
		statuses     []v1beta1.DeviceDeltaApplyStatus
		wantOutcome  v1beta1.DeviceDeltaApplyOutcomeType
		wantFallback *string
	}{
		{
			name: "When every image delta applies it should report Applied",
			statuses: []v1beta1.DeviceDeltaApplyStatus{
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
		{
			name: "When Applied is combined with NotRequired it should report Applied",
			statuses: []v1beta1.DeviceDeltaApplyStatus{
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
		},
		{
			name: "When every target is already at its desired digest it should report NotRequired",
			statuses: []v1beta1.DeviceDeltaApplyStatus{
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotRequired,
		},
		{
			name: "When every image delta falls back it should report Fallback",
			statuses: []v1beta1.DeviceDeltaApplyStatus{{
				Outcome:        v1beta1.DeviceDeltaApplyOutcomeFallback,
				FallbackReason: lo.ToPtr("import failed"),
			}},
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomeFallback,
			wantFallback: lo.ToPtr("import failed"),
		},
		{
			name: "When image targets have Applied and Fallback results it should report Partial",
			statuses: []v1beta1.DeviceDeltaApplyStatus{
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeFallback, FallbackReason: lo.ToPtr("import failed")},
			},
			wantOutcome:  v1beta1.DeviceDeltaApplyOutcomePartial,
			wantFallback: lo.ToPtr("import failed"),
		},
		{
			name: "When no image target uses a delta it should report NotUsed",
			statuses: []v1beta1.DeviceDeltaApplyStatus{
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
				{Outcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
			},
			wantOutcome: v1beta1.DeviceDeltaApplyOutcomeNotUsed,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"app": "spec"}))

			targets := make(map[string]string, len(tc.statuses))
			images := make([]deltastatus.ApplicationImage, len(tc.statuses))
			for i := range tc.statuses {
				image := deltastatus.ApplicationImage{
					Image: fmt.Sprintf("quay.io/acme/workload-%d:latest", i),
					Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanImage),
					Digest: fmt.Sprintf("sha256:observed-%d", i),
				}
				images[i] = image
				targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
				targets[targetID] = fmt.Sprintf("target-%d", i)
			}
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"app": targets}, true))
			for i, image := range images {
				targetID := deltastatus.Fingerprint(image.Owner, image.Image, image.OCIType)
				require.NoError(store.RecordApplicationImageResult("app", "spec", targetID, targets[targetID], image, tc.statuses[i]))
			}

			status := store.ObservedApplicationResult("app", images, true)
			require.NotNil(status)
			require.Equal(tc.wantOutcome, status.Outcome)
			require.Equal(tc.wantFallback, status.FallbackReason)
		})
	}
}

func TestApplicationDeltaStatusWaitsForTargetsToBeScheduled(t *testing.T) {
	const application = "app"
	target := imageRef{image: "quay.io/acme/workload:target", ociType: OCITypePodmanImage}
	const targetKey = "target-key"
	manager := &prefetchManager{
		tasks: make(map[imageRef]*prefetchTask),
		deltaApplyResults: map[string]map[imageRef]applicationDeltaApplyResult{
			application: {target: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied, targetKey: targetKey}},
		},
		deltaAppTargetKeys: map[string]map[string]string{
			application: {target.targetID(): targetKey},
		},
		deltaTargetRefs: map[string]imageRef{target.targetID(): target},
	}

	for _, tt := range []struct {
		name      string
		scheduled bool
	}{
		{name: "When targets are still being scheduled it should withhold LastDelta"},
		{name: "When all targets are scheduled it should publish LastDelta", scheduled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager.deltaTargetsScheduled = tt.scheduled
			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
			if !tt.scheduled {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
				return
			}
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestApplicationDeltaStatusWaitsForAllTargetsToHaveResults(t *testing.T) {
	const application = "app"
	manager := &prefetchManager{
		deltaTargetsScheduled: true,
		deltaTargetRefs:       make(map[string]imageRef),
		tasks:                 make(map[imageRef]*prefetchTask),
		deltaApplyResults: map[string]map[imageRef]applicationDeltaApplyResult{
			application: make(map[imageRef]applicationDeltaApplyResult),
		},
		deltaAppTargetKeys: map[string]map[string]string{
			application: make(map[string]string),
		},
	}

	for _, image := range []string{
		"quay.io/acme/workload-1:target",
		"quay.io/acme/workload-2:target",
		"quay.io/acme/workload-3:target",
		"quay.io/acme/workload-4:target",
		"quay.io/acme/workload-5:target",
	} {
		target := imageRef{image: image, ociType: OCITypePodmanImage}
		targetKey := "target-key:" + image
		manager.tasks[target] = &prefetchTask{
			delta:                &OCIDeltaTarget{Application: application},
			applicationTargetKey: targetKey,
			done:                 true,
		}
		manager.deltaAppTargetKeys[application][target.targetID()] = targetKey
		manager.deltaTargetRefs[target.targetID()] = target
		manager.deltaApplyResults[application][target] = applicationDeltaApplyResult{
			outcome:   v1beta1.DeviceDeltaApplyOutcomeApplied,
			targetKey: targetKey,
		}
	}

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	lastTarget := imageRef{image: "quay.io/acme/workload-6:target", ociType: OCITypePodmanImage}
	lastTargetKey := "target-key:" + lastTarget.image
	lastTask := &prefetchTask{
		delta:                &OCIDeltaTarget{Application: application},
		applicationTargetKey: lastTargetKey,
	}
	manager.tasks[lastTarget] = lastTask
	manager.deltaAppTargetKeys[application][lastTarget.targetID()] = lastTargetKey
	manager.deltaTargetRefs[lastTarget.targetID()] = lastTarget
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.Nil(t, deviceStatus.Applications[0].LastDelta)

	manager.deltaApplyResults[application][lastTarget] = applicationDeltaApplyResult{
		outcome:        v1beta1.DeviceDeltaApplyOutcomeFallback,
		fallbackReason: "delta import failed",
		targetKey:      lastTargetKey,
	}
	lastTask.done = true
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomePartial, deviceStatus.Applications[0].LastDelta.Outcome)

	// A later collection can remove a target while leaving its prior result in
	// memory. Its result must not affect the current application's outcome.
	delete(manager.deltaAppTargetKeys[application], lastTarget.targetID())
	delete(manager.deltaTargetRefs, lastTarget.targetID())
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	manager.tasks = make(map[imageRef]*prefetchTask)
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
}

func TestApplicationDeltaStatusUsesCurrentTargetReferences(t *testing.T) {
	const application = "app"
	const targetKey = "current-target"
	target := imageRef{image: "quay.io/acme/workload:target", owner: "app-user", ociType: OCITypePodmanImage}
	targetID := target.targetID()

	for _, tt := range []struct {
		name        string
		missingRef  bool
		otherOwner  bool
		staleResult bool
		staleTask   bool
		withoutTask bool
		wantStatus  bool
	}{
		{name: "When the reference is missing it should withhold LastDelta", missingRef: true},
		{name: "When another owner has the same image it should withhold LastDelta", otherOwner: true},
		{name: "When the result belongs to an older target it should withhold LastDelta", staleResult: true},
		{name: "When the task belongs to an older target it should withhold LastDelta", staleTask: true},
		{name: "When the completed task is cleaned up it should retain LastDelta", withoutTask: true, wantStatus: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := &prefetchManager{
				deltaTargetsScheduled: true,
				deltaAppTargetKeys:    map[string]map[string]string{application: {targetID: targetKey}},
				deltaTargetRefs:       map[string]imageRef{targetID: target},
				deltaApplyResults: map[string]map[imageRef]applicationDeltaApplyResult{
					application: {target: {outcome: v1beta1.DeviceDeltaApplyOutcomeApplied, targetKey: targetKey}},
				},
				tasks: map[imageRef]*prefetchTask{target: {applicationTargetKey: targetKey, done: true}},
			}
			if tt.missingRef {
				delete(manager.deltaTargetRefs, targetID)
			}
			if tt.otherOwner {
				otherTarget := imageRef{image: target.image, owner: "other-user", ociType: OCITypePodmanImage}
				manager.deltaApplyResults[application][otherTarget] = manager.deltaApplyResults[application][target]
				delete(manager.deltaApplyResults[application], target)
			}
			if tt.staleResult {
				manager.deltaApplyResults[application][target] = applicationDeltaApplyResult{
					outcome: v1beta1.DeviceDeltaApplyOutcomeApplied, targetKey: "previous-target",
				}
			}
			if tt.staleTask {
				manager.tasks[target].applicationTargetKey = "previous-target"
			}
			if tt.withoutTask {
				delete(manager.tasks, target)
			}

			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: application}}}
			require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
			if !tt.wantStatus {
				require.Nil(t, deviceStatus.Applications[0].LastDelta)
				return
			}
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestApplicationTargetIdentitiesTrackTargetsWithoutDeltaHintOrSourceDigest(t *testing.T) {
	target := imageRef{image: "quay.io/acme/app:latest", ociType: OCITypePodmanImage}
	targetKey := applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
	appTargets, targetRefs := applicationTargetIdentities(map[imageRef]OCIPullTarget{
		target: {
			Type:      OCITypePodmanImage,
			Reference: target.image,
			Delta:     &OCIDeltaTarget{Application: "app"},
		},
	})

	targetID := target.targetID()
	require.Equal(t, map[string]map[string]string{"app": {targetID: targetKey}}, appTargets)
	require.Equal(t, map[string]imageRef{targetID: target}, targetRefs)
}

func TestPrepareTaskReusedCompletedTargetKeepsCacheWhenDigestCheckIsUnavailable(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/workload:target"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	target := imageRef{image: image, ociType: OCITypePodmanImage}
	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, fileio.NewMockReadWriter(ctrl), poll.NewConfig(time.Millisecond, 2))
	delta := &OCIDeltaTarget{Hint: "quay.io/acme/delta:target", Application: application}
	task := &prefetchTask{
		ociType:              OCITypePodmanImage,
		targetDigest:         digest,
		applicationTargetKey: applicationImageTargetKeyFor(target, digest, OCITypePodmanImage),
		delta:                delta,
		deltaGeneration:      1,
		done:                 true,
	}
	manager := &prefetchManager{
		log:               logger,
		podmanFactory:     func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		tasks:             map[imageRef]*prefetchTask{target: task},
		deltaGeneration:   2,
		deltaApplyResults: make(map[string]map[imageRef]applicationDeltaApplyResult),
	}

	needsQueue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, digest, nil, delta)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.Equal(t, uint64(2), task.deltaGeneration)
	require.True(t, task.targetPresent)
	require.True(t, task.done)
	require.Empty(t, manager.deltaApplyResults)
}

func TestPrepareTaskCachedDigestChecks(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const currentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const differentDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testCases := []struct {
		name           string
		reused         bool
		sameGeneration bool
		missing        bool
		wantQueue      bool
	}{
		{name: "When the cached image is present it should complete without resolving its tag"},
		{name: "When the cached image tag moves it should complete without a pull"},
		{name: "When registry inspection would fail it should still reuse the cache"},
		{name: "When a reused task changes generation it should keep a present image", reused: true},
		{name: "When a reused image was pruned it should queue a pull", reused: true, missing: true, wantQueue: true},
		{name: "When a reused image was pruned in the same generation it should queue a pull", reused: true, sameGeneration: true, missing: true, wantQueue: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			existsExit := 0
			if testCase.missing {
				existsExit = 1
			}
			existsCalls := 1
			if !testCase.reused {
				existsCalls++
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", existsExit).Times(existsCalls)
			if testCase.missing {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("", "", 1)
			}
			logger := log.NewPrefixLogger("test")
			readWriter := fileio.NewMockReadWriter(ctrl)
			podman := client.NewPodman(logger, mockExec, readWriter, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, mockExec, readWriter)
			manager := &prefetchManager{
				log: logger, tasks: make(map[imageRef]*prefetchTask), deltaGeneration: 2,
				podmanFactory: func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				ociDelta:      client.NewOCIDelta(logger, mockExec, time.Minute),
			}
			target := imageRef{image: image, ociType: OCITypePodmanImage}
			delta := &OCIDeltaTarget{Application: "app", SourceDigest: currentDigest}
			if testCase.reused {
				manager.tasks[target] = &prefetchTask{ociType: OCITypePodmanImage, done: true, deltaGeneration: 1, resolvedDigest: currentDigest}
				if testCase.sameGeneration {
					manager.tasks[target].deltaGeneration = 2
					manager.tasks[target].delta = delta
					manager.tasks[target].resolvedDigest = ""
				}
			}
			needsQueue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, "", nil, delta)
			require.NoError(err)
			require.Equal(testCase.wantQueue, needsQueue)
			task := manager.tasks[target]
			require.Equal(!testCase.wantQueue, task.done)
			if !testCase.wantQueue {
				require.NoError(manager.checkReady(context.Background()))
				require.Empty(manager.deltaApplyResults)
				return
			}
			require.False(task.targetPresent)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).Return(differentDigest, "", 0)
			require.ErrorIs(manager.checkReady(context.Background()), errors.ErrPrefetchNotReady)
			expectPlatformReferrers(t, mockExec, image, differentDigest, `{"manifests": []}`)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("", "", 0)
			require.NoError(manager.pull(context.Background(), target, task))
		})
	}
}

func TestPrepareTaskReusesExistingImageWithoutResolvingItsTag(t *testing.T) {
	const (
		image     = "quay.io/acme/workload:target"
		candidate = "quay.io/acme/delta:target"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(2)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	target := imageRef{image: image, ociType: OCITypePodmanImage}
	manager := &prefetchManager{
		log: logger,
		podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
			return podman, nil
		},
		tasks:           make(map[imageRef]*prefetchTask),
		deltaGeneration: 1,
	}

	needsQueue, err := manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanImage,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Hint: candidate, Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].targetPresent)
	require.True(t, manager.tasks[target].done)
	require.Empty(t, manager.deltaApplyResults)
}

func TestPrepareTaskSkipsExistingArtifactWithoutDeltaHint(t *testing.T) {
	const (
		image = "quay.io/acme/artifact:v2"
	)

	ctrl := gomock.NewController(t)
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return(`{"Digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, "", 0).AnyTimes()

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	target := imageRef{image: image, ociType: OCITypePodmanArtifact}
	manager := &prefetchManager{
		log: logger,
		podmanFactory: func(v1beta1.Username) (*client.Podman, error) {
			return podman, nil
		},
		tasks:           make(map[imageRef]*prefetchTask),
		deltaGeneration: 1,
	}

	needsQueue, err := manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanArtifact,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].done)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotUsed, manager.deltaApplyResults["app"][target].outcome)

	manager.tasks[target].done = true
	needsQueue, err = manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanArtifact,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].done)

	manager.deltaGeneration++
	needsQueue, err = manager.prepareTask(
		context.Background(),
		target,
		OCITypePodmanArtifact,
		"sha256:target",
		nil,
		&OCIDeltaTarget{Application: "app"},
	)
	require.NoError(t, err)
	require.False(t, needsQueue)
	require.True(t, manager.tasks[target].done)
}

func TestBeforeUpdateKeepsGenerationForEquivalentDesiredSpecs(t *testing.T) {
	manager := &prefetchManager{log: log.NewPrefixLogger("test")}
	ctx := context.Background()

	require.NoError(t, manager.BeforeUpdate(ctx, nil, &v1beta1.DeviceSpec{}))
	firstGeneration := manager.deltaGeneration
	require.NoError(t, manager.BeforeUpdate(ctx, nil, &v1beta1.DeviceSpec{}))

	require.Equal(t, firstGeneration, manager.deltaGeneration)
}

func TestPrepareTaskReusedCompletedNoHintTargetOnlyRechecksOnNewGeneration(t *testing.T) {
	const (
		application = "app"
		image       = "quay.io/acme/workload:target"
		digest      = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	tests := []struct {
		name           string
		taskGeneration uint64
		wantQueue      bool
	}{
		{
			name:           "When the generation is unchanged it should reuse the completed digest check",
			taskGeneration: 2,
		},
		{
			name:           "When the generation changes and inspection is unavailable it should keep the cache",
			taskGeneration: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			exec := executer.NewMockExecuter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0)
			logger := log.NewPrefixLogger("test")
			podman := client.NewPodman(logger, exec, fileio.NewMockReadWriter(ctrl), poll.NewConfig(time.Millisecond, 2))
			target := imageRef{image: image, ociType: OCITypePodmanImage}
			delta := &OCIDeltaTarget{SourceDigest: digest, Application: application}
			task := &prefetchTask{
				ociType:              OCITypePodmanImage,
				targetDigest:         digest,
				applicationTargetKey: applicationImageTargetKeyFor(target, digest, OCITypePodmanImage),
				targetPresent:        true,
				delta:                delta,
				deltaGeneration:      tt.taskGeneration,
				done:                 true,
			}
			manager := &prefetchManager{
				log:             logger,
				podmanFactory:   func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				tasks:           map[imageRef]*prefetchTask{target: task},
				deltaGeneration: 2,
			}

			needsQueue, err := manager.prepareTask(context.Background(), target, OCITypePodmanImage, digest, nil, delta)
			require.NoError(t, err)
			require.Equal(t, tt.wantQueue, needsQueue)
			require.True(t, task.targetPresent)
			require.True(t, task.done)
		})
	}
}

func TestApplicationDeltaSpecKeyIgnoresOnlyRenderedStatusAndDeltaHints(t *testing.T) {
	makeDesired := func(desiredState v1beta1.ApplicationDesiredState, restartGeneration int, deltaImage, nestedDelta string, nestedValue string) *v1beta1.DeviceSpec {
		values := map[string]interface{}{"desiredState": nestedValue, "restartGeneration": 7, "deltaImage": "user-chart-value"}
		application := v1beta1.HelmApplication{
			Name:              lo.ToPtr("app"),
			AppType:           v1beta1.AppTypeHelm,
			DesiredState:      lo.ToPtr(desiredState),
			RestartGeneration: lo.ToPtr(restartGeneration),
			Values:            &values,
		}
		require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{
			Image:      "quay.io/acme/chart:v1",
			DeltaImage: lo.ToPtr(deltaImage),
			DeltaImages: &[]v1beta1.ImageDeltaHint{{
				TargetImage:  "quay.io/acme/workload:v1",
				TargetDigest: "sha256:target",
				DeltaImage:   nestedDelta,
			}},
		}))
		var spec v1beta1.ApplicationProviderSpec
		require.NoError(t, spec.FromHelmApplication(application))
		return &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{spec}}
	}

	first, err := applicationDeltaSpecKeys(makeDesired(v1beta1.ApplicationDesiredStateRunning, 1, "quay.io/acme/delta:v1", "quay.io/acme/nested-delta:v1", "chart-value"), nil)
	require.NoError(t, err)
	second, err := applicationDeltaSpecKeys(makeDesired(v1beta1.ApplicationDesiredStateStopped, 2, "quay.io/acme/delta:v2", "quay.io/acme/nested-delta:v2", "chart-value"), nil)
	require.NoError(t, err)
	require.Equal(t, first["app"], second["app"])

	changedChartValue, err := applicationDeltaSpecKeys(makeDesired(v1beta1.ApplicationDesiredStateRunning, 1, "quay.io/acme/delta:v1", "quay.io/acme/nested-delta:v1", "changed-chart-value"), nil)
	require.NoError(t, err)
	require.NotEqual(t, first["app"], changedChartValue["app"])
}

func TestApplicationDeltaSpecKeyUsesResolvedNameWhenNameIsOmitted(t *testing.T) {
	application := v1beta1.ContainerApplication{AppType: v1beta1.AppTypeContainer}
	require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/acme/app:v1"}))
	var spec v1beta1.ApplicationProviderSpec
	require.NoError(t, spec.FromContainerApplication(application))
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{spec}}

	keys, err := applicationDeltaSpecKeys(desired, func(*v1beta1.ApplicationProviderSpec) (string, error) {
		return "quay.io/acme/app:v1", nil
	})
	require.NoError(t, err)
	require.Contains(t, keys, "quay.io/acme/app:v1")
}

func TestApplicationDeltaStatusPersistsAcrossRestartAndClearsForChangedSpec(t *testing.T) {
	const (
		imageV2       = "quay.io/acme/app:v2"
		imageV3       = "quay.io/acme/app:v3"
		imageDigestV2 = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		imageDigestV3 = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
		deltaV2       = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		deltaV3       = "quay.io/acme/delta@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	)

	root := t.TempDir()
	rw := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)

	makeApplication := func(image string) v1beta1.ApplicationProviderSpec {
		application := v1beta1.ContainerApplication{
			Name:    lo.ToPtr("app"),
			AppType: v1beta1.AppTypeContainer,
		}
		require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
		var spec v1beta1.ApplicationProviderSpec
		require.NoError(t, spec.FromContainerApplication(application))
		return spec
	}

	makeTarget := func(image, digest, deltaImage, sourceDigest string) (imageRef, OCIPullTarget) {
		ref := imageRef{image: image, ociType: OCITypePodmanImage}
		return ref, OCIPullTarget{
			Type:      OCITypePodmanImage,
			Reference: image,
			Digest:    digest,
			Delta: &OCIDeltaTarget{
				Hint:         deltaImage,
				SourceDigest: sourceDigest,
				Application:  "app",
			},
		}
	}

	refV2, targetV2 := makeTarget(imageV2, imageDigestV2, deltaV2, "sha256:source-v1")
	desiredV2 := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{makeApplication(imageV2)}}
	desiredV2WithUnrelatedOSUpdate := &v1beta1.DeviceSpec{
		Os:           &v1beta1.DeviceOsSpec{Image: "quay.io/acme/os:v2"},
		Applications: desiredV2.Applications,
	}
	specKeys, err := applicationDeltaSpecKeys(desiredV2, nil)
	require.NoError(t, err)
	targetID := refV2.targetID()
	targetKeyV2 := applicationImageTargetKey(refV2, targetV2)
	require.NoError(t, store.ReconcileApplicationSpecs(specKeys))
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{
		"app": {targetID: targetKeyV2},
	}, true))
	require.NoError(t, store.RecordApplicationResult(
		"app", specKeys["app"], targetID, targetKeyV2,
		v1beta1.DeviceDeltaApplyStatus{Outcome: v1beta1.DeviceDeltaApplyOutcomeApplied},
	))

	// Re-read the state through a new Store to simulate an agent restart.
	restartedStore := deltastatus.New(rw, "/var/lib/flightctl", logger)
	ctrl := gomock.NewController(t)
	mockExec := executer.NewMockExecuter(ctrl)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageV2).Return("", "", 0).Times(6)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", imageV3).Return("", "", 0).Times(3)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", gomock.Any()).DoAndReturn(
		func(_ context.Context, _ string, args ...string) (string, string, int) {
			switch args[len(args)-1] {
			case imageV2:
				return fmt.Sprintf(`{"Id":"immutable-v2","Digest": %q}`, imageDigestV2), "", 0
			case imageV3:
				return fmt.Sprintf(`{"Id":"immutable-v3","Digest": %q}`, imageDigestV3), "", 0
			default:
				return "", "unexpected image", 1
			}
		},
	).AnyTimes()
	mockResources := resource.NewMockManager(ctrl)
	mockResources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(6)
	podman := client.NewPodman(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, mockExec, rw)
	manager := NewPrefetchManager(
		logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		client.NewCLIClients(),
		rw,
		util.Duration(time.Minute),
		mockResources,
		poll.Config{},
		WithDeltaStatusStore(restartedStore),
	)
	var target OCIPullTarget
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {target}}}, nil
	}))
	target = targetV2
	ctx := context.Background()
	completePrefetch := func(target imageRef, current, desired *v1beta1.DeviceSpec) {
		require.NoError(t, manager.BeforeUpdate(ctx, current, desired))
		require.True(t, manager.tasks[target].done)
		require.NoError(t, manager.BeforeUpdate(ctx, current, desired))
	}
	completePrefetch(refV2, nil, desiredV2)

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	// An unrelated OS update and a changed delta hint do not erase the fact that
	// the current application image was reconstructed with a delta before restart.
	_, targetWithNewHint := makeTarget(imageV2, imageDigestV2, deltaV3, "sha256:source-v2")
	target = targetWithNewHint
	completePrefetch(refV2, desiredV2, desiredV2WithUnrelatedOSUpdate)
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	// The last outcome remains reportable after transient prefetch tasks are
	// discarded at the end of reconciliation.
	manager.mu.Lock()
	manager.tasks = make(map[imageRef]*prefetchTask)
	manager.mu.Unlock()
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	// A new application target clears the old result. Its already-cached image
	// is reported as NotRequired, rather than inheriting v2's Applied outcome.
	refV3, targetV3 := makeTarget(imageV3, imageDigestV3, deltaV3, "sha256:source-v2")
	target = targetV3
	desiredV3 := &v1beta1.DeviceSpec{
		Os:           desiredV2WithUnrelatedOSUpdate.Os,
		Applications: &[]v1beta1.ApplicationProviderSpec{makeApplication(imageV3)},
	}
	completePrefetch(refV3, desiredV2WithUnrelatedOSUpdate, desiredV3)
	deviceStatus = &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, ctx, deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, deviceStatus.Applications[0].LastDelta.Outcome)
	newTargetID := refV3.targetID()
	require.Equal(t, map[string]imageRef{newTargetID: refV3}, manager.deltaTargetRefs)
	appResults := restartedStore.ApplicationResults("app")
	require.NotContains(t, appResults, targetID)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, appResults[newTargetID].Status.Outcome)
}

func TestBeforeUpdatePreservesCurrentDeltaResultsAcrossRetries(t *testing.T) {
	const (
		image        = "quay.io/acme/app:target"
		targetDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		candidate    = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		sourceDigest = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	)

	tests := []struct {
		name                  string
		withStatusStore       bool
		withoutApplicationKey bool
	}{
		{
			name: "When persistence is disabled it should preserve the in-memory result",
		},
		{
			name:                  "When no persisted target key is available it should preserve the in-memory result",
			withStatusStore:       true,
			withoutApplicationKey: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			rw := fileio.NewReadWriter(
				fileio.NewReader(fileio.WithReaderRootDir(root)),
				fileio.NewWriter(fileio.WithWriterRootDir(root)),
			)
			logger := log.NewPrefixLogger("test")
			ctrl := gomock.NewController(t)
			mockExec := executer.NewMockExecuter(ctrl)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).Times(2)
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).Return("", "identity unavailable", 1).AnyTimes()
			mockResources := resource.NewMockManager(ctrl)
			mockResources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(3)
			podman := client.NewPodman(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2))

			var options []PrefetchManagerOption
			if tt.withStatusStore {
				options = append(options, WithDeltaStatusStore(deltastatus.New(rw, "/var/lib/flightctl", logger)))
			}
			manager := NewPrefetchManager(
				logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return nil, nil },
				client.NewCLIClients(),
				rw,
				util.Duration(time.Minute),
				mockResources,
				poll.Config{},
				options...,
			)
			defer manager.Cleanup()

			application := v1beta1.ContainerApplication{Name: lo.ToPtr("app"), AppType: v1beta1.AppTypeContainer}
			require.NoError(t, application.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
			var applicationSpec v1beta1.ApplicationProviderSpec
			require.NoError(t, applicationSpec.FromContainerApplication(application))
			desired := &v1beta1.DeviceSpec{Applications: lo.ToPtr([]v1beta1.ApplicationProviderSpec{applicationSpec})}

			target := OCIPullTarget{
				Type:      OCITypePodmanImage,
				Reference: image,
				Digest:    targetDigest,
				Delta: &OCIDeltaTarget{
					Hint:         candidate,
					SourceDigest: sourceDigest,
					Application:  "app",
				},
			}
			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{v1beta1.CurrentProcessUsername: {target}}}, nil
			}))

			ctx := context.Background()
			require.ErrorIs(t, manager.BeforeUpdate(ctx, nil, desired), errors.ErrPrefetchNotReady)
			ref := imageRef{image: image, owner: v1beta1.CurrentProcessUsername, ociType: OCITypePodmanImage}
			task := manager.tasks[ref]
			require.NotNil(t, task)
			if tt.withoutApplicationKey {
				manager.mu.Lock()
				manager.deltaAppTargetKeys = nil
				manager.mu.Unlock()
			}
			manager.recordDeltaApplied(ref, task)
			manager.setResult(ref, nil, task)

			require.NoError(t, manager.BeforeUpdate(ctx, desired, desired))
			generation := manager.deltaGeneration
			changed := *desired
			changed.Os = &v1beta1.DeviceOsSpec{Image: "quay.io/acme/os:changed"}
			require.NoError(t, manager.BeforeUpdate(ctx, desired, &changed))
			require.Equal(t, generation, manager.deltaGeneration)
			deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
			require.NoError(t, collectPrefetchTaskOutcomes(t, manager, ctx, deviceStatus))
			require.NotNil(t, deviceStatus.Applications[0].LastDelta)
			require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)
		})
	}
}

func TestApplicationDeltaPrefetchRunsAsRunAsUser(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const candidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const runAsUser v1beta1.Username = "delta-user"

	ctrl := gomock.NewController(t)
	userExec := executer.NewMockExecuter(ctrl)

	root := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	userSkopeo := client.NewSkopeo(logger, userExec, readWriter)
	userPodman := client.NewPodman(logger, userExec, readWriter, poll.NewConfig(time.Millisecond, 2))
	manager := &prefetchManager{
		log:        logger,
		readWriter: readWriter,
		readWriterFactory: func(username v1beta1.Username) (fileio.ReadWriter, error) {
			require.Equal(t, runAsUser, username)
			return readWriter, nil
		},
		ociDeltaFactory: func(username v1beta1.Username) (*client.OCIDelta, error) {
			require.Equal(t, runAsUser, username)
			return client.NewOCIDelta(logger, userExec, time.Minute), nil
		},
		pullTimeout:           time.Minute,
		tasks:                 make(map[imageRef]*prefetchTask),
		deltaGeneration:       1,
		deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
		deltaTargetsScheduled: true,
	}
	target := imageRef{image: image, owner: runAsUser, ociType: OCITypePodmanImage}
	task := &prefetchTask{delta: &OCIDeltaTarget{Hint: candidate, Application: "app"}, deltaGeneration: 1}
	task.applicationTargetKey = applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
	manager.deltaAppTargetKeys = map[string]map[string]string{
		"app": {target.targetID(): task.applicationTargetKey},
	}
	manager.deltaTargetRefs = map[string]imageRef{target.targetID(): target}
	manager.tasks[target] = task

	userExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate).Return(podmanDeltaManifest, "", 0)
	expectPodmanDeltaTarget(t, userExec, image, 1)
	expectPodmanDeltaSource(userExec, 1)

	deltaFetch := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(),
	).Return("", "", 0)
	deltaImport := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
	).Return("", "", 0)
	canonicalPull := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "pull", "quay.io/acme/app@"+podmanDeltaTargetDigest,
	).Return("", "", 0)
	gomock.InOrder(deltaFetch, deltaImport, canonicalPull)

	err := manager.pullApplicationImage(context.Background(), target, task, userPodman, userSkopeo, client.Timeout(time.Minute))
	require.NoError(t, err)
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeApplied, deviceStatus.Applications[0].LastDelta.Outcome)

	matches, err := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestApplicationDeltaPrefetchRunAsDeltaImportFailureFallsBackToFullPull(t *testing.T) {
	const image = "quay.io/acme/app:target"
	const candidate = "quay.io/acme/delta@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const runAsUser v1beta1.Username = "delta-user"

	ctrl := gomock.NewController(t)
	userExec := executer.NewMockExecuter(ctrl)
	userExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+candidate).Return(podmanDeltaManifest, "", 0)
	expectPodmanDeltaTarget(t, userExec, image, 1)
	expectPodmanDeltaSource(userExec, 1)

	root := t.TempDir()
	readWriter := fileio.NewReadWriter(
		fileio.NewReader(fileio.WithReaderRootDir(root)),
		fileio.NewWriter(fileio.WithWriterRootDir(root)),
	)
	logger := log.NewPrefixLogger("test")
	userSkopeo := client.NewSkopeo(logger, userExec, readWriter)
	userPodman := client.NewPodman(logger, userExec, readWriter, poll.NewConfig(time.Millisecond, 2))
	manager := &prefetchManager{
		log:        logger,
		readWriter: readWriter,
		readWriterFactory: func(username v1beta1.Username) (fileio.ReadWriter, error) {
			require.Equal(t, runAsUser, username)
			return readWriter, nil
		},
		ociDeltaFactory: func(username v1beta1.Username) (*client.OCIDelta, error) {
			require.Equal(t, runAsUser, username)
			return client.NewOCIDelta(logger, userExec, time.Minute), nil
		},
		pullTimeout:           time.Minute,
		tasks:                 make(map[imageRef]*prefetchTask),
		deltaGeneration:       1,
		deltaApplyResults:     make(map[string]map[imageRef]applicationDeltaApplyResult),
		deltaTargetsScheduled: true,
	}
	target := imageRef{image: image, owner: runAsUser, ociType: OCITypePodmanImage}
	task := &prefetchTask{delta: &OCIDeltaTarget{Hint: candidate, Application: "app"}, deltaGeneration: 1}
	task.applicationTargetKey = applicationImageTargetKeyFor(target, "", OCITypePodmanImage)
	manager.deltaAppTargetKeys = map[string]map[string]string{
		"app": {target.targetID(): task.applicationTargetKey},
	}
	manager.deltaTargetRefs = map[string]imageRef{target.targetID(): target}
	manager.tasks[target] = task

	deltaFetch := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "skopeo", "copy", "docker://"+candidate, gomock.Any(),
	).Return("", "", 0)
	deltaImport := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "oci-delta", "import", "--tag", image, gomock.Any(),
	).Return("", "import into RunAs storage failed", 1)
	fullPull := userExec.EXPECT().ExecuteWithContext(
		gomock.Any(), "podman", "pull", image,
	).Return("", "", 0)
	gomock.InOrder(deltaFetch, deltaImport, fullPull)

	err := manager.pullApplicationImage(context.Background(), target, task, userPodman, userSkopeo, client.Timeout(time.Minute))
	require.NoError(t, err)
	task.done = true

	deviceStatus := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
	require.NoError(t, collectPrefetchTaskOutcomes(t, manager, context.Background(), deviceStatus))
	require.NotNil(t, deviceStatus.Applications[0].LastDelta)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeFallback, deviceStatus.Applications[0].LastDelta.Outcome)
	require.NotNil(t, deviceStatus.Applications[0].LastDelta.FallbackReason)
	require.Contains(t, *deviceStatus.Applications[0].LastDelta.FallbackReason, "import into RunAs storage failed")

	matches, err := filepath.Glob(filepath.Join(root, "tmp", "application-delta*"))
	require.NoError(t, err)
	require.Empty(t, matches)
}

func TestMovedApplicationTagClearsPersistedDeltaOutcome(t *testing.T) {
	const image = "quay.io/acme/app:v2"
	const oldDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const newDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name         string
		ociType      OCIType
		reused       bool
		absent       bool
		previous     v1beta1.DeviceDeltaApplyOutcomeType
		remoteDigest string
		remoteExit   int
		localExit    int
		wantPull     bool
		want         v1beta1.DeviceDeltaApplyOutcomeType
	}{
		{name: "When a Podman image is absent after restart and its tag moves it should replace NotRequired", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When a CRI image is absent after restart and its tag moves it should replace NotRequired", ociType: OCITypeCRIImage, absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an absent image tag moves after Applied it should replace the persisted outcome", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an absent image tag moves after Fallback it should replace the persisted outcome", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeFallback, remoteDigest: newDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotUsed},
		{name: "When an absent image digest is unchanged it should preserve Applied", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeApplied, remoteDigest: oldDigest, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When registry inspection fails for an absent image it should preserve NotRequired", absent: true, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, remoteExit: 1, wantPull: true, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When a present CRI image tag moves it should preserve the cached result", ociType: OCITypeCRIImage, previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When a present tag moves between prepare generations it should preserve Applied", reused: true, previous: v1beta1.DeviceDeltaApplyOutcomeApplied, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a present tag moves after NotRequired it should preserve NotRequired", previous: v1beta1.DeviceDeltaApplyOutcomeNotRequired, want: v1beta1.DeviceDeltaApplyOutcomeNotRequired},
		{name: "When a present tag moves after Applied it should preserve Applied", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When a present tag moves after Fallback it should preserve Fallback", previous: v1beta1.DeviceDeltaApplyOutcomeFallback, want: v1beta1.DeviceDeltaApplyOutcomeFallback},
		{name: "When a present image keeps its cached content it should preserve Applied", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
		{name: "When local inspection fails it should preserve Applied", previous: v1beta1.DeviceDeltaApplyOutcomeApplied, localExit: 1, want: v1beta1.DeviceDeltaApplyOutcomeApplied},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			mockExec := executer.NewMockExecuter(ctrl)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			ociType := tt.ociType
			if ociType == "" {
				ociType = OCITypePodmanImage
			}
			target := imageRef{image: image, ociType: ociType}
			targetID := target.targetID()
			targetKey := applicationImageTargetKeyFor(target, "", ociType)
			specKeys := map[string]string{"app": "unchanged-app-spec"}
			targetKeys := map[string]map[string]string{"app": {targetID: targetKey}}
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			require.NoError(store.ReconcileApplicationSpecs(specKeys))
			lifetime := store.ApplicationLifetime("app")
			require.NoError(store.ReconcileApplicationTargets(targetKeys, true))
			require.NoError(store.RecordApplicationResult("app", specKeys["app"], targetID, targetKey, v1beta1.DeviceDeltaApplyStatus{Outcome: tt.previous}))
			restarted := deltastatus.New(rw, "/var/lib/flightctl", logger)
			podman := client.NewPodman(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, mockExec, rw)
			m := &prefetchManager{
				log: logger, tasks: make(map[imageRef]*prefetchTask), deltaGeneration: 1,
				cliClients:       client.NewCLIClients(client.WithCRIClient(client.NewCRI(logger, mockExec, rw, poll.NewConfig(time.Millisecond, 2)))),
				podmanFactory:    func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				skopeoFactory:    func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				deltaStatusStore: restarted, deltaAppSpecKeys: specKeys, deltaAppLifetimes: map[string]string{"app": lifetime}, deltaAppTargetKeys: targetKeys,
				deltaTargetRefs: map[string]imageRef{targetID: target}, deltaTargetsScheduled: true, pullTimeout: time.Minute,
			}
			m.deltaApplyResults = m.restoreApplicationDeltaResults(targetKeys, m.deltaTargetRefs)
			require.Equal(tt.previous, m.deltaApplyResults["app"][target].outcome)
			delta := &OCIDeltaTarget{Application: "app"}
			existsCalls := 2
			existsExit := 0
			criImages := "IMAGE TAG ID\napp v2 abc"
			if tt.absent {
				m.ociDelta = client.NewOCIDelta(logger, mockExec, time.Minute)
				delta.SourceDigest = oldDigest
				existsCalls = 1
				existsExit = 1
				criImages = ""
			}
			if tt.reused {
				m.tasks[target] = &prefetchTask{ociType: ociType, applicationTargetKey: targetKey, applicationAttempts: m.applicationAttemptsLocked(target, targetKey, delta), done: true, deltaGeneration: 0, resolvedDigest: oldDigest}
				existsCalls = 1
			}
			if ociType == OCITypeCRIImage {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", image).Return(criImages, "", 0).Times(existsCalls)
				if !tt.absent {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", image).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, oldDigest), "inspect failed", tt.localExit)
				}
			} else {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", existsExit).Times(existsCalls)
				if !tt.absent {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", image).Return(fmt.Sprintf(`{"Digest": %q}`, oldDigest), "inspect failed", tt.localExit)
				}
			}
			if tt.absent && tt.localExit == 0 {
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--format", "{{.Digest}}", "docker://"+image).Return(tt.remoteDigest, "inspect failed", tt.remoteExit)
			}
			mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", image).Return(`{"status":{"repoDigests":[]}}`, "", 0).AnyTimes()
			ctx := context.Background()
			needsQueue, err := m.prepareTask(ctx, target, ociType, "", nil, delta)
			require.NoError(err)
			require.Equal(tt.wantPull, needsQueue)
			if tt.wantPull {
				if !tt.absent && tt.remoteDigest == newDigest {
					// The old result must already be gone, including if the agent crashes before pulling.
					require.Empty(m.deltaApplyResults["app"])
					require.Empty(deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("app"))
				}
				if tt.absent {
					platformDigest := tt.remoteDigest
					if platformDigest == "" {
						platformDigest = oldDigest
					}
					expectPlatformReferrers(t, mockExec, image, platformDigest, `{"manifests": []}`)
				}
				assertInvalidated := func() {
					if tt.absent && tt.remoteDigest == newDigest {
						// Persist invalidation before pulling, even if the agent crashes mid-pull.
						require.Empty(m.deltaApplyResults["app"])
						require.Empty(deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("app"))
					}
				}
				if ociType == OCITypeCRIImage {
					mockExec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", image}, gomock.Any()).DoAndReturn(func(context.Context, string, string, []string, ...string) (string, string, int) {
						assertInvalidated()
						return "", "", 0
					})
				} else {
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
						assertInvalidated()
						return "", "", 0
					})
				}
				require.NoError(m.pull(ctx, target, m.tasks[target]))
				m.tasks[target].done = true
			}
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "app"}}}
			require.NoError(collectPrefetchTaskOutcomes(t, m, ctx, status))
			require.NotNil(status.Applications[0].LastDelta)
			require.Equal(tt.want, status.Applications[0].LastDelta.Outcome)
			require.Equal(tt.want, deltastatus.New(rw, "/var/lib/flightctl", logger).ApplicationResults("app")[targetID].Status.Outcome)
		})
	}
}

const podmanDeltaSource = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
const podmanDeltaTargetDigest = "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
const podmanDeltaManifest = `{"artifactType":"application/vnd.io.github.containers.oci-delta.v1","subject":{"digest":"sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","mediaType":"application/vnd.oci.image.manifest.v1+json","size":100},"annotations":{"io.github.containers.delta.source":"sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"config":{"digest":"sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","mediaType":"application/vnd.oci.image.config.v1+json","size":10},"layers":[{"digest":"sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee","mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","size":100}]}`

func expectPodmanDeltaTarget(t *testing.T, exec *executer.MockExecuter, image string, attempts int) {
	t.Helper()
	targetIndex, _ := testApplicationTargetIndex(t, podmanDeltaTargetDigest)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://"+image).Return(targetIndex, "", 0).Times(attempts)
}

// Registry manifest digests differ from Podman's image IDs and can appear in RepoDigests.
func expectPodmanDeltaSource(exec *executer.MockExecuter, attempts int) {
	listing := "sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\t[\"quay.io/local/base@" + podmanDeltaSource + "\"]"
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "ls", "--format", client.PodmanImageDigestsListFormat).Return(listing, "", 0).Times(attempts)
}

func TestSuccessfulPullRecordsImmutableWorkloadDigest(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	target := imageRef{image: "quay.io/acme/web:latest", ociType: OCITypePodmanImage}
	targetKey := applicationImageTargetKeyFor(target, "", target.ociType)
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): targetKey}}, true))
	task := &prefetchTask{ociType: target.ociType, delta: &OCIDeltaTarget{Application: "web"}, applicationTargetKey: targetKey, applicationAttempts: map[string]deltastatus.ApplicationAttempt{"web": {Application: "web", Lifetime: lifetime, SpecKey: "spec", TargetID: target.targetID(), TargetKey: targetKey}}}
	manager := &prefetchManager{log: logger, tasks: map[imageRef]*prefetchTask{target: task}, deltaStatusStore: store, deltaAppSpecKeys: map[string]string{"web": "spec"}, deltaAppLifetimes: map[string]string{"web": lifetime}, deltaAppTargetKeys: map[string]map[string]string{"web": {target.targetID(): targetKey}}}
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", target.image).Return("sha256:immutable-config", "", 0)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "sha256:immutable-config").Return("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "", 0)
	require.NoError(manager.pullApplicationImage(context.Background(), target, task, podman, nil))
	image := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
	result := store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{image}, true)
	require.NotNil(result)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, result.Outcome)
}

func TestAlreadyPresentImageRecordsImmutableWorkloadDigest(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	const index = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	const platform = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	target := imageRef{image: "quay.io/acme/web:latest", ociType: OCITypePodmanImage}
	targetKey := applicationImageTargetKeyFor(target, platform, target.ociType)
	require.NoError(t, store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(t, store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): targetKey}}, true))
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", target.image).Return("", "", 0).Times(2)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{json .}}", target.image).Return(fmt.Sprintf(`{"Id":%q,"Digest":%q,"RepoDigests":[%q]}`, "sha256:immutable-config", platform, target.image+"@"+index), "", 0)
	manager := &prefetchManager{log: logger, tasks: make(map[imageRef]*prefetchTask), podmanFactory: func(v1beta1.Username) (*client.Podman, error) { return podman, nil }, deltaStatusStore: store, deltaAppSpecKeys: map[string]string{"web": "spec"}, deltaAppLifetimes: map[string]string{"web": lifetime}, deltaAppTargetKeys: map[string]map[string]string{"web": {target.targetID(): targetKey}}}
	queued, err := manager.prepareTask(context.Background(), target, target.ociType, platform, nil, &OCIDeltaTarget{Application: "web"}, v1beta1.PullIfNotPresent)
	require.NoError(t, err)
	require.False(t, queued)
	image := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: platform}
	result := store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{image}, true)
	require.NotNil(t, result)
	require.Equal(t, v1beta1.DeviceDeltaApplyOutcomeNotRequired, result.Outcome)
}

// collectPrefetchTaskOutcomes keeps legacy task-processing assertions distinct
// from application publication. Prefetch itself must never attach an unverified
// desired result; application collector tests cover the emitted image/result pair.
func collectPrefetchTaskOutcomes(t *testing.T, manager *prefetchManager, ctx context.Context, taskStatus *v1beta1.DeviceStatus) error {
	t.Helper()
	require := require.New(t)
	for i := range taskStatus.Applications {
		taskStatus.Applications[i].LastDelta = nil
	}
	if err := manager.Status(ctx, taskStatus); err != nil {
		return err
	}
	for _, application := range taskStatus.Applications {
		require.Nil(application.LastDelta, "prefetch must not publish a desired-only outcome")
	}
	outcomes := manager.status(ctx).DeltaApplyStatuses
	for i := range taskStatus.Applications {
		if outcome, exists := outcomes[taskStatus.Applications[i].Name]; exists {
			copied := outcome
			taskStatus.Applications[i].LastDelta = &copied
		}
	}
	return nil
}

func TestWhenPullCompletesForAReusedTaskItShouldNotInheritTheNewGeneration(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	target := imageRef{image: "quay.io/acme/web:latest", ociType: OCITypePodmanImage}
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): "old-target"}}, true))
	task := &prefetchTask{ociType: target.ociType, delta: &OCIDeltaTarget{Application: "web"}, applicationTargetKey: "old-target", applicationAttempts: map[string]deltastatus.ApplicationAttempt{"web": {Application: "web", Lifetime: lifetime, SpecKey: "spec", TargetID: target.targetID(), TargetKey: "old-target"}}}
	manager := &prefetchManager{log: logger, tasks: map[imageRef]*prefetchTask{target: task}, deltaStatusStore: store, deltaAppSpecKeys: map[string]string{"web": "spec"}, deltaAppLifetimes: map[string]string{"web": lifetime}, deltaAppTargetKeys: map[string]map[string]string{"web": {target.targetID(): "old-target"}}}
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", target.image).DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
		manager.mu.Lock()
		manager.deltaGeneration++
		task.deltaGeneration = manager.deltaGeneration
		task.applicationTargetKey = "new-target"
		manager.deltaAppTargetKeys["web"][target.targetID()] = "new-target"
		manager.mu.Unlock()
		require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): "new-target"}}, true))
		return "sha256:old-operation-config", "", 0
	})
	const observed = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", "sha256:old-operation-config").Return(observed, "", 0).AnyTimes()
	require.NoError(manager.pullApplicationImage(context.Background(), target, task, podman, nil))
	image := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: observed}
	require.Nil(store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{image}, true))
	require.Empty(store.ApplicationResults("web"))
}

func TestWhenCRIPullFindsLocalImagesItShouldRecordOnlyVerifiedPlatform(t *testing.T) {
	manifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json"}`
	platform := digest.FromString(manifest).String()
	index := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"platform":{"os":%q,"architecture":%q}}]}`, platform, runtime.GOOS, runtime.GOARCH)
	indexDigest := digest.FromString(index).String()
	for _, inspectFails := range []bool{false, true} {
		t.Run(fmt.Sprintf("When local CRI inspection fails=%t it should publish only verified membership", inspectFails), func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			logger := log.NewPrefixLogger("test")
			store := deltastatus.New(rw, "/var/lib/flightctl", logger)
			target := imageRef{image: "quay.io/acme/web:latest", ociType: OCITypeCRIImage}
			const pulledImageID = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			const targetKey = "target"
			require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
			lifetime := store.ApplicationLifetime("web")
			require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): targetKey}}, true))
			task := &prefetchTask{ociType: target.ociType, delta: &OCIDeltaTarget{Application: "web"}, applicationTargetKey: targetKey, applicationAttempts: map[string]deltastatus.ApplicationAttempt{"web": {Application: "web", Lifetime: lifetime, SpecKey: "spec", TargetID: target.targetID(), TargetKey: targetKey}}}
			cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			manager := &prefetchManager{log: logger, tasks: map[imageRef]*prefetchTask{target: task}, cliClients: client.NewCLIClients(client.WithCRIClient(cri)), skopeoFactory: func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil }, deltaStatusStore: store, deltaAppSpecKeys: map[string]string{"web": "spec"}, deltaAppLifetimes: map[string]string{"web": lifetime}, deltaAppTargetKeys: map[string]map[string]string{"web": {target.targetID(): targetKey}}}
			exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", target.image}, gomock.Any()).Return("Image is up to date for "+pulledImageID, "", 0)
			if inspectFails {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", pulledImageID).Return("", "not available", 1)
			} else {
				// Index first is deliberately not an ordered preference for the reported digest.
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", pulledImageID).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q,%q]}}`, "quay.io/acme/web@"+indexDigest, "quay.io/acme/web@"+platform), "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/web@"+indexDigest, "--no-creds").Return(index, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/web@"+platform, "--no-creds").Return(manifest, "", 0)
			}
			require.NoError(manager.pullCRIImage(context.Background(), target, task, skopeo))
			observed := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: platform}
			result := store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{observed}, true)
			if inspectFails {
				require.Nil(result)
			} else {
				require.NotNil(result)
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, result.Outcome)
			}
		})
	}
}

func TestWhenCRITagMovesAfterPullItShouldRecordThePulledImage(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	target := imageRef{image: "quay.io/acme/web:latest", ociType: OCITypeCRIImage}
	const targetKey = "target"
	const pulledImageID = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	oldManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"version":"pulled"}}`
	newManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"version":"retagged"}}`
	oldDigest := digest.FromString(oldManifest).String()
	newDigest := digest.FromString(newManifest).String()
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): targetKey}}, true))
	task := &prefetchTask{ociType: target.ociType, delta: &OCIDeltaTarget{Application: "web"}, applicationTargetKey: targetKey, applicationAttempts: map[string]deltastatus.ApplicationAttempt{"web": {Application: "web", Lifetime: lifetime, SpecKey: "spec", TargetID: target.targetID(), TargetKey: targetKey}}}
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	manager := &prefetchManager{
		log:               logger,
		tasks:             map[imageRef]*prefetchTask{target: task},
		cliClients:        client.NewCLIClients(client.WithCRIClient(cri)),
		skopeoFactory:     func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		deltaStatusStore:  store,
		deltaAppSpecKeys:  map[string]string{"web": "spec"},
		deltaAppLifetimes: map[string]string{"web": lifetime},
		deltaAppTargetKeys: map[string]map[string]string{
			"web": {target.targetID(): targetKey},
		},
	}

	exec.EXPECT().ExecuteWithContextFromDir(gomock.Any(), "", "crictl", []string{"pull", target.image}, gomock.Any()).Return("Image is up to date for "+pulledImageID, "", 0)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", pulledImageID).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, "quay.io/acme/web@"+oldDigest), "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", target.image).Return(fmt.Sprintf(`{"status":{"repoDigests":[%q]}}`, "quay.io/acme/web@"+newDigest), "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/web@"+oldDigest, "--no-creds").Return(oldManifest, "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/web@"+newDigest, "--no-creds").Return(newManifest, "", 0).AnyTimes()

	require.NoError(manager.pullCRIImage(context.Background(), target, task, skopeo))
	oldImage := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: oldDigest}
	newImage := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: newDigest}
	oldResult := store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{oldImage}, true)
	newResult := store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{newImage}, true)
	require.NotNil(oldResult)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotUsed, oldResult.Outcome)
	require.Nil(newResult)
}

func TestWhenCRICacheTagMovesItShouldWithholdUnverifiedNotRequired(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	exec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	target := imageRef{image: "quay.io/acme/web:latest", ociType: OCITypeCRIImage}
	targetKey := applicationImageTargetKeyFor(target, "", target.ociType)
	const imageID = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	oldManifest := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","annotations":{"version":"validated"}}`
	oldDigest := digest.FromString(oldManifest).String()
	require.NoError(store.ReconcileApplicationSpecs(map[string]string{"web": "spec"}))
	lifetime := store.ApplicationLifetime("web")
	require.NoError(store.ReconcileApplicationTargets(map[string]map[string]string{"web": {target.targetID(): targetKey}}, true))
	cri := client.NewCRI(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	manager := &prefetchManager{
		log:               logger,
		tasks:             make(map[imageRef]*prefetchTask),
		deltaGeneration:   1,
		cliClients:        client.NewCLIClients(client.WithCRIClient(cri)),
		podmanFactory:     func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		skopeoFactory:     func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		deltaStatusStore:  store,
		deltaAppSpecKeys:  map[string]string{"web": "spec"},
		deltaAppLifetimes: map[string]string{"web": lifetime},
		deltaAppTargetKeys: map[string]map[string]string{
			"web": {target.targetID(): targetKey},
		},
	}

	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "images", target.image).Return("IMAGE TAG ID\nweb latest abc", "", 0).Times(2)
	var targetInspections int
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", target.image).DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
		targetInspections++
		return fmt.Sprintf(`{"status":{"id":%q,"repoDigests":[%q]}}`, imageID, "quay.io/acme/web@"+oldDigest), "", 0
	}).Times(1)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", imageID).Return(fmt.Sprintf(`{"status":{"id":%q,"repoDigests":[%q]}}`, imageID, "quay.io/acme/web@"+oldDigest), "", 0).Times(1)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "skopeo", "inspect", "--raw", "docker://quay.io/acme/web@"+oldDigest, "--no-creds").Return(oldManifest, "", 0).AnyTimes()

	needsQueue, err := manager.prepareTask(context.Background(), target, OCITypeCRIImage, "", nil, &OCIDeltaTarget{Application: "web"})
	require.NoError(err)
	require.False(needsQueue)
	oldImage := deltastatus.ApplicationImage{Image: target.image, Owner: "root", OCIType: string(target.ociType), Digest: oldDigest}
	oldResult := store.ObservedApplicationResult("web", []deltastatus.ApplicationImage{oldImage}, true)
	require.Equal(1, targetInspections)
	require.Nil(oldResult)
}
