package helm

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestLabelerInjectLabels(t *testing.T) {
	const tempDir = "/tmp/kustomize-labels-test"

	testCases := []struct {
		name          string
		input         string
		labels        map[string]string
		setupMocks    func(*executer.MockExecuter, *fileio.MockReadWriter, string) string
		expected      []string
		expectedCount int
	}{
		{
			name: "simple pod with no existing labels",
			input: `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx`,
			labels: map[string]string{AppLabelKey: "my-app"},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter, input string) string {
				output := `apiVersion: v1
kind: Pod
metadata:
  labels:
    agent.flightctl.io/app: my-app
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx
`
				mockRW.EXPECT().MkdirTemp("kustomize-labels-*").Return(tempDir, nil)
				mockRW.EXPECT().WriteFile(tempDir+"/manifests.yaml", []byte(input), fileio.DefaultFilePermissions).Return(nil)
				mockRW.EXPECT().WriteFile(tempDir+"/kustomization.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "kustomize", tempDir).Return(output, "", 0)
				mockRW.EXPECT().RemoveAll(tempDir).Return(nil)
				return output
			},
			expected: []string{
				"agent.flightctl.io/app: my-app",
			},
			expectedCount: 1,
		},
		{
			name: "deployment with pod template",
			input: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deployment
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:latest`,
			labels: map[string]string{AppLabelKey: "my-app"},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter, input string) string {
				output := `apiVersion: apps/v1
kind: Deployment
metadata:
  labels:
    agent.flightctl.io/app: my-app
  name: test-deployment
spec:
  replicas: 1
  selector:
    matchLabels:
      app: nginx
  template:
    metadata:
      labels:
        agent.flightctl.io/app: my-app
        app: nginx
    spec:
      containers:
      - name: nginx
        image: nginx:latest
`
				mockRW.EXPECT().MkdirTemp("kustomize-labels-*").Return(tempDir, nil)
				mockRW.EXPECT().WriteFile(tempDir+"/manifests.yaml", []byte(input), fileio.DefaultFilePermissions).Return(nil)
				mockRW.EXPECT().WriteFile(tempDir+"/kustomization.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "kustomize", tempDir).Return(output, "", 0)
				mockRW.EXPECT().RemoveAll(tempDir).Return(nil)
				return output
			},
			expected: []string{
				"agent.flightctl.io/app: my-app",
			},
			expectedCount: 2,
		},
		{
			name: "configmap only gets metadata labels",
			input: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
data:
  key: value`,
			labels: map[string]string{AppLabelKey: "my-config"},
			setupMocks: func(mockExec *executer.MockExecuter, mockRW *fileio.MockReadWriter, input string) string {
				output := `apiVersion: v1
data:
  key: value
kind: ConfigMap
metadata:
  labels:
    agent.flightctl.io/app: my-config
  name: test-config
`
				mockRW.EXPECT().MkdirTemp("kustomize-labels-*").Return(tempDir, nil)
				mockRW.EXPECT().WriteFile(tempDir+"/manifests.yaml", []byte(input), fileio.DefaultFilePermissions).Return(nil)
				mockRW.EXPECT().WriteFile(tempDir+"/kustomization.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
				mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "kustomize", tempDir).Return(output, "", 0)
				mockRW.EXPECT().RemoveAll(tempDir).Return(nil)
				return output
			},
			expected: []string{
				"agent.flightctl.io/app: my-config",
			},
			expectedCount: 1,
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

			tc.setupMocks(mockExec, mockReadWriter, tc.input)

			kubeClient := client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl"), client.WithKubeconfigPath("/tmp"))
			labeler := NewLabeler(kubeClient, mockReadWriter)

			var output bytes.Buffer
			err := labeler.InjectLabels(context.Background(), strings.NewReader(tc.input), &output, tc.labels)
			require.NoError(err)

			result := output.String()
			for _, exp := range tc.expected {
				require.Contains(result, exp)
				occurrences := strings.Count(result, exp)
				require.Equal(tc.expectedCount, occurrences, "expected %d occurrences of label %q", tc.expectedCount, exp)
			}
		})
	}
}

