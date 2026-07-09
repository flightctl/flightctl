package client

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewKube_WithExplicitBinary(t *testing.T) {
	testCases := []struct {
		name   string
		binary string
	}{
		{
			name:   "kubectl binary",
			binary: "kubectl",
		},
		{
			name:   "oc binary",
			binary: "oc",
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

			k8s := NewKube(logger, mockExec, mockReadWriter,
				WithBinary(tc.binary),
				WithKubeconfigPath("/tmp/kubeconfig"),
			)
			require.NotNil(k8s)
			require.Equal(tc.binary, k8s.Binary())
			require.True(k8s.IsAvailable())
		})
	}
}

func TestKube_IsAvailable(t *testing.T) {
	testCases := []struct {
		name      string
		binary    string
		setupMock func(*fileio.MockReadWriter)
		envSetup  func()
		want      bool
	}{
		{
			name:      "available when binary and kubeconfig are set",
			binary:    "kubectl",
			setupMock: func(mockRW *fileio.MockReadWriter) {},
			envSetup:  func() {},
			want:      true,
		},
		{
			name:   "not available when kubeconfig resolution fails",
			binary: "kubectl",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(microshiftKubeconfigPath).Return(false, nil)
				mockRW.EXPECT().PathExists("/nonexistent/.kube/config").Return(false, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
				t.Setenv("HOME", "/nonexistent")
			},
			want: false,
		},
		{
			name:      "not available when binary not found",
			binary:    "",
			setupMock: func(mockRW *fileio.MockReadWriter) {},
			envSetup:  func() {},
			want:      false,
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

			tc.setupMock(mockReadWriter)
			tc.envSetup()

			opts := []KubernetesOption{}
			if tc.binary != "" {
				opts = append(opts, WithBinary(tc.binary))
			}
			if tc.want {
				opts = append(opts, WithKubeconfigPath("/tmp/kubeconfig"))
			}

			k8s := NewKube(logger, mockExec, mockReadWriter, opts...)
			if tc.binary == "" {
				k8s.commandAvailable = func(string) bool { return false }
			}

			require.Equal(tc.want, k8s.IsAvailable())
		})
	}
}

func TestKube_WatchPodsCmd(t *testing.T) {
	testCases := []struct {
		name             string
		binary           string
		kubeconfigPath   string
		commandAvailable func(string) bool
		expectedArgs     []string
		wantErr          bool
		errContains      string
	}{
		{
			name:           "kubectl without kubeconfig option",
			binary:         "kubectl",
			kubeconfigPath: "",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json"},
		},
		{
			name:           "kubectl with kubeconfig option",
			binary:         "kubectl",
			kubeconfigPath: "/tmp/kubeconfig",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json", "--kubeconfig", "/tmp/kubeconfig"},
		},
		{
			name:           "oc without kubeconfig option",
			binary:         "oc",
			kubeconfigPath: "",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json"},
		},
		{
			name:           "oc with kubeconfig option",
			binary:         "oc",
			kubeconfigPath: "/var/lib/microshift/resources/kubeadmin/kubeconfig",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json", "--kubeconfig", "/var/lib/microshift/resources/kubeadmin/kubeconfig"},
		},
		{
			name:             "no binary available",
			binary:           "",
			commandAvailable: func(string) bool { return false },
			wantErr:          true,
			errContains:      "kubernetes CLI binary not available",
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

			opts := []KubernetesOption{WithKubeconfigPath("/default/kubeconfig")}
			if tc.binary != "" {
				opts = append(opts, WithBinary(tc.binary))
			}

			k8s := NewKube(logger, mockExec, mockReadWriter, opts...)
			if tc.commandAvailable != nil {
				k8s.commandAvailable = tc.commandAvailable
			}

			var kubeOpts []KubeOption
			if tc.kubeconfigPath != "" {
				kubeOpts = append(kubeOpts, WithKubeKubeconfig(tc.kubeconfigPath))
			}

			cmd, err := k8s.WatchPodsCmd(context.Background(), kubeOpts...)

			if tc.wantErr {
				require.Error(err)
				require.Nil(cmd)
				if tc.errContains != "" {
					require.Contains(err.Error(), tc.errContains)
				}
				return
			}

			require.NoError(err)
			require.NotNil(cmd)
			require.Equal(append([]string{tc.binary}, tc.expectedArgs...), cmd.Args)
		})
	}
}

