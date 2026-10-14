package applications

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/chartutil"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestKubernetesMonitorUpdateAfterRestart(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logger := log.NewPrefixLogger("test")
	monitor := newTestKubernetesMonitor(logger, executer.NewMockExecuter(ctrl), fileio.NewMockReadWriter(ctrl), "/tmp/kubeconfig")
	volumeManager, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)
	app := &application{
		id: "app", path: "/var/lib/flightctl/helm/charts/app",
		status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeHelm},
		volume: volumeManager,
	}
	require.NoError(monitor.Update(app))
	require.Same(app, monitor.apps[app.ID()])
	require.Len(monitor.actions, 1)
	require.Equal(lifecycle.ActionUpdate, monitor.actions[0].Type)
	require.Equal(app.ID(), monitor.actions[0].ID)
}

func TestKubernetesMonitor_QueueLifecycle(t *testing.T) {
	const appName = "my-helm-app"
	const kubeconfigPath = "/tmp/kubeconfig"

	type lifecycleIntent struct {
		desiredState v1beta1.ApplicationDesiredState
		restartGen   int
	}

	testCases := []struct {
		name        string
		stored      lifecycleIntent
		first       lifecycleIntent
		second      *lifecycleIntent
		setupFirst  func(*executer.MockExecuter, *fileio.MockReadWriter)
		setupSecond func(*executer.MockExecuter, *fileio.MockReadWriter)
	}{
		{
			name:   "When desiredState transitions from running to stopped it should scale workloads to 0",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			setupFirst: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				setupKubeScaleMock(mockExec, appName, kubeconfigPath, 0)
			},
		},
		{
			name:   "When desiredState is unchanged (running) it should not issue any call",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			// no mock expectation
		},
		{
			name:   "When desiredState is unchanged (stopped) it should not issue any call",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			// no mock expectation
		},
		{
			name:   "When desiredState transitions from stopped to running it should re-apply the chart",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			second: &lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			setupFirst: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				setupKubeScaleMock(mockExec, appName, kubeconfigPath, 0)
			},
			setupSecond: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				setupHelmUpgradeMock(mockExec, mockRW, appName, kubeconfigPath)
			},
		},
		{
			name:   "When restartGeneration increments it should scale to 0 then re-apply the chart",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning, restartGen: 0},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning, restartGen: 0},
			second: &lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning, restartGen: 1},
			// no action on first call (same intent)
			setupSecond: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				scaleCall := setupKubeScaleMock(mockExec, appName, kubeconfigPath, 0)
				upgradeCalls := setupHelmUpgradeMock(mockExec, mockRW, appName, kubeconfigPath)
				// setupHelmUpgradeMock already orders its own calls; chain the scale call before the first of them.
				gomock.InOrder(scaleCall, upgradeCalls[0])
			},
		},
		{
			name:   "When restartGeneration increments but desiredState is stopped it should not restart",
			stored: lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateRunning},
			first:  lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped},
			second: &lifecycleIntent{desiredState: v1beta1.ApplicationDesiredStateStopped, restartGen: 1},
			setupFirst: func(mockExec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				setupKubeScaleMock(mockExec, appName, kubeconfigPath, 0)
			},
			// no second mock: restart must not fire when desiredState is stopped
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			testLog := log.NewPrefixLogger("test")
			testLog.SetLevel(logrus.DebugLevel)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockExec := executer.NewMockExecuter(ctrl)
			mockRW := fileio.NewMockReadWriter(ctrl)

			monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

			id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
			volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
			require.NoError(err)

			// Pre-register the app with the stored (previously converged) lifecycle intent.
			monitor.apps[id] = &application{
				id:   id,
				path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
				status: &v1beta1.DeviceApplicationStatus{
					Name:    appName,
					AppType: v1beta1.AppTypeHelm,
				},
				volume:            volumeManager,
				desiredState:      tc.stored.desiredState,
				restartGeneration: tc.stored.restartGen,
			}

			// First spec sync.
			if tc.setupFirst != nil {
				tc.setupFirst(mockExec, mockRW)
			}
			monitor.QueueLifecycle(id, tc.first.desiredState, tc.first.restartGen)
			err = monitor.ExecuteActions(ctx)
			require.NoError(err)

			if tc.second == nil {
				return
			}

			// Second spec sync.
			if tc.setupSecond != nil {
				tc.setupSecond(mockExec, mockRW)
			}
			monitor.QueueLifecycle(id, tc.second.desiredState, tc.second.restartGen)
			err = monitor.ExecuteActions(ctx)
			require.NoError(err)
		})
	}
}

