package applications

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/lifecycle"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

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
	namespace := helm.AppNamespace(nil, appName)
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
	namespace := helm.AppNamespace(nil, appName)
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
	return NewKubernetesMonitor(log, cliClients, rwFactory)
}

// testKubeCLIClients implements client.CLIClients for KubernetesMonitor tests.
type testKubeCLIClients struct {
	kube *client.Kube
	helm *client.Helm
}

func (c *testKubeCLIClients) Podman() *client.Podman { return nil }
func (c *testKubeCLIClients) Skopeo() *client.Skopeo { return nil }
func (c *testKubeCLIClients) Kube() *client.Kube     { return c.kube }
func (c *testKubeCLIClients) Helm() *client.Helm     { return c.helm }
func (c *testKubeCLIClients) CRI() *client.CRI       { return nil }

// setupKubeScaleMock sets up the mock expectation for kubectl scale --replicas=N.
func setupKubeScaleMock(mockExec *executer.MockExecuter, appName, kubeconfigPath string, replicas int) *gomock.Call {
	appID := lifecycle.GenerateAppID(appName, v1beta1.CurrentProcessUsername)
	namespace := helm.AppNamespace(nil, appName)
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
	namespace := helm.AppNamespace(nil, appName)
	versionCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", gomock.Any()).Return("v3.14.0", "", 0)
	getNamespaceCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
		"get", "namespace", namespace, "--kubeconfig", kubeconfigPath,
	}).Return(namespace+"   Active   1d", "", 0)
	pathExistsCall := mockRW.EXPECT().PathExists(kubeconfigPath).Return(true, nil)
	upgradeCall := mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", gomock.Any()).Return("", "", 0)

	gomock.InOrder(versionCall, getNamespaceCall, pathExistsCall, upgradeCall)
	return []*gomock.Call{versionCall, getNamespaceCall, pathExistsCall, upgradeCall}
}
