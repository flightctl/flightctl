package dependency

import (
	"context"
	"path/filepath"
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
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestWhenCachedArtifactsHaveHintsItShouldReportFallback(t *testing.T) {
	const image = "quay.io/acme/compose:target"
	const hint = "quay.io/acme/delta:target"
	tests := []struct {
		name          string
		ociType       OCIType
		pullPolicy    v1beta1.ImagePullPolicy
		packageTarget bool
		setupMocks    func(*executer.MockExecuter)
	}{
		{
			name:    "When a cached artifact has a delta hint it should report Fallback without pulling the artifact",
			ociType: OCITypePodmanArtifact,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0).Times(3)
			},
		},
		{
			name:    "When a cached auto detected artifact has a delta hint it should report Fallback without pulling the artifact",
			ociType: OCITypeAuto,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0).Times(3)
			},
		},
		{
			name:       "When a cached hinted artifact uses Always it should refresh through the full artifact path",
			ociType:    OCITypePodmanArtifact,
			pullPolicy: v1beta1.PullAlways,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0).Times(2)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "pull", image).Return("", "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0)
			},
		},
		{
			name:       "When a cached hinted artifact uses Never it should report Fallback and reuse the artifact",
			ociType:    OCITypePodmanArtifact,
			pullPolicy: v1beta1.PullNever,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0).Times(3)
			},
		},
		{
			name:          "When a Compose package has a hint it should report NotRequired without delta processing",
			ociType:       OCITypePodmanArtifact,
			packageTarget: true,
			setupMocks: func(exec *executer.MockExecuter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).Return("{}", "", 0).AnyTimes()
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			tt.setupMocks(exec)
			pullPolicy := tt.pullPolicy
			if pullPolicy == "" {
				pullPolicy = v1beta1.PullIfNotPresent
			}
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).AnyTimes()
			resources := resource.NewMockManager(ctrl)
			resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
			logger := log.NewPrefixLogger("test")
			root := t.TempDir()
			rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
			skopeo := client.NewSkopeo(logger, exec, rw)
			manager := NewPrefetchManager(logger,
				func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
				func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
				client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{})
			defer manager.Cleanup()
			manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
				return &OCICollection{Targets: OCIPullTargetsByUser{"": {{Type: tt.ociType, Reference: image, PullPolicy: pullPolicy, Delta: &OCIDeltaTarget{Hint: hint, Application: "compose", Package: tt.packageTarget}}}}}, nil
			}))
			err := manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{})
			if tt.packageTarget {
				require.NoError(err)
			} else {
				require.ErrorIs(err, errors.ErrPrefetchNotReady)
			}
			stopWorker := startPrefetchWorker(t, manager)
			defer stopWorker()
			require.Eventually(func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond)
			status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "compose"}}}
			require.NoError(collectPrefetchTaskOutcomes(t, manager, t.Context(), status))
			require.NotNil(status.Applications[0].LastDelta)
			if tt.packageTarget {
				require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, status.Applications[0].LastDelta.Outcome)
				require.Nil(status.Applications[0].LastDelta.FallbackReason)
				return
			}
			require.Equal(v1beta1.DeviceDeltaApplyOutcomeFallback, status.Applications[0].LastDelta.Outcome)
			require.NotNil(status.Applications[0].LastDelta.FallbackReason)
			require.Contains(*status.Applications[0].LastDelta.FallbackReason, "not supported for OCI artifacts")
		})
	}
}