func TestKubernetesMonitor_StopApp_AppNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, "/tmp/kc")

	err := monitor.StopApp(ctx, "nonexistent-app-id")
	require.Error(err)
	require.ErrorIs(err, errors.ErrAppNotFound)
}

func TestKubernetesMonitor_StartApp_AppNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, "/tmp/kc")

	err := monitor.StartApp(ctx, "nonexistent-app-id")
	require.Error(err)
	require.ErrorIs(err, errors.ErrAppNotFound)
}

func TestKubernetesMonitor_RestartApp_AppNotFound(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, "/tmp/kc")

	err := monitor.RestartApp(ctx, "nonexistent-app-id", 0)
	require.Error(err)
	require.ErrorIs(err, errors.ErrAppNotFound)
}

func TestKubernetesMonitor_ExecuteActions_PostInstallStop(t *testing.T) {
	const appName = "my-helm-app"
	const kubeconfigPath = "/tmp/kubeconfig"

	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testLog := log.NewPrefixLogger("test")
	testLog.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

	id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)

	app := &application{
		id:   id,
		path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
		status: &v1beta1.DeviceApplicationStatus{
			Name:    appName,
			AppType: v1beta1.AppTypeHelm,
		},
		volume:       volumeManager,
		desiredState: v1beta1.ApplicationDesiredStateStopped,
	}
	require.NoError(monitor.Ensure(app))

	// Expect the Helm upgrade --install (structural Add action) followed by scale to 0 (post-install stop).
	setupHelmUpgradeMock(mockExec, mockRW, appName, kubeconfigPath)
	setupKubeScaleMock(mockExec, appName, kubeconfigPath, 0)

	err = monitor.ExecuteActions(ctx)
	require.NoError(err)
}

func TestKubernetesMonitor_StopApp_PropagatesHandlerError(t *testing.T) {
	const appName = "my-helm-app"
	const kubeconfigPath = "/tmp/kubeconfig"

	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

	id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)

	monitor.apps[id] = &application{
		id:   id,
		path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
		status: &v1beta1.DeviceApplicationStatus{
			Name:    appName,
			AppType: v1beta1.AppTypeHelm,
		},
		volume:       volumeManager,
		desiredState: v1beta1.ApplicationDesiredStateRunning,
	}

	appID := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	namespace := chartutil.AppNamespace(nil, appName)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"scale", "deployment,statefulset",
		"-l", fmt.Sprintf("%s=%s", helm.AppLabelKey, appID),
		"--replicas=0",
		"-n", namespace,
		"--kubeconfig", kubeconfigPath,
	}).Return("", "error: scale failed", 1)

	err = monitor.StopApp(ctx, id)
	require.Error(err)
	require.Contains(err.Error(), "scale workloads")
}

// TestKubernetesMonitor_StopApp_UsesHelmSpecNamespace guards the QE regression where
// lifecycleDispatch omitted ActionSpec, so Stop scaled in flightctl-<name> instead of
// the chart namespace (e.g. test-app) and kubectl reported "no objects passed to scale".
func TestKubernetesMonitor_StopApp_UsesHelmSpecNamespace(t *testing.T) {
	const appName = "helm-demo"
	const namespace = "test-app"
	const kubeconfigPath = "/tmp/kubeconfig"

	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)
	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

	appID := fmt.Sprintf("%s_%s", namespace, appName)
	volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)

	monitor.apps[appID] = &application{
		id:   appID,
		path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
		status: &v1beta1.DeviceApplicationStatus{
			Name:    appName,
			AppType: v1beta1.AppTypeHelm,
		},
		volume:       volumeManager,
		desiredState: v1beta1.ApplicationDesiredStateRunning,
		actionSpec: lifecycle.HelmSpec{
			Namespace: namespace,
		},
	}

	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"scale", "deployment,statefulset",
		"-l", fmt.Sprintf("%s=%s", helm.AppLabelKey, appID),
		"--replicas=0",
		"-n", namespace,
		"--kubeconfig", kubeconfigPath,
	}).Return("", "", 0)

	require.NoError(monitor.StopApp(ctx, appID))
}

