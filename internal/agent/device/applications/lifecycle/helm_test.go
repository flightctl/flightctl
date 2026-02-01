package lifecycle

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type testCLIClients struct {
	helm *client.Helm
	kube *client.Kube
}

func (t *testCLIClients) Podman() *client.Podman { return nil }
func (t *testCLIClients) Skopeo() *client.Skopeo { return nil }
func (t *testCLIClients) Kube() *client.Kube     { return t.kube }
func (t *testCLIClients) Helm() *client.Helm     { return t.helm }
func (t *testCLIClients) CRI() *client.CRI       { return nil }

type testExecutableResolver struct {
	path string
}

func (r testExecutableResolver) Resolve() (string, error) {
	return r.path, nil
}

func TestHelmHandler_Execute_Add(t *testing.T) {
	testCases := []struct {
		name           string
		action         *Action
		kubeconfigPath string
		setupMock      func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr        bool
	}{
		{
			name: "success without kubeconfig",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-1.0.0",
				Type: ActionAdd,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"get", "namespace", "my-namespace",
					}).Return("", "not found", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"create", "namespace", "my-namespace",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"label", "namespace", "my-namespace",
						"flightctl.io/managed-by=flightctl", "--overwrite",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-1.0.0",
						"--install",
						"--namespace", "my-namespace",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "my-namespace",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "success with kubeconfig",
			action: &Action{
				ID:   "prod-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-1.0.0",
				Type: ActionAdd,
				Spec: HelmSpec{Namespace: "prod-namespace"},
			},
			kubeconfigPath: "/tmp/kubeconfig",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"get", "namespace", "prod-namespace", "--kubeconfig", "/tmp/kubeconfig",
					}).Return("", "not found", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"create", "namespace", "prod-namespace", "--kubeconfig", "/tmp/kubeconfig",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"label", "namespace", "prod-namespace",
						"flightctl.io/managed-by=flightctl", "--overwrite", "--kubeconfig", "/tmp/kubeconfig",
					}).Return("", "", 0),
					mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-1.0.0",
						"--install",
						"--namespace", "prod-namespace",
						"--kubeconfig", "/tmp/kubeconfig",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "prod-namespace",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "success with existing namespace",
			action: &Action{
				ID:   "existing-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-1.0.0",
				Type: ActionAdd,
				Spec: HelmSpec{Namespace: "existing-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"get", "namespace", "existing-namespace",
					}).Return("existing-namespace   Active   5d", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-1.0.0",
						"--install",
						"--namespace", "existing-namespace",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "existing-namespace",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "success with default namespace",
			action: &Action{
				ID:   "my-release",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-1.0.0",
				Type: ActionAdd,
				Spec: HelmSpec{},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"get", "namespace", "flightctl-my-release",
					}).Return("", "not found", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"create", "namespace", "flightctl-my-release",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"label", "namespace", "flightctl-my-release",
						"flightctl.io/managed-by=flightctl", "--overwrite",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-1.0.0",
						"--install",
						"--namespace", "flightctl-my-release",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "my-release",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "error from helm upgrade --install",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-1.0.0",
				Type: ActionAdd,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"get", "namespace", "my-namespace",
					}).Return("", "not found", 1),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"create", "namespace", "my-namespace",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
						"label", "namespace", "my-namespace",
						"flightctl.io/managed-by=flightctl", "--overwrite",
					}).Return("", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-1.0.0",
						"--install",
						"--namespace", "my-namespace",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "my-namespace",
					}).Return("", "Error: chart not found", 1),
				)
			},
			wantErr: true,
		},
		{
			name: "helm 4.x uses rollback-on-failure instead of atomic",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-1.0.0",
				Type: ActionAdd,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().MkdirTemp("helm-plugins").Return("/tmp/helm-plugins-123", nil)
				mockRW.EXPECT().MkdirAll("/tmp/helm-plugins-123/flightctl-postrenderer", fileio.DefaultDirectoryPermissions).Return(nil)
				mockRW.EXPECT().WriteFile("/tmp/helm-plugins-123/flightctl-postrenderer/plugin.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
				mockRW.EXPECT().RemoveAll("/tmp/helm-plugins-123").Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
					"version", "--short",
				}).Return("v4.0.0", "", 0)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
					"get", "namespace", "my-namespace",
				}).Return("my-namespace   Active   5d", "", 0)
				mockExec.EXPECT().ExecuteWithContextFromDir(
					gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(),
				).DoAndReturn(func(ctx context.Context, dir, cmd string, args []string, env ...string) (string, string, int) {
					require.Equal(t, "", dir)
					require.Equal(t, "helm", cmd)
					require.Contains(t, args, "--rollback-on-failure")
					require.NotContains(t, args, "--atomic")
					require.Contains(t, args, "--post-renderer")
					require.Contains(t, args, "flightctl-postrenderer")
					return "", "", 0
				})
			},
			wantErr: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)

			clients := &testCLIClients{
				helm: client.NewHelm(logger, mockExec, mockReadWriter, "/var/lib/flightctl"),
				kube: client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl")),
			}
			resolver := testExecutableResolver{path: "/test/flightctl"}
			rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
				return mockReadWriter, nil
			}
			handler := NewHelmHandler(logger, clients, tc.kubeconfigPath, resolver, rwFactory)
			err := handler.Execute(context.Background(), []Action{*tc.action})

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmHandler_Execute_Remove(t *testing.T) {
	testCases := []struct {
		name           string
		action         *Action
		kubeconfigPath string
		setupMock      func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr        bool
	}{
		{
			name: "success without kubeconfig",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Type: ActionRemove,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"uninstall", "my-release",
						"--namespace", "my-namespace",
						"--ignore-not-found",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "success with kubeconfig",
			action: &Action{
				ID:   "prod-namespace",
				Name: "my-release",
				Type: ActionRemove,
				Spec: HelmSpec{Namespace: "prod-namespace"},
			},
			kubeconfigPath: "/tmp/kubeconfig",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"uninstall", "my-release",
						"--namespace", "prod-namespace",
						"--kubeconfig", "/tmp/kubeconfig",
						"--ignore-not-found",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "error from helm uninstall",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Type: ActionRemove,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"uninstall", "my-release",
						"--namespace", "my-namespace",
						"--ignore-not-found",
					}).Return("", "Error: release not found", 1),
				)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)

			clients := &testCLIClients{
				helm: client.NewHelm(logger, mockExec, mockReadWriter, "/var/lib/flightctl"),
				kube: client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl")),
			}
			resolver := testExecutableResolver{path: "/test/flightctl"}
			rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
				return mockReadWriter, nil
			}
			handler := NewHelmHandler(logger, clients, tc.kubeconfigPath, resolver, rwFactory)
			err := handler.Execute(context.Background(), []Action{*tc.action})

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmHandler_Execute_Update(t *testing.T) {
	testCases := []struct {
		name           string
		action         *Action
		kubeconfigPath string
		setupMock      func(*executer.MockExecuter, *fileio.MockReadWriter)
		wantErr        bool
	}{
		{
			name: "success without kubeconfig",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-2.0.0",
				Type: ActionUpdate,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-2.0.0",
						"--install",
						"--namespace", "my-namespace",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "my-namespace",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "success with kubeconfig",
			action: &Action{
				ID:   "prod-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-2.0.0",
				Type: ActionUpdate,
				Spec: HelmSpec{Namespace: "prod-namespace"},
			},
			kubeconfigPath: "/tmp/kubeconfig",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockRW.EXPECT().PathExists("/tmp/kubeconfig").Return(true, nil),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-2.0.0",
						"--install",
						"--namespace", "prod-namespace",
						"--kubeconfig", "/tmp/kubeconfig",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "prod-namespace",
					}).Return("", "", 0),
				)
			},
			wantErr: false,
		},
		{
			name: "error from helm upgrade",
			action: &Action{
				ID:   "my-namespace",
				Name: "my-release",
				Path: "/var/lib/flightctl/helm/charts/mychart-2.0.0",
				Type: ActionUpdate,
				Spec: HelmSpec{Namespace: "my-namespace"},
			},
			kubeconfigPath: "",
			setupMock: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter) {
				gomock.InOrder(
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"version", "--short",
					}).Return("v3.14.0", "", 0),
					mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
						"upgrade", "my-release", "/var/lib/flightctl/helm/charts/mychart-2.0.0",
						"--install",
						"--namespace", "my-namespace",
						"--atomic",
						"--post-renderer", "/test/flightctl",
						"--post-renderer-args", "helm-render",
						"--post-renderer-args", "--app",
						"--post-renderer-args", "my-namespace",
					}).Return("", "Error: chart not found", 1),
				)
			},
			wantErr: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockReadWriter := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockExec, mockReadWriter)

			clients := &testCLIClients{
				helm: client.NewHelm(logger, mockExec, mockReadWriter, "/var/lib/flightctl"),
				kube: client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl")),
			}
			resolver := testExecutableResolver{path: "/test/flightctl"}
			rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
				return mockReadWriter, nil
			}
			handler := NewHelmHandler(logger, clients, tc.kubeconfigPath, resolver, rwFactory)
			err := handler.Execute(context.Background(), []Action{*tc.action})

			if tc.wantErr {
				require.Error(err)
				return
			}
			require.NoError(err)
		})
	}
}

