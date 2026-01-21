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
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	k8s := NewKube(logger, mockExec, mockReadWriter, WithBinary("kubectl"))
	require.NotNil(k8s)
	require.Equal("kubectl", k8s.Binary())
	require.True(k8s.IsAvailable())

	k8s = NewKube(logger, mockExec, mockReadWriter, WithBinary("oc"))
	require.NotNil(k8s)
	require.Equal("oc", k8s.Binary())
	require.True(k8s.IsAvailable())
}

func TestKube_IsAvailable(t *testing.T) {
	testCases := []struct {
		name     string
		binary   string
		expected bool
	}{
		{
			name:     "available when binary is set",
			binary:   "kubectl",
			expected: true,
		},
		{
			name:     "not available when binary is empty",
			binary:   "",
			expected: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k8s := &Kube{binary: tc.binary}
			require.Equal(t, tc.expected, k8s.IsAvailable())
		})
	}
}

func TestKube_WatchPodsCmd(t *testing.T) {
	testCases := []struct {
		name           string
		binary         string
		kubeconfigPath string
		expectedArgs   []string
	}{
		{
			name:           "kubectl without kubeconfig",
			binary:         "kubectl",
			kubeconfigPath: "",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json"},
		},
		{
			name:           "kubectl with kubeconfig",
			binary:         "kubectl",
			kubeconfigPath: "/tmp/kubeconfig",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json", "--kubeconfig", "/tmp/kubeconfig"},
		},
		{
			name:           "oc without kubeconfig",
			binary:         "oc",
			kubeconfigPath: "",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json"},
		},
		{
			name:           "oc with kubeconfig",
			binary:         "oc",
			kubeconfigPath: "/var/lib/microshift/resources/kubeadmin/kubeconfig",
			expectedArgs:   []string{"get", "pods", "--watch", "--output-watch-events", "--all-namespaces", "-o", "json", "--kubeconfig", "/var/lib/microshift/resources/kubeadmin/kubeconfig"},
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

			k8s := NewKube(logger, mockExec, mockReadWriter, WithBinary(tc.binary))

			var opts []KubeOption
			if tc.kubeconfigPath != "" {
				opts = append(opts, WithKubeKubeconfig(tc.kubeconfigPath))
			}
			cmd, err := k8s.WatchPodsCmd(context.Background(), opts...)
			require.NoError(err)
			require.NotNil(cmd)
			require.Equal(append([]string{tc.binary}, tc.expectedArgs...), cmd.Args)
		})
	}
}

func TestKube_WatchPodsCmd_NoBinary(t *testing.T) {
	require := require.New(t)

	k8s := &Kube{
		binary: "",
	}

	cmd, err := k8s.WatchPodsCmd(context.Background())
	require.Error(err)
	require.Nil(cmd)
	require.Contains(err.Error(), "kubernetes CLI binary not available")
}

func TestKube_ResolveKubeconfig(t *testing.T) {
	testCases := []struct {
		name        string
		setupMock   func(*fileio.MockReadWriter)
		envSetup    func()
		envCleanup  func()
		wantPath    string
		wantErr     bool
		errContains string
	}{
		{
			name: "KUBECONFIG env var exists and file exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/custom/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/custom/kubeconfig")
			},
			envCleanup: func() {},
			wantPath:   "/custom/kubeconfig",
			wantErr:    false,
		},
		{
			name: "KUBECONFIG env var exists but file does not exist, fallback to microshift",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/custom/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists("/var/lib/microshift/resources/kubeadmin/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/custom/kubeconfig")
			},
			envCleanup: func() {},
			wantPath:   "/var/lib/microshift/resources/kubeadmin/kubeconfig",
			wantErr:    false,
		},
		{
			name: "no KUBECONFIG env, microshift path exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/microshift/resources/kubeadmin/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
			},
			envCleanup: func() {},
			wantPath:   "/var/lib/microshift/resources/kubeadmin/kubeconfig",
			wantErr:    false,
		},
		{
			name: "no KUBECONFIG env, no microshift, default path exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/microshift/resources/kubeadmin/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists(gomock.Any()).Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
			},
			envCleanup: func() {},
			wantPath:   "", // will match any path since HOME varies
			wantErr:    false,
		},
		{
			name: "no kubeconfig found anywhere",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/var/lib/microshift/resources/kubeadmin/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists(gomock.Any()).Return(false, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "")
			},
			envCleanup:  func() {},
			wantPath:    "",
			wantErr:     true,
			errContains: "no kubeconfig found",
		},
		{
			name: "KUBECONFIG with multiple paths - first exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/first/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/first/kubeconfig:/second/kubeconfig:/third/kubeconfig")
			},
			envCleanup: func() {},
			wantPath:   "/first/kubeconfig",
			wantErr:    false,
		},
		{
			name: "KUBECONFIG with multiple paths - second exists",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/first/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists("/second/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/first/kubeconfig:/second/kubeconfig:/third/kubeconfig")
			},
			envCleanup: func() {},
			wantPath:   "/second/kubeconfig",
			wantErr:    false,
		},
		{
			name: "KUBECONFIG with multiple paths - none exist, fallback to microshift",
			setupMock: func(mockRW *fileio.MockReadWriter) {
				mockRW.EXPECT().PathExists("/first/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists("/second/kubeconfig").Return(false, nil)
				mockRW.EXPECT().PathExists("/var/lib/microshift/resources/kubeadmin/kubeconfig").Return(true, nil)
			},
			envSetup: func() {
				t.Setenv("KUBECONFIG", "/first/kubeconfig:/second/kubeconfig")
			},
			envCleanup: func() {},
			wantPath:   "/var/lib/microshift/resources/kubeadmin/kubeconfig",
			wantErr:    false,
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
			defer tc.envCleanup()

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
			if tc.wantPath != "" {
				require.Equal(tc.wantPath, path)
			} else {
				require.NotEmpty(path)
			}
		})
	}
}