// Execute must use the Spec captured at QueueLifecycle time, even if the tracked
// app's ActionSpec changes before ExecuteActions runs.
func TestKubernetesMonitor_QueueLifecycle_ExecuteUsesQueuedSpec(t *testing.T) {
	const appName = "helm-demo"
	const queuedNamespace = "test-app"
	const mutatedNamespace = "wrong-ns"
	const kubeconfigPath = "/tmp/kubeconfig"

	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)
	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

	appID := fmt.Sprintf("%s_%s", queuedNamespace, appName)
	volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)

	app := &application{
		id:   appID,
		path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
		status: &v1beta1.DeviceApplicationStatus{
			Name:    appName,
			AppType: v1beta1.AppTypeHelm,
		},
		volume:       volumeManager,
		desiredState: v1beta1.ApplicationDesiredStateRunning,
		actionSpec: lifecycle.HelmSpec{
			Namespace: queuedNamespace,
		},
	}
	monitor.apps[appID] = app

	monitor.QueueLifecycle(appID, v1beta1.ApplicationDesiredStateStopped, 0)

	app.actionSpec = lifecycle.HelmSpec{Namespace: mutatedNamespace}

	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"scale", "deployment,statefulset",
		"-l", fmt.Sprintf("%s=%s", helm.AppLabelKey, appID),
		"--replicas=0",
		"-n", queuedNamespace,
		"--kubeconfig", kubeconfigPath,
	}).Return("", "", 0)

	require.NoError(monitor.ExecuteActions(ctx))
	require.Equal(v1beta1.ApplicationDesiredStateStopped, app.DesiredState())
}

func TestKubernetesMonitor_StartApp_PropagatesHandlerError(t *testing.T) {
	const appName = "my-helm-app"
	const kubeconfigPath = "/tmp/kubeconfig"

	require := require.New(t)
	ctx := context.Background()
	testLog := log.NewPrefixLogger("test")
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

	id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)

	monitor.apps[id] = &application{
		id:   id,
		path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
		status: &v1beta1.DeviceApplicationStatus{
			Name:    appName,
			AppType: v1beta1.AppTypeHelm,
		},
		volume:       volumeManager,
		desiredState: v1beta1.ApplicationDesiredStateStopped,
	}

	// Make helm version fail so that setupHelmVersionConfig (and thus Start) returns an error.
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
		"version", "--short",
	}).Return("", "helm not found", 1)

	err = monitor.StartApp(ctx, id)
	require.Error(err)
}