func TestHelmHandler_Execute_MultipleActions(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	gomock.InOrder(
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
			"version", "--short",
		}).Return("v3.14.0", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
			"get", "namespace", "ns1",
		}).Return("", "not found", 1),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
			"create", "namespace", "ns1",
		}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", []string{
			"label", "namespace", "ns1",
			"flightctl.io/managed-by=flightctl", "--overwrite",
		}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
			"upgrade", "app1", "/charts/app1",
			"--install",
			"--namespace", "ns1",
			"--atomic",
			"--post-renderer", "/test/flightctl",
			"--post-renderer-args", "helm-render",
			"--post-renderer-args", "--app",
			"--post-renderer-args", "ns1",
		}).Return("", "", 0),
		mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
			"upgrade", "app2", "/charts/app2",
			"--install",
			"--namespace", "ns2",
			"--atomic",
			"--post-renderer", "/test/flightctl",
			"--post-renderer-args", "helm-render",
			"--post-renderer-args", "--app",
			"--post-renderer-args", "ns2",
		}).Return("", "", 0),
	)

	clients := &testCLIClients{
		helm: client.NewHelm(logger, mockExec, mockReadWriter, client.HelmChartsDir),
		kube: client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl")),
	}
	resolver := testExecutableResolver{path: "/test/flightctl"}
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
		return mockReadWriter, nil
	}
	handler := NewHelmHandler(logger, clients, "", resolver, rwFactory)

	actions := []Action{
		{
			ID:   "ns1",
			Name: "app1",
			Path: "/charts/app1",
			Type: ActionAdd,
			Spec: HelmSpec{Namespace: "ns1"},
		},
		{
			ID:   "ns2",
			Name: "app2",
			Path: "/charts/app2",
			Type: ActionUpdate,
			Spec: HelmSpec{Namespace: "ns2"},
		},
	}

	err := handler.Execute(context.Background(), actions)
	require.NoError(err)
}

func TestHelmHandler_Execute_UnsupportedActionType(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "helm", []string{
		"version", "--short",
	}).Return("v3.14.0", "", 0)

	clients := &testCLIClients{
		helm: client.NewHelm(logger, mockExec, mockReadWriter, client.HelmChartsDir),
		kube: client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl")),
	}
	resolver := testExecutableResolver{path: "/test/flightctl"}
	rwFactory := func(_ v1beta1.Username) (fileio.ReadWriter, error) {
		return mockReadWriter, nil
	}
	handler := NewHelmHandler(logger, clients, "", resolver, rwFactory)

	action := Action{
		ID:   "ns1",
		Name: "app1",
		Path: "/charts/app1",
		Type: ActionType("unsupported"),
	}

	err := handler.Execute(context.Background(), []Action{action})
	require.Error(err)
	require.Contains(err.Error(), "unsupported action type")
}
