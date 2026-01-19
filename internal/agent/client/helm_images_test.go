package client

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractImagesFromManifests(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name           string
		manifests      string
		expectedImages []string
	}{
		{
			name:           "empty manifest returns empty slice",
			manifests:      "",
			expectedImages: []string{},
		},
		{
			name: "single Pod with containers",
			manifests: `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: nginx:1.19
  - name: sidecar
    image: busybox:latest`,
			expectedImages: []string{"nginx:1.19", "busybox:latest"},
		},
		{
			name: "Pod with init containers and containers",
			manifests: `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  initContainers:
  - name: init
    image: alpine:3.14
  containers:
  - name: app
    image: nginx:1.19`,
			expectedImages: []string{"alpine:3.14", "nginx:1.19"},
		},
		{
			name: "Deployment with pod template",
			manifests: `apiVersion: apps/v1
kind: Deployment
metadata:
  name: test-deployment
spec:
  replicas: 3
  template:
    spec:
      containers:
      - name: app
        image: myapp:v1.0.0
      initContainers:
      - name: init
        image: init-image:latest`,
			expectedImages: []string{"myapp:v1.0.0", "init-image:latest"},
		},
		{
			name: "StatefulSet with pod template",
			manifests: `apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: test-statefulset
spec:
  template:
    spec:
      containers:
      - name: db
        image: postgres:13`,
			expectedImages: []string{"postgres:13"},
		},
		{
			name: "DaemonSet with pod template",
			manifests: `apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: test-daemonset
spec:
  template:
    spec:
      containers:
      - name: agent
        image: monitoring-agent:v2`,
			expectedImages: []string{"monitoring-agent:v2"},
		},
		{
			name: "ReplicaSet with pod template",
			manifests: `apiVersion: apps/v1
kind: ReplicaSet
metadata:
  name: test-replicaset
spec:
  template:
    spec:
      containers:
      - name: app
        image: webapp:v3`,
			expectedImages: []string{"webapp:v3"},
		},
		{
			name: "Job with pod template",
			manifests: `apiVersion: batch/v1
kind: Job
metadata:
  name: test-job
spec:
  template:
    spec:
      containers:
      - name: worker
        image: job-runner:latest`,
			expectedImages: []string{"job-runner:latest"},
		},
		{
			name: "CronJob with job template",
			manifests: `apiVersion: batch/v1
kind: CronJob
metadata:
  name: test-cronjob
spec:
  schedule: "0 * * * *"
  jobTemplate:
    spec:
      template:
        spec:
          containers:
          - name: cron
            image: cron-image:v1
          initContainers:
          - name: setup
            image: setup-image:v1`,
			expectedImages: []string{"cron-image:v1", "setup-image:v1"},
		},
		{
			name: "multi-document YAML with multiple resources",
			manifests: `apiVersion: v1
kind: Pod
metadata:
  name: pod1
spec:
  containers:
  - name: app
    image: app1:v1
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: deployment1
spec:
  template:
    spec:
      containers:
      - name: app
        image: app2:v2
---
apiVersion: v1
kind: Service
metadata:
  name: service1
spec:
  ports:
  - port: 80`,
			expectedImages: []string{"app1:v1", "app2:v2"},
		},
		{
			name: "deduplication of images",
			manifests: `apiVersion: v1
kind: Pod
metadata:
  name: pod1
spec:
  containers:
  - name: app1
    image: nginx:1.19
---
apiVersion: v1
kind: Pod
metadata:
  name: pod2
spec:
  containers:
  - name: app2
    image: nginx:1.19`,
			expectedImages: []string{"nginx:1.19"},
		},
		{
			name: "skips documents with missing kind",
			manifests: `apiVersion: v1
kind: Pod
metadata:
  name: valid-pod
spec:
  containers:
  - name: app
    image: valid-image:v1
---
apiVersion: v1
metadata:
  name: unknown-resource
data:
  key: value
---
apiVersion: v1
kind: Pod
metadata:
  name: another-pod
spec:
  containers:
  - name: app
    image: another-image:v2`,
			expectedImages: []string{"valid-image:v1", "another-image:v2"},
		},
		{
			name: "skips resources without images",
			manifests: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test-config
data:
  key: value
---
apiVersion: v1
kind: Secret
metadata:
  name: test-secret
type: Opaque
data:
  password: cGFzc3dvcmQ=`,
			expectedImages: []string{},
		},
		{
			name: "handles containers with empty image",
			manifests: `apiVersion: v1
kind: Pod
metadata:
  name: test-pod
spec:
  containers:
  - name: app
    image: ""
  - name: valid
    image: valid-image:v1`,
			expectedImages: []string{"valid-image:v1"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			images, err := ExtractImagesFromManifests(tc.manifests)
			require.NoError(err)

			sort.Strings(images)
			sort.Strings(tc.expectedImages)
			require.Equal(tc.expectedImages, images)
		})
	}
}