func TestKubernetesMonitor_ExecuteActions_LifecycleActionFailureContinues(t *testing.T) {
	// Verifies that ExecuteActions logs a warning but does NOT return an error
	// when a lifecycle action (Stop) fails; subsequent actions still execute.
	const appName = "my-helm-app"
	const kubeconfigPath = "/tmp/kubeconfig"

	require := require.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	testLog := log.NewPrefixLogger("test")
	testLog.SetLevel(logrus.DebugLevel)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	mockExec := executer.NewMockExecuter(ctrl)
	mockRW := fileio.NewMockReadWriter(ctrl)

	monitor := newTestKubernetesMonitor(testLog, mockExec, mockRW, kubeconfigPath)

	id := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	volumeManager, err := provider.NewVolumeManager(testLog, appName, v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)

	monitor.apps[id] = &application{
		id:   id,
		path: fmt.Sprintf("/var/lib/flightctl/helm/charts/%s", appName),
		status: &v1beta1.DeviceApplicationStatus{
			Name:    appName,
			AppType: v1beta1.AppTypeHelm,
		},
		volume:       volumeManager,
		desiredState: v1beta1.ApplicationDesiredStateRunning,
	}

	// Queue a Stop action; make kubectl scale fail.
	appID := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	namespace := chartutil.AppNamespace(nil, appName)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"scale", "deployment,statefulset",
		"-l", fmt.Sprintf("%s=%s", helm.AppLabelKey, appID),
		"--replicas=0",
		"-n", namespace,
		"--kubeconfig", kubeconfigPath,
	}).Return("", "error: scale failed", 1)

	monitor.QueueLifecycle(id, v1beta1.ApplicationDesiredStateStopped, 0)
	err = monitor.ExecuteActions(ctx)
	// ExecuteActions must return nil even though StopApp failed — it logs a warning.
	require.NoError(err)
}
func newTestKubernetesMonitor(log *log.PrefixLogger, mockExec executer.Executer, mockRW fileio.ReadWriter, kubeconfigPath string) *KubernetesMonitor {
	kubeClient := client.NewKube(log, mockExec, mockRW,
		client.WithBinary("kubectl"),
		client.WithKubeconfigPath(kubeconfigPath),
	)
	helmClient := client.NewHelm(log, mockExec, mockRW, "/var/lib/flightctl", testutil.NewPollConfig())
	cliClients := &testKubeCLIClients{kube: kubeClient, helm: helmClient}
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) { return mockRW, nil }
	return NewKubernetesMonitor(log, cliClients, rwFactory, nil)
}

func TestKubernetesMonitorUpdatePodStatusCollectsImageSnapshot(t *testing.T) {
	app := &application{workloads: []Workload{{
		ID:   "old-uid",
		Name: "app-pod",
		Images: []WorkloadImage{{
			Image: "quay.io/acme/old:v1",
		}},
	}}}
	monitor := &KubernetesMonitor{monitor: &monitor{log: log.NewPrefixLogger("test")}, clients: &testKubeCLIClients{}}
	pod := &kubernetesPod{}
	pod.Kind = "Pod"
	pod.Metadata.Name = "app-pod"
	pod.Metadata.UID = "new-uid"
	pod.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: "quay.io/acme/web:latest"}}
	pod.Spec.InitContainers = []kubernetesContainerSpec{{Name: "setup", Image: "quay.io/acme/setup:v2"}}
	pod.Status.Phase = podPhaseRunning
	pod.Status.ContainerStatuses = []kubernetesContainerStatus{{
		Name:    "web",
		ImageID: "docker-pullable://quay.io/acme/web@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Ready:   true,
	}}
	pod.Status.InitContainerStatuses = []kubernetesContainerStatus{{
		Name:    "setup",
		ImageID: "containerd://sha256:opaque",
		Ready:   true,
	}}

	monitor.updatePodStatus(t.Context(), app, pod)

	workload, ok := app.Workload("app-pod")
	require.True(t, ok)
	require.Equal(t, "new-uid", workload.ID)
	require.Equal(t, []WorkloadImage{
		{Image: "quay.io/acme/web:latest", Digest: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{Image: "quay.io/acme/setup:v2"},
	}, workload.Images)

	// A later pod snapshot replaces the old set so removed containers cannot
	// leave image references in the application status.
	pod.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: "quay.io/acme/web:v2"}}
	pod.Spec.InitContainers = nil
	pod.Status.InitContainerStatuses = nil
	pod.Status.ContainerStatuses = []kubernetesContainerStatus{{Name: "web", Ready: true}}
	monitor.updatePodStatus(t.Context(), app, pod)

	workload, ok = app.Workload("app-pod")
	require.True(t, ok)
	require.Equal(t, []WorkloadImage{{Image: "quay.io/acme/web:v2"}}, workload.Images)
}

// testKubeCLIClients implements client.CLIClients for KubernetesMonitor tests.
type testKubeCLIClients struct {
	kube *client.Kube
	helm *client.Helm
	cri  *client.CRI
}