func TestLabelerInjectLabels_MkdirTempFails(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	mockReadWriter.EXPECT().MkdirTemp("kustomize-labels-*").Return("", fmt.Errorf("disk full"))

	kubeClient := client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl"), client.WithKubeconfigPath("/tmp"))
	labeler := NewLabeler(kubeClient, mockReadWriter)

	var output bytes.Buffer
	err := labeler.InjectLabels(context.Background(), strings.NewReader("test"), &output, map[string]string{"key": "value"})
	require.Error(err)
	require.Contains(err.Error(), "create temp directory")
}

func TestLabelerInjectLabels_KustomizeFails(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	tempDir := "/tmp/kustomize-labels-test"
	input := "apiVersion: v1\nkind: Pod\nmetadata:\n  name: test"

	mockReadWriter.EXPECT().MkdirTemp("kustomize-labels-*").Return(tempDir, nil)
	mockReadWriter.EXPECT().WriteFile(tempDir+"/manifests.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
	mockReadWriter.EXPECT().WriteFile(tempDir+"/kustomization.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "kustomize", tempDir).
		Return("", "error: invalid kustomization", 1)
	mockReadWriter.EXPECT().RemoveAll(tempDir).Return(nil)

	kubeClient := client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl"), client.WithKubeconfigPath("/tmp"))
	labeler := NewLabeler(kubeClient, mockReadWriter)

	var output bytes.Buffer
	err := labeler.InjectLabels(context.Background(), strings.NewReader(input), &output, map[string]string{"key": "value"})
	require.Error(err)
	require.Contains(err.Error(), "kubectl kustomize failed")
}

func TestLabelerInjectLabels_EmptyInput(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	tempDir := "/tmp/kustomize-labels-test"

	mockReadWriter.EXPECT().MkdirTemp("kustomize-labels-*").Return(tempDir, nil)
	mockReadWriter.EXPECT().WriteFile(tempDir+"/manifests.yaml", []byte(""), fileio.DefaultFilePermissions).Return(nil)
	mockReadWriter.EXPECT().WriteFile(tempDir+"/kustomization.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "kustomize", tempDir).
		Return("", "", 0)
	mockReadWriter.EXPECT().RemoveAll(tempDir).Return(nil)

	kubeClient := client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl"), client.WithKubeconfigPath("/tmp"))
	labeler := NewLabeler(kubeClient, mockReadWriter)

	var output bytes.Buffer
	err := labeler.InjectLabels(context.Background(), strings.NewReader(""), &output, map[string]string{"key": "value"})
	require.NoError(err)
	require.Empty(strings.TrimSpace(output.String()))
}

func TestLabelerInjectLabels_MultiDocument(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("test")
	mockExec := executer.NewMockExecuter(ctrl)
	mockReadWriter := fileio.NewMockReadWriter(ctrl)

	tempDir := "/tmp/kustomize-labels-test"

	input := `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: config1
data:
  key: value1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: config2
data:
  key: value2`

	kustomizeOutput := `apiVersion: v1
data:
  key: value1
kind: ConfigMap
metadata:
  labels:
    agent.flightctl.io/app: multi-doc-test
  name: config1
---
apiVersion: v1
data:
  key: value2
kind: ConfigMap
metadata:
  labels:
    agent.flightctl.io/app: multi-doc-test
  name: config2
`

	mockReadWriter.EXPECT().MkdirTemp("kustomize-labels-*").Return(tempDir, nil)
	mockReadWriter.EXPECT().WriteFile(tempDir+"/manifests.yaml", []byte(input), fileio.DefaultFilePermissions).Return(nil)
	mockReadWriter.EXPECT().WriteFile(tempDir+"/kustomization.yaml", gomock.Any(), fileio.DefaultFilePermissions).Return(nil)
	mockExec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "kustomize", tempDir).
		Return(kustomizeOutput, "", 0)
	mockReadWriter.EXPECT().RemoveAll(tempDir).Return(nil)

	kubeClient := client.NewKube(logger, mockExec, mockReadWriter, client.WithBinary("kubectl"), client.WithKubeconfigPath("/tmp"))
	labeler := NewLabeler(kubeClient, mockReadWriter)

	labels := map[string]string{AppLabelKey: "multi-doc-test"}

	var output bytes.Buffer
	err := labeler.InjectLabels(context.Background(), strings.NewReader(input), &output, labels)
	require.NoError(err)

	result := output.String()
	occurrences := strings.Count(result, "agent.flightctl.io/app: multi-doc-test")
	require.Equal(2, occurrences, "expected 2 label injections for 2 documents")
}