func TestKube_ScaleWorkloadsByLabel(t *testing.T) {
	testCases := []struct {
		name             string
		binary           string
		labelSelector    string
		namespace        string
		kubeconfigPath   string
		replicas         int
		commandAvailable func(string) bool
		mockStdout       string
		mockStderr       string
		mockExitCode     int
		wantErr          bool
		errContains      string
		expectedArgs     []string
	}{
		{
			name:           "When scaling succeeds it should issue the correct kubectl command",
			binary:         "kubectl",
			labelSelector:  "agent.flightctl.io/app=my-app",
			namespace:      "my-namespace",
			kubeconfigPath: "/tmp/kubeconfig",
			replicas:       0,
			mockExitCode:   0,
			expectedArgs:   []string{"scale", "deployment,statefulset", "-l", "agent.flightctl.io/app=my-app", "--replicas=0", "-n", "my-namespace", "--kubeconfig", "/tmp/kubeconfig"},
		},
		{
			name:           "When no matching resources exist kubectl exits 0 and no error is returned",
			binary:         "kubectl",
			labelSelector:  "agent.flightctl.io/app=empty-app",
			namespace:      "my-namespace",
			kubeconfigPath: "/tmp/kubeconfig",
			replicas:       0,
			mockStdout:     "",
			mockExitCode:   0,
			expectedArgs:   []string{"scale", "deployment,statefulset", "-l", "agent.flightctl.io/app=empty-app", "--replicas=0", "-n", "my-namespace", "--kubeconfig", "/tmp/kubeconfig"},
		},
		{
			name:           "When kubectl exits non-zero the stderr message is returned as error",
			binary:         "kubectl",
			labelSelector:  "agent.flightctl.io/app=my-app",
			namespace:      "my-namespace",
			kubeconfigPath: "/tmp/kubeconfig",
			replicas:       0,
			mockStderr:     "error: unable to scale",
			mockExitCode:   1,
			wantErr:        true,
			errContains:    "error: unable to scale",
			expectedArgs:   []string{"scale", "deployment,statefulset", "-l", "agent.flightctl.io/app=my-app", "--replicas=0", "-n", "my-namespace", "--kubeconfig", "/tmp/kubeconfig"},
		},
		{
			name:             "When no binary is available an error is returned",
			binary:           "",
			labelSelector:    "agent.flightctl.io/app=my-app",
			namespace:        "my-namespace",
			kubeconfigPath:   "/tmp/kubeconfig",
			replicas:         0,
			commandAvailable: func(string) bool { return false },
			wantErr:          true,
			errContains:      "kubernetes CLI binary not available",
		},
		{
			name:           "When kubeconfigPath is empty it is omitted from the command",
			binary:         "kubectl",
			labelSelector:  "agent.flightctl.io/app=my-app",
			namespace:      "my-namespace",
			kubeconfigPath: "",
			replicas:       0,
			mockExitCode:   0,
			expectedArgs:   []string{"scale", "deployment,statefulset", "-l", "agent.flightctl.io/app=my-app", "--replicas=0", "-n", "my-namespace"},
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

			opts := []KubernetesOption{WithKubeconfigPath("/default/kubeconfig")}
			if tc.binary != "" {
				opts = append(opts, WithBinary(tc.binary))
			}

			k8s := NewKube(logger, mockExec, mockReadWriter, opts...)
			if tc.commandAvailable != nil {
				k8s.commandAvailable = tc.commandAvailable
			}

			if tc.expectedArgs != nil {
				varArgs := make([]any, len(tc.expectedArgs))
				for i, a := range tc.expectedArgs {
					varArgs[i] = a
				}
				mockExec.EXPECT().
					ExecuteWithContext(gomock.Any(), tc.binary, varArgs...).
					Return(tc.mockStdout, tc.mockStderr, tc.mockExitCode)
			}

			var scaleOpts []KubeOption
			if tc.kubeconfigPath != "" {
				scaleOpts = append(scaleOpts, WithKubeKubeconfig(tc.kubeconfigPath))
			}
			err := k8s.ScaleWorkloadsByLabel(context.Background(), tc.namespace, tc.labelSelector, tc.replicas, scaleOpts...)

			if tc.wantErr {
				require.Error(err)
				if tc.errContains != "" {
					require.Contains(err.Error(), tc.errContains)
				}
				return
			}

			require.NoError(err)
		})
	}
}