func (c *testKubeCLIClients) Podman() *client.Podman { return nil }
func (c *testKubeCLIClients) Skopeo() *client.Skopeo { return nil }
func (c *testKubeCLIClients) Kube() *client.Kube     { return c.kube }
func (c *testKubeCLIClients) Helm() *client.Helm     { return c.helm }
func (c *testKubeCLIClients) CRI() *client.CRI       { return c.cri }

// setupKubeScaleMock sets up the mock expectation for kubectl scale --replicas=N.
func setupKubeScaleMock(mockExec *executer.MockExecuter, appName, kubeconfigPath string, replicas int) *gomock.Call {
	appID := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	namespace := chartutil.AppNamespace(nil, appName)
	return mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"scale", "deployment,statefulset",
		"-l", fmt.Sprintf("%s=%s", helm.AppLabelKey, appID),
		fmt.Sprintf("--replicas=%d", replicas),
		"-n", namespace,
		"--kubeconfig", kubeconfigPath,
	}).Return("", "", 0)
}

// setupHelmUpgradeMock sets up mock expectations for the helm upgrade --install chain and
// returns the calls in order so callers can chain them with preceding/following expectations.
// Uses gomock.Any() for the helm args to avoid depending on the OSExecutableResolver path.
func setupHelmUpgradeMock(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter, appName, kubeconfigPath string) []*gomock.Call {
	namespace := chartutil.AppNamespace(nil, appName)
	versionCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", gomock.Any()).Return("v3.14.0", "", 0)
	getNamespaceCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"get", "namespace", namespace, "--kubeconfig", kubeconfigPath,
	}).Return(namespace+"   Active   1d", "", 0)
	pathExistsCall := mockRW.EXPECT().PathExists(kubeconfigPath).Return(true, nil)
	upgradeCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", gomock.Any()).Return("", "", 0)

	gomock.InOrder(versionCall, getNamespaceCall, pathExistsCall, upgradeCall)
	return []*gomock.Call{versionCall, getNamespaceCall, pathExistsCall, upgradeCall}
}

