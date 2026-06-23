package provider

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// samplePodYAML is a realistic subset of the pod YAML produced by kubevirt-vm-to-pod,
// containing all three source fields the agent must parse for OCI prefetch.
const samplePodYAML = `
apiVersion: v1
kind: Pod
metadata:
  name: fedora-vm
spec:
  containers:
  - name: compute
    image: quay.io/kubevirt/virt-launcher:v1.8.0
  initContainers:
  - name: volumecontainerdisk
    image: quay.io/containerdisks/fedora:40
  volumes:
  - name: containerdisk
    image:
      reference: quay.io/containerdisks/fedora:40
  - name: launcher-volume
    image:
      reference: quay.io/kubevirt/virt-launcher:v1.8.0
`

const sampleKubeUnit = `[Kube]
Yaml=pod.yaml
`

const nonVMPodYAML = `
apiVersion: v1
kind: Pod
metadata:
  name: regular-app
spec:
  containers:
  - name: app
    image: nginx:latest
  volumes:
  - name: cache
    image:
      reference: redis:7
`

const kubeUnitNoYamlKey = `[Kube]
KubeDownForce=false
`

// sampleVMPodYAML is a minimal VM pod YAML with the annotations and command
// that parseVMContainerInfo requires.
const sampleVMPodYAML = `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-fedora-vm
  annotations:
    kubectl.kubernetes.io/default-container: compute
    kubevirt.io/domain: fedora-vm
spec:
  containers:
  - name: compute
    image: quay.io/kubevirt/virt-launcher:v1.8.0
    command:
    - /usr/bin/virt-launcher-monitor
    - --name
    - fedora-vm
    - --namespace
    - default
`

func TestParseNamespaceFromCommand(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "When --namespace flag is present it should return its value",
			args:     []string{"--name", "fedora-vm", "--namespace", "default"},
			expected: "default",
		},
		{
			name:     "When --namespace flag has a non-default value it should return that value",
			args:     []string{"--namespace", "my-ns", "--name", "vm"},
			expected: "my-ns",
		},
		{
			name:     "When --namespace flag is absent it should return empty string",
			args:     []string{"--name", "fedora-vm", "--uid", ""},
			expected: "",
		},
		{
			name:     "When --namespace is the last element with no value it should return empty string",
			args:     []string{"--name", "fedora-vm", "--namespace"},
			expected: "",
		},
		{
			name:     "When args is empty it should return empty string",
			args:     []string{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.expected, parseNamespaceFromCommand(tt.args))
		})
	}
}

func TestParseNamespaceFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      []podEnvVar
		expected string
	}{
		{
			name: "When STANDALONE_VMI has a namespace it should return it",
			env: []podEnvVar{
				{Name: "OTHER_VAR", Value: "irrelevant"},
				{Name: standaloneVMIEnvVar, Value: `{"metadata":{"name":"fedora-vm","namespace":"staging"}}`},
			},
			expected: "staging",
		},
		{
			name: "When STANDALONE_VMI has namespace default it should return default",
			env: []podEnvVar{
				{Name: standaloneVMIEnvVar, Value: `{"metadata":{"name":"fedora-vm","namespace":"default"}}`},
			},
			expected: "default",
		},
		{
			name: "When STANDALONE_VMI env var is absent it should return empty string",
			env: []podEnvVar{
				{Name: "OTHER_VAR", Value: "val"},
			},
			expected: "",
		},
		{
			name: "When STANDALONE_VMI value is not valid JSON it should return empty string",
			env: []podEnvVar{
				{Name: standaloneVMIEnvVar, Value: "not-json"},
			},
			expected: "",
		},
		{
			name: "When STANDALONE_VMI JSON has no namespace field it should return empty string",
			env: []podEnvVar{
				{Name: standaloneVMIEnvVar, Value: `{"metadata":{"name":"fedora-vm"}}`},
			},
			expected: "",
		},
		{
			name:     "When env is empty it should return empty string",
			env:      []podEnvVar{},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.expected, parseNamespaceFromEnv(tt.env))
		})
	}
}