func TestWhenAHintedArtifactWasInitiallyAbsentItShouldReuseTheCacheAcrossUpdates(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const image = "quay.io/acme/compose:target"
	const artifactDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	var present atomic.Bool
	exec := executer.NewMockExecuter(ctrl)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "--version").Return("podman version 5.5.0", "", 0).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "inspect", image).
		DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
			if !present.Load() {
				return "", "artifact not found", 1
			}
			return `{"Digest":"` + artifactDigest + `"}`, "", 0
		}).AnyTimes()
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "artifact", "pull", image).
		DoAndReturn(func(context.Context, string, ...string) (string, string, int) {
			if present.Swap(true) {
				return "", "registry unavailable after the initial download", 1
			}
			return "", "", 0
		}).AnyTimes()
	logger := log.NewPrefixLogger("test")
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	store := deltastatus.New(rw, "/var/lib/flightctl", logger)
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
	manager := NewPrefetchManager(logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		nil, client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{}, WithDeltaStatusStore(store))
	defer manager.Cleanup()
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{"": {{
			Type: OCITypePodmanArtifact, Reference: image, PullPolicy: v1beta1.PullIfNotPresent,
			Delta: &OCIDeltaTarget{Hint: "quay.io/acme/delta:target", Application: "compose", Package: true},
		}}}}, nil
	}))
	makeApplication := func(name string) v1beta1.ApplicationProviderSpec {
		app := v1beta1.ComposeApplication{Name: &name, AppType: v1beta1.AppTypeCompose}
		require.NoError(app.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}))
		var spec v1beta1.ApplicationProviderSpec
		require.NoError(spec.FromComposeApplication(app))
		return spec
	}
	app := makeApplication("compose")
	desired := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{app}}
	require.ErrorIs(manager.BeforeUpdate(t.Context(), nil, desired), errors.ErrPrefetchNotReady)
	stopWorker := startPrefetchWorker(t, manager)
	defer stopWorker()
	require.Eventually(func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond)

	next := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{app, makeApplication("unrelated")}}
	require.NoError(manager.BeforeUpdate(t.Context(), desired, next))
	require.Eventually(func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond,
		"an unrelated update must succeed with the downloaded artifact while the registry is unavailable")
	status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "compose"}}}
	require.NoError(collectPrefetchTaskOutcomes(t, manager, t.Context(), status))
	require.NotNil(status.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, status.Applications[0].LastDelta.Outcome)
	require.Nil(status.Applications[0].LastDelta.FallbackReason)
	require.Empty(store.ApplicationSnapshot("compose").Targets, "packages must not increase workload history capacity")
	require.Empty(store.ApplicationSnapshot("compose").Results)
	require.Nil(store.ObservedApplicationResult("compose", []deltastatus.ApplicationImage{{
		Image: image, Owner: string(v1beta1.RootUsername), OCIType: string(OCITypePodmanArtifact), Digest: artifactDigest,
	}}, true), "packages must not retain pending workload history")
}

func startPrefetchWorker(t *testing.T, manager *prefetchManager) func() {
	t.Helper()
	workerCtx, cancelWorker := context.WithCancel(t.Context())
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		manager.Run(workerCtx)
	}()
	return func() {
		cancelWorker()
		<-workerDone
	}
}

func TestWhenAHelmPackageHasAHintItShouldResolveNormallyAndReportNotRequired(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const chart = "oci://quay.io/acme/charts/workload:1.0.0"
	logger := log.NewPrefixLogger("test")
	exec := executer.NewMockExecuter(ctrl)
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	chartDir := "/var/lib/flightctl/helm/charts/" + client.SanitizeChartRef(chart)
	require.NoError(rw.MkdirAll(chartDir, fileio.DefaultDirectoryPermissions))
	require.NoError(rw.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: workload\nversion: 1.0.0\n"), fileio.DefaultFilePermissions))
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", "dependency", "update", chartDir).Return("", "", 0)
	helm := client.NewHelm(logger, exec, rw, "/var/lib/flightctl", poll.NewConfig(time.Millisecond, 1))
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
	manager := NewPrefetchManager(logger, func(v1beta1.Username) (*client.Podman, error) { return nil, nil }, nil,
		client.NewCLIClients(client.WithHelmClient(helm)), rw, util.Duration(time.Second), resources, poll.Config{})
	defer manager.Cleanup()
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{"": {{Type: OCITypeHelmChart, Reference: chart,
			Delta: &OCIDeltaTarget{Application: "helm-app", Hint: "quay.io/acme/delta:ignored", Package: true}}}}}, nil
	}))
	require.ErrorIs(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}), errors.ErrPrefetchNotReady)
	stopWorker := startPrefetchWorker(t, manager)
	defer stopWorker()
	require.Eventually(func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond)
	resolved, err := helm.IsResolved(chart)
	require.NoError(err)
	require.True(resolved)
	status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "helm-app"}}}
	require.NoError(collectPrefetchTaskOutcomes(t, manager, t.Context(), status))
	require.NotNil(status.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, status.Applications[0].LastDelta.Outcome)
	require.Nil(status.Applications[0].LastDelta.FallbackReason)
}