func TestKubernetesMonitorReportsCRIObservedDigest(t *testing.T) {
	const index = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const observed = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const other = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	cases := []struct {
		name          string
		image         string
		imageID       string
		repoDigests   []string
		exitCode      int
		want          string
		initContainer bool
		inspect       bool
		inlineConfig  bool
		repeatEvent   bool
		inspectCalls  int
	}{
		{name: "When containerd reports an opaque ID it should report the observed platform digest instead of the desired index", image: "quay.io/acme/web@" + index, imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/other/web@" + other, "quay.io/acme/web@" + observed}, want: observed, inspect: true},
		{name: "When inline CRI configuration differs from disk it should inspect the configured runtime", image: "quay.io/acme/web:latest", imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/acme/web@" + observed}, want: observed, inspect: true, inlineConfig: true},
		{name: "When CRI-O reports an opaque ID it should resolve the tagged image from CRI", image: "quay.io/acme/web:latest", imageID: "sha256:config", repoDigests: []string{"quay.io/acme/web@" + observed}, want: observed, inspect: true},
		{name: "When an init container has an opaque ID it should report its CRI registry digest", image: "quay.io/acme/web:latest", imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/acme/web@" + observed}, want: observed, inspect: true, initContainer: true},
		{name: "When CRI inspection fails it should withhold the unverified immutable reference", image: "quay.io/acme/web@" + index, imageID: "containerd://sha256:config", exitCode: 1, inspect: true},
		{name: "When CRI retains multiple registry objects it should withhold the ambiguous observation and reuse it on later pod events", image: "quay.io/acme/web@" + index, imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/acme/web@" + index, "quay.io/acme/web@" + observed}, inspect: true, repeatEvent: true, inspectCalls: 1},
		{name: "When CRI exposes no matching registry digest it should keep an opaque ID out of status", image: "quay.io/acme/web:latest", imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/other/web@" + other}, inspect: true},
		{name: "When the pod reports a pullable platform digest it should retain that observation", image: "quay.io/acme/web@" + index, imageID: "docker-pullable://quay.io/acme/web@" + observed, want: observed},
		{name: "When the pod reports a verified index identity it should retain that observation", image: "quay.io/acme/web:latest", imageID: "docker-pullable://quay.io/acme/web@" + index, want: index},
		{name: "When CRI exposes duplicate repository digests it should report their single identity", image: "quay.io/acme/web:latest", imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/acme/web@" + observed, "quay.io/acme/web@" + observed}, want: observed, inspect: true},
		{name: "When the pod reports a platform-pinned observed image it should retain the platform digest", image: "quay.io/acme/web@" + observed, imageID: "quay.io/acme/web@" + observed, want: observed},
		{name: "When a pullable runtime identity refers to another repository it should remain unknown", image: "quay.io/acme/web@" + index, imageID: "docker-pullable://quay.io/other/web@" + other},
		{name: "When an immutable image has no runtime identity it should remain unknown", image: "quay.io/acme/web@" + index},
		{name: "When an immutable image has no matching CRI registry digest it should remain unknown", image: "quay.io/acme/web@" + index, imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/other/web@" + other}, inspect: true},
		{name: "When the pod has no runtime image ID it should retain an unknown tagged digest", image: "quay.io/acme/web:latest"},
		{name: "When CRI inspection succeeds it should reuse repo digests for repeated pod events", image: "quay.io/acme/web:latest", imageID: "containerd://sha256:config", repoDigests: []string{"quay.io/acme/web@" + observed}, want: observed, inspect: true, repeatEvent: true, inspectCalls: 1},
		{name: "When CRI inspection fails it should retry on a later pod event", image: "quay.io/acme/web:latest", imageID: "containerd://sha256:config", exitCode: 1, inspect: true, repeatEvent: true, inspectCalls: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			logger := log.NewPrefixLogger("test")
			exec := executer.NewMockExecuter(ctrl)
			rwFactory := fileio.NewReadWriterFactory(t.TempDir())
			rw, err := rwFactory(v1beta1.CurrentProcessUsername)
			require.NoError(err)
			var resolver dependency.PullConfigResolver
			args := []any{"inspecti", "--output", "json", "sha256:config"}
			if tc.inlineConfig {
				var config v1beta1.ConfigProviderSpec
				require.NoError(config.FromInlineConfigProviderSpec(v1beta1.InlineConfigProviderSpec{Name: "cri", Inline: []v1beta1.FileSpec{{Path: "/etc/crictl.yaml", Content: "runtime-endpoint: unix:///run/custom/cri.sock"}}}))
				resolver = dependency.NewPullConfigResolver(logger, rwFactory)
				resolver.BeforeUpdate(&v1beta1.DeviceSpec{Config: &[]v1beta1.ConfigProviderSpec{config}})
				defer resolver.Cleanup()
				var resolved string
				resolver.Options(dependency.PullConfigSpec{Paths: []string{"/etc/crictl.yaml"}, OptionFn: func(path string) client.ClientOption {
					resolved = path
					return client.WithCRIConfig(path)
				}})()
				require.NotEmpty(resolved)
				args = append([]any{"--config", resolved}, args...)
			}
			if tc.inspect {
				response, err := json.Marshal(map[string]any{"status": map[string]any{"id": "sha256:config", "repoDigests": tc.repoDigests}})
				require.NoError(err)
				inspectCalls := tc.inspectCalls
				if inspectCalls == 0 {
					inspectCalls = 1
				}
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", args...).Return(string(response), "inspection failed", tc.exitCode).Times(inspectCalls)
			}
			monitor := NewKubernetesMonitor(logger, &testKubeCLIClients{cri: client.NewCRI(logger, exec, rw, testutil.NewPollConfig())}, rwFactory, resolver)
			volume, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
			require.NoError(err)
			app := &application{id: "app", volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeHelm}}
			require.NoError(monitor.Ensure(app))
			event := kubernetesWatchEvent{Type: "ADDED"}
			event.Object.Kind = "Pod"
			event.Object.Metadata.Name = "web"
			event.Object.Metadata.UID = "pod"
			event.Object.Metadata.Labels = map[string]string{helm.AppLabelKey: "app"}
			container := []kubernetesContainerSpec{{Name: "web", Image: tc.image}}
			status := []kubernetesContainerStatus{{Name: "web", ImageID: tc.imageID, Ready: true}}
			if tc.initContainer {
				event.Object.Spec.InitContainers = container
				event.Object.Status.InitContainerStatuses = status
			} else {
				event.Object.Spec.Containers = container
				event.Object.Status.ContainerStatuses = status
			}
			data, err := json.Marshal(event)
			require.NoError(err)
			monitor.HandleEvent(t.Context(), data)
			if tc.repeatEvent {
				event.Type = "MODIFIED"
				repeatedData, err := json.Marshal(event)
				require.NoError(err)
				monitor.HandleEvent(t.Context(), repeatedData)
			}
			results, err := monitor.Status()
			require.NoError(err)
			require.Len(results, 1)
			require.NotNil(results[0].Status.ImageDigests)
			require.Equal([]v1beta1.ApplicationImageDigest{{Image: tc.image, Digest: tc.want}}, *results[0].Status.ImageDigests)
		})
	}
}

func TestKubernetesMonitorReinspectsCRIDigestsForNewRepository(t *testing.T) {
	const imageID = "containerd://sha256:config"
	const firstDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const secondDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	cases := []struct {
		name              string
		firstRepoDigests  []string
		secondRepoDigests []string
	}{
		{
			name:              "When a successful inspection returns no repo digests it should retry for a new repository",
			firstRepoDigests:  nil,
			secondRepoDigests: []string{"quay.io/second/web@" + secondDigest},
		},
		{
			name:              "When cached repo digests do not match the requested repository it should inspect again",
			firstRepoDigests:  []string{"quay.io/first/web@" + firstDigest},
			secondRepoDigests: []string{"quay.io/second/web@" + secondDigest},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			logger := log.NewPrefixLogger("test")
			exec := executer.NewMockExecuter(ctrl)
			rwFactory := fileio.NewReadWriterFactory(t.TempDir())
			rw, err := rwFactory(v1beta1.CurrentProcessUsername)
			require.NoError(err)
			firstResponse, err := json.Marshal(map[string]any{"status": map[string]any{"repoDigests": tc.firstRepoDigests}})
			require.NoError(err)
			secondResponse, err := json.Marshal(map[string]any{"status": map[string]any{"repoDigests": tc.secondRepoDigests}})
			require.NoError(err)
			gomock.InOrder(
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", "sha256:config").
					Return(string(firstResponse), "", 0),
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", "sha256:config").
					Return(string(secondResponse), "", 0),
			)

			monitor := NewKubernetesMonitor(logger, &testKubeCLIClients{cri: client.NewCRI(logger, exec, rw, testutil.NewPollConfig())}, rwFactory, nil)
			volume, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
			require.NoError(err)
			app := &application{id: "app", volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeHelm}}
			require.NoError(monitor.Ensure(app))
			observe := func(image, eventType string) {
				event := kubernetesWatchEvent{Type: eventType}
				event.Object.Kind = "Pod"
				event.Object.Metadata.Name = "web"
				event.Object.Metadata.UID = "pod"
				event.Object.Metadata.Labels = map[string]string{helm.AppLabelKey: "app"}
				event.Object.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: image}}
				event.Object.Status.ContainerStatuses = []kubernetesContainerStatus{{
					Name: "web", ImageID: imageID, Ready: true,
				}}
				data, err := json.Marshal(event)
				require.NoError(err)
				monitor.HandleEvent(t.Context(), data)
			}
			observe("quay.io/first/web:latest", "ADDED")
			observe("quay.io/second/web:latest", "MODIFIED")

			results, err := monitor.Status()
			require.NoError(err)
			require.Len(results, 1)
			require.Equal([]v1beta1.ApplicationImageDigest{{Image: "quay.io/second/web:latest", Digest: secondDigest}}, *results[0].Status.ImageDigests)
		})
	}
}

func TestKubernetesMonitorBoundsCRIImageDigestLookups(t *testing.T) {
	const image = "quay.io/acme/web:latest"
	const observed = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	logger := log.NewPrefixLogger("test")
	exec := executer.NewMockExecuter(ctrl)
	rwFactory := fileio.NewReadWriterFactory(t.TempDir())
	rw, err := rwFactory(v1beta1.CurrentProcessUsername)
	require.NoError(err)
	response, err := json.Marshal(map[string]any{"status": map[string]any{"repoDigests": []string{"quay.io/acme/web@" + observed}}})
	require.NoError(err)
	for i := 0; i <= maxCachedCRIDigestLookups; i++ {
		runtimeID := fmt.Sprintf("sha256:config-%d", i)
		calls := 1
		if i == 0 {
			calls = 2
		}
		exec.EXPECT().ExecuteWithContext(gomock.Any(), "crictl", "inspecti", "--output", "json", runtimeID).
			Return(string(response), "", 0).Times(calls)
	}

	monitor := NewKubernetesMonitor(logger, &testKubeCLIClients{cri: client.NewCRI(logger, exec, rw, testutil.NewPollConfig())}, rwFactory, nil)
	volume, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)
	app := &application{id: "app", volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeHelm}}
	require.NoError(monitor.Ensure(app))
	observe := func(index int, eventType string) {
		event := kubernetesWatchEvent{Type: eventType}
		event.Object.Kind = "Pod"
		event.Object.Metadata.Name = fmt.Sprintf("web-%d", index)
		event.Object.Metadata.UID = fmt.Sprintf("pod-%d", index)
		event.Object.Metadata.Labels = map[string]string{helm.AppLabelKey: "app"}
		event.Object.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: image}}
		event.Object.Status.ContainerStatuses = []kubernetesContainerStatus{{
			Name: "web", ImageID: fmt.Sprintf("containerd://sha256:config-%d", index), Ready: true,
		}}
		data, err := json.Marshal(event)
		require.NoError(err)
		monitor.HandleEvent(t.Context(), data)
	}
	for i := 0; i <= maxCachedCRIDigestLookups; i++ {
		observe(i, "ADDED")
	}
	observe(0, "MODIFIED")

	results, err := monitor.Status()
	require.NoError(err)
	require.Len(results, 1)
	require.Equal([]v1beta1.ApplicationImageDigest{{Image: image, Digest: observed}}, *results[0].Status.ImageDigests)
}