func TestParseVMContainerInfo(t *testing.T) {
	tests := []struct {
		name            string
		podYAML         string
		expectedInfo    VMContainerInfo
		wantErr         bool
		wantErrContains string
	}{
		{
			name:    "When pod YAML has all required fields it should return correct info with parsed namespace",
			podYAML: sampleVMPodYAML,
			expectedInfo: VMContainerInfo{
				OriginalPodName: "virt-launcher-fedora-vm",
				ContainerName:   "compute",
				DomainName:      "default_fedora-vm",
			},
		},
		{
			name: "When --namespace flag is absent but STANDALONE_VMI has namespace it should use env namespace",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-fedora-vm
  annotations:
    kubectl.kubernetes.io/default-container: compute
    kubevirt.io/domain: fedora-vm
spec:
  containers:
  - name: compute
    image: quay.io/kubevirt/virt-launcher:v1.8.0
    command:
    - /usr/bin/virt-launcher-monitor
    - --name
    - fedora-vm
    env:
    - name: STANDALONE_VMI
      value: '{"metadata":{"name":"fedora-vm","namespace":"staging"}}'
`,
			expectedInfo: VMContainerInfo{
				OriginalPodName: "virt-launcher-fedora-vm",
				ContainerName:   "compute",
				DomainName:      "staging_fedora-vm",
			},
		},
		{
			name: "When neither command flag nor STANDALONE_VMI is present it should fall back to default namespace",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-fedora-vm
  annotations:
    kubectl.kubernetes.io/default-container: compute
    kubevirt.io/domain: fedora-vm
spec:
  containers:
  - name: compute
    image: quay.io/kubevirt/virt-launcher:v1.8.0
    command:
    - /usr/bin/virt-launcher-monitor
    - --name
    - fedora-vm
`,
			expectedInfo: VMContainerInfo{
				OriginalPodName: "virt-launcher-fedora-vm",
				ContainerName:   "compute",
				DomainName:      "default_fedora-vm",
			},
		},
		{
			name: "When --namespace has a non-default value it should use that namespace",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-my-vm
  annotations:
    kubectl.kubernetes.io/default-container: compute
    kubevirt.io/domain: my-vm
spec:
  containers:
  - name: compute
    image: quay.io/kubevirt/virt-launcher:v1.8.0
    command:
    - /usr/bin/virt-launcher-monitor
    - --namespace
    - production
    - --name
    - my-vm
`,
			expectedInfo: VMContainerInfo{
				OriginalPodName: "virt-launcher-my-vm",
				ContainerName:   "compute",
				DomainName:      "production_my-vm",
			},
		},
		{
			name: "When default-container annotation names a container absent from spec.containers it should return an error",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-fedora-vm
  annotations:
    kubectl.kubernetes.io/default-container: compute
    kubevirt.io/domain: fedora-vm
spec:
  containers:
  - name: sidecar
    image: quay.io/kubevirt/virt-launcher:v1.8.0
`,
			wantErr:         true,
			wantErrContains: "compute",
		},
		{
			name: "When metadata.name is missing it should return an error",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  annotations:
    kubectl.kubernetes.io/default-container: compute
    kubevirt.io/domain: fedora-vm
spec:
  containers: []
`,
			wantErr:         true,
			wantErrContains: "metadata.name",
		},
		{
			name: "When default-container annotation is missing it should return an error",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-fedora-vm
  annotations:
    kubevirt.io/domain: fedora-vm
spec:
  containers: []
`,
			wantErr:         true,
			wantErrContains: annotationDefaultContainer,
		},
		{
			name: "When kubevirt.io/domain annotation is missing it should return an error",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: virt-launcher-fedora-vm
  annotations:
    kubectl.kubernetes.io/default-container: compute
spec:
  containers: []
`,
			wantErr:         true,
			wantErrContains: annotationKubeVirtDomain,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			info, err := parseVMContainerInfo([]byte(tt.podYAML))
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedInfo, info)
		})
	}
}

func TestParsePodYAMLTargets(t *testing.T) {
	tests := []struct {
		name            string
		podYAML         string
		expectedRefs    []string
		expectedTypes   map[string]dependency.OCIType
		wantErr         bool
		wantErrContains string
	}{
		{
			name:    "When pod has all source fields it should extract targets from volumes initContainers and containers",
			podYAML: samplePodYAML,
			// volumes[].image.reference: fedora:40 (Auto), virt-launcher (Auto)
			// initContainers[].image: fedora:40 — deduplicated (already seen)
			// containers[].image: virt-launcher — deduplicated (already seen)
			expectedRefs: []string{
				"quay.io/containerdisks/fedora:40",
				"quay.io/kubevirt/virt-launcher:v1.8.0",
			},
			expectedTypes: map[string]dependency.OCIType{
				"quay.io/containerdisks/fedora:40":      dependency.OCITypeAuto,
				"quay.io/kubevirt/virt-launcher:v1.8.0": dependency.OCITypeAuto,
			},
		},
		{
			name: "When pod has no volume images it should extract targets from initContainers and containers",
			podYAML: `
apiVersion: v1
kind: Pod
metadata:
  name: regular-app
spec:
  containers:
  - name: app
    image: nginx:latest
  volumes:
  - name: cache
    image:
      reference: redis:7
`,
			expectedRefs: []string{
				"redis:7",
				"nginx:latest",
			},
			expectedTypes: map[string]dependency.OCIType{
				"redis:7":      dependency.OCITypeAuto,
				"nginx:latest": dependency.OCITypePodmanImage,
			},
		},
		{
			name: "When pod has only containers it should extract OCITypePodmanImage targets",
			podYAML: `
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: app
    image: myapp:latest
`,
			expectedRefs: []string{"myapp:latest"},
			expectedTypes: map[string]dependency.OCIType{
				"myapp:latest": dependency.OCITypePodmanImage,
			},
		},
		{
			name: "When pod has only initContainers it should extract OCITypeAuto targets",
			podYAML: `
apiVersion: v1
kind: Pod
spec:
  initContainers:
  - name: init
    image: busybox:latest
`,
			expectedRefs: []string{"busybox:latest"},
			expectedTypes: map[string]dependency.OCIType{
				"busybox:latest": dependency.OCITypeAuto,
			},
		},
		{
			name: "When the same image appears in volumes and containers it should be deduplicated with OCITypeAuto",
			podYAML: `
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: app
    image: shared:latest
  volumes:
  - name: vol
    image:
      reference: shared:latest
`,
			// volumes processed first → OCITypeAuto; containers skipped (already seen)
			expectedRefs: []string{"shared:latest"},
			expectedTypes: map[string]dependency.OCIType{
				"shared:latest": dependency.OCITypeAuto,
			},
		},
		{
			name:            "When pod YAML is malformed it should return an error",
			podYAML:         "this: is: not: valid: yaml: {{{",
			wantErr:         true,
			wantErrContains: "",
		},
		{
			name:         "When pod YAML is empty it should return no targets",
			podYAML:      ``,
			expectedRefs: []string{},
		},
		{
			name: "When pod has containers or volumes with empty image references they should be silently skipped",
			podYAML: `
apiVersion: v1
kind: Pod
spec:
  containers:
  - name: app
    image: ""
  initContainers:
  - name: init
    image: ""
  volumes:
  - name: vol
    image:
      reference: ""
  - name: real
    image:
      reference: nginx:latest
`,
			expectedRefs: []string{"nginx:latest"},
			expectedTypes: map[string]dependency.OCIType{
				"nginx:latest": dependency.OCITypeAuto,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			if !tt.wantErr {
				mockResolver.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			}

			targets, err := parsePodYAMLTargets([]byte(tt.podYAML), mockResolver, v1beta1.CurrentProcessUsername)
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(err)

			refs := make([]string, 0, len(targets))
			for _, tgt := range targets {
				refs = append(refs, tgt.Reference)
			}
			require.ElementsMatch(tt.expectedRefs, refs, "unexpected OCI references")

			for _, tgt := range targets {
				expectedType, ok := tt.expectedTypes[tgt.Reference]
				if ok {
					require.Equal(expectedType, tgt.Type, "unexpected OCIType for %q", tgt.Reference)
				}
				require.Equal(v1beta1.PullIfNotPresent, tgt.PullPolicy, "unexpected pull policy for %q", tgt.Reference)
			}
		})
	}
}

func TestCollectKubePodTargets(t *testing.T) {
	tests := []struct {
		name            string
		kubeContent     string
		inlineContent   []v1beta1.ApplicationContent
		expectedRefs    []string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:        "When kube unit and VM pod YAML are both present it should return OCI targets",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr(samplePodYAML)},
			},
			expectedRefs: []string{
				"quay.io/containerdisks/fedora:40",
				"quay.io/kubevirt/virt-launcher:v1.8.0",
			},
		},
		{
			name:        "When kube unit and non-VM pod YAML are both present it should return OCI targets",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr(nonVMPodYAML)},
			},
			expectedRefs: []string{
				"redis:7",
				"nginx:latest",
			},
		},
		{
			name: "When Yaml= references a subdirectory path it should match by full path not basename",
			kubeContent: `[Kube]
Yaml=subdir/pod.yaml
`,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "subdir/pod.yaml", Content: lo.ToPtr(samplePodYAML)},
				{Path: "pod.yaml", Content: lo.ToPtr(`apiVersion: v1
kind: Pod
spec:
  containers:
  - name: wrong
    image: wrong:image
`)},
			},
			expectedRefs: []string{
				"quay.io/containerdisks/fedora:40",
				"quay.io/kubevirt/virt-launcher:v1.8.0",
			},
		},
		{
			name: "When Yaml= contains a path traversal sequence it should return an error",
			kubeContent: `[Kube]
Yaml=../etc/passwd
`,
			inlineContent:   []v1beta1.ApplicationContent{},
			wantErr:         true,
			wantErrContains: "not a valid relative path",
		},
		{
			name: "When Yaml= contains an absolute path it should return an error",
			kubeContent: `[Kube]
Yaml=/etc/passwd
`,
			inlineContent:   []v1beta1.ApplicationContent{},
			wantErr:         true,
			wantErrContains: "not a valid relative path",
		},
		{
			name:        "When kube unit references a pod YAML not in the inline set it should return an error",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "other.yaml", Content: lo.ToPtr(samplePodYAML)},
			},
			wantErr:         true,
			wantErrContains: "pod.yaml",
		},
		{
			name:            "When kube unit has no Yaml= directive it should return an error",
			kubeContent:     kubeUnitNoYamlKey,
			inlineContent:   []v1beta1.ApplicationContent{},
			wantErr:         true,
			wantErrContains: "Yaml",
		},
		{
			name:        "When kube unit is malformed it should return an error",
			kubeContent: string([]byte{0x00, 0x01, 0x02}),
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr(samplePodYAML)},
			},
			wantErr: true,
		},
		{
			name:        "When pod YAML content is base64-encoded but invalid it should return a decode error",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{
					Path:            "pod.yaml",
					Content:         lo.ToPtr("!!not-valid-base64!!"),
					ContentEncoding: lo.ToPtr(v1beta1.EncodingBase64),
				},
			},
			wantErr:         true,
			wantErrContains: "decoding pod YAML",
		},
		{
			name:        "When pod YAML contains invalid YAML syntax it should return a parse error",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr("{ invalid: yaml: content: [")},
			},
			wantErr:         true,
			wantErrContains: "parsing pod YAML",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockResolver := dependency.NewMockPullConfigResolver(ctrl)
			if !tt.wantErr {
				mockResolver.EXPECT().Options(gomock.Any()).Return(func() []client.ClientOption {
					return nil
				}).AnyTimes()
			}

			targets, err := collectKubePodTargets([]byte(tt.kubeContent), tt.inlineContent, mockResolver, v1beta1.CurrentProcessUsername)
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(err)

			refs := make([]string, 0, len(targets))
			for _, tgt := range targets {
				refs = append(refs, tgt.Reference)
			}
			require.ElementsMatch(tt.expectedRefs, refs)
		})
	}
}