func TestWhenComposePackageUsesAnImageItShouldPullWithoutDeltaAndReportNotRequired(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	const image = "quay.io/acme/compose-image:v1"
	root := t.TempDir()
	rw := fileio.NewReadWriter(fileio.NewReader(fileio.WithReaderRootDir(root)), fileio.NewWriter(fileio.WithWriterRootDir(root)))
	logger := log.NewPrefixLogger("test")
	exec := executer.NewMockExecuter(ctrl)
	gomock.InOrder(
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 1),
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "exists", image).Return("", "", 0).AnyTimes(),
	)
	exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "pull", image).Return("immutable-package", "", 0)
	podman := client.NewPodman(logger, exec, rw, poll.NewConfig(time.Millisecond, 2))
	skopeo := client.NewSkopeo(logger, exec, rw)
	resources := resource.NewMockManager(ctrl)
	resources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).AnyTimes()
	manager := NewPrefetchManager(logger,
		func(v1beta1.Username) (*client.Podman, error) { return podman, nil },
		func(v1beta1.Username) (*client.Skopeo, error) { return skopeo, nil },
		client.NewCLIClients(), rw, util.Duration(time.Second), resources, poll.Config{})
	defer manager.Cleanup()
	manager.RegisterOCICollector(newTestOCICollector(func(context.Context, *v1beta1.DeviceSpec, *v1beta1.DeviceSpec, ...OCICollectOpt) (*OCICollection, error) {
		return &OCICollection{Targets: OCIPullTargetsByUser{"": {{Type: OCITypePodmanImage, Reference: image,
			Delta: &OCIDeltaTarget{Application: "compose", Hint: "quay.io/acme/delta:ignored", Package: true}}}}}, nil
	}))
	require.ErrorIs(manager.BeforeUpdate(t.Context(), nil, &v1beta1.DeviceSpec{}), errors.ErrPrefetchNotReady)
	stopWorker := startPrefetchWorker(t, manager)
	defer stopWorker()
	require.Eventually(func() bool { return manager.IsReady(t.Context()) }, time.Second, time.Millisecond)
	status := &v1beta1.DeviceStatus{Applications: []v1beta1.DeviceApplicationStatus{{Name: "compose"}}}
	require.NoError(collectPrefetchTaskOutcomes(t, manager, t.Context(), status))
	require.NotNil(status.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, status.Applications[0].LastDelta.Outcome)
	require.Nil(status.Applications[0].LastDelta.FallbackReason)
	other := v1beta1.ComposeApplication{Name: new("other"), AppType: v1beta1.AppTypeCompose}
	require.NoError(other.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: "quay.io/acme/other:v1"}))
	var otherSpec v1beta1.ApplicationProviderSpec
	require.NoError(otherSpec.FromComposeApplication(other))
	next := &v1beta1.DeviceSpec{Applications: &[]v1beta1.ApplicationProviderSpec{otherSpec}}
	require.NoError(manager.BeforeUpdate(t.Context(), &v1beta1.DeviceSpec{}, next))
	require.NoError(collectPrefetchTaskOutcomes(t, manager, t.Context(), status))
	require.NotNil(status.Applications[0].LastDelta)
	require.Equal(v1beta1.DeviceDeltaApplyOutcomeNotRequired, status.Applications[0].LastDelta.Outcome)

}