func TestKube_ResolveKubeconfig(t *testing.T) {
	testCases := []struct {
		name        string
		setupMock   func(*fileio.MockReadWriter)
		envSetup    func()
		wantPath    string
		wantEmpty   bool
		wantErr     bool
		errContains string
	}{
		{
			name: "KUBECONFIG env var exists and file exists - returns empty to use env var",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/custom/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/custom/kubeconfig")
			},
			wantEmpty: true,
		},
		{
			name: "KUBECONFIG env var exists but file does not exist, fallback to microshift",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/custom/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists(microshiftKubeconfigPath).Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/custom/kubeconfig")
			},
			wantPath: microshiftKubeconfigPath,
		},
		{
			name: "no KUBECONFIG env, microshift path exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(microshiftKubeconfigPath).Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
			},
			wantPath: microshiftKubeconfigPath,
		},
		{
			name: "no KUBECONFIG env, no microshift, default path exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(microshiftKubeconfigPath).Return(false, nil)
				mockRW.EXPECT().PathExists(gomock.Any()).Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
				t.Setenv("HOME", "/home/testuser")
			},
			wantPath: "/home/testuser/.kube/config",
		},
		{
			name: "no kubeconfig found anywhere",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists(microshiftKubeconfigPath).Return(false, nil)
				mockRW.EXPECT().PathExists(gomock.Any()).Return(false, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
				t.Setenv("HOME", "/nonexistent")
			},
			wantErr:     true,
			errContains: "no kubeconfig found",
		},
		{
			name: "KUBECONFIG with multiple paths - first exists - returns empty to use env var",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/first/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/first/kubeconfig:/second/kubeconfig:/third/kubeconfig")
			},
			wantEmpty: true,
		},
		{
			name: "KUBECONFIG with multiple paths - second exists - returns empty to use env var",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/first/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists("/second/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/first/kubeconfig:/second/kubeconfig:/third/kubeconfig")
			},
			wantEmpty: true,
		},
		{
			name: "KUBECONFIG with multiple paths - none exist, fallback to microshift",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/first/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists("/second/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists(microshiftKubeconfigPath).Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/first/kubeconfig:/second/kubeconfig")
			},
			wantPath: microshiftKubeconfigPath,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			logger := log.NewPrefixLogger("test")
			mockExec := executer.NewMockExecuter(ctrl)
			mockRW := fileio.NewMockReadWriter(ctrl)

			tc.setupMock(mockRW)
			tc.envSetup()

			k8s := NewKube(logger, mockExec, mockRW, WithBinary("kubectl"))

			path, err := k8s.ResolveKubeconfig()

			if tc.wantErr {
				require.Error(err)
				if tc.errContains != "" {
					require.Contains(err.Error(), tc.errContains)
				}
				return
			}

			require.NoError(err)
			if tc.wantEmpty {
				require.Empty(path)
			} else {
				require.Equal(tc.wantPath, path)
			}
		})
	}
}