func TestKubernetesMonitorReportsMixedWorkloadImageSnapshot(t *testing.T) {
	require := require.New(t)
	logger := log.NewPrefixLogger("test")
	monitor := NewKubernetesMonitor(logger, &testKubeCLIClients{}, fileio.NewReadWriterFactory(t.TempDir()), nil)
	volume, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeHelm, v1beta1.CurrentProcessUsername, nil)
	require.NoError(err)
	app := &application{id: "app", volume: volume, status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeHelm}}
	require.NoError(monitor.Ensure(app))
	const image = "quay.io/acme/web:latest"
	const oldDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const newDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, pod := range []struct{ name, digest string }{{"old-pod", oldDigest}, {"new-pod", newDigest}} {
		event := kubernetesWatchEvent{Type: "ADDED"}
		event.Object.Kind = "Pod"
		event.Object.Metadata.Name = pod.name
		event.Object.Metadata.UID = pod.name
		event.Object.Metadata.Labels = map[string]string{helm.AppLabelKey: "app"}
		event.Object.Spec.Containers = []kubernetesContainerSpec{{Name: "web", Image: image}}
		event.Object.Status.ContainerStatuses = []kubernetesContainerStatus{{Name: "web", ImageID: image + "@" + pod.digest, Ready: true}}
		data, err := json.Marshal(event)
		require.NoError(err)
		monitor.HandleEvent(t.Context(), data)
	}
	results, err := monitor.Status()
	require.NoError(err)
	require.Len(results, 1)
	require.NotNil(results[0].Status.ImageDigests)
	require.Equal([]v1beta1.ApplicationImageDigest{{Image: image, Digest: oldDigest}, {Image: image, Digest: newDigest}}, *results[0].Status.ImageDigests)
}
