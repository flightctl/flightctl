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

// nonVMPodYAML is a pod YAML that has no virt-launcher image.
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

const sampleKubeUnit = `[Kube]
Yaml=pod.yaml
`

const kubeUnitNoYamlKey = `[Kube]
KubeDownForce=false
`

func TestIsVMWorkload(t *testing.T) {
	tests := []struct {
		name     string
		pod      *podSpec
		expected bool
	}{
		{
			name: "When a volume image reference matches virt-launcher prefix it should return true",
			pod: &podSpec{
				Spec: podSpecInner{
					Volumes: []podVolume{
						{Image: &podVolumeImage{Reference: "quay.io/kubevirt/virt-launcher:v1.8.0"}},
					},
				},
			},
			expected: true,
		},
		{
			name: "When a container image matches virt-launcher prefix it should return true",
			pod: &podSpec{
				Spec: podSpecInner{
					Containers: []podContainer{
						{Image: "quay.io/kubevirt/virt-launcher:v1.8.0"},
					},
				},
			},
			expected: true,
		},
		{
			name: "When no image matches virt-launcher prefix it should return false",
			pod: &podSpec{
				Spec: podSpecInner{
					Containers: []podContainer{
						{Image: "nginx:latest"},
					},
					Volumes: []podVolume{
						{Image: &podVolumeImage{Reference: "redis:7"}},
					},
				},
			},
			expected: false,
		},
		{
			name: "When pod has no containers or volumes it should return false",
			pod: &podSpec{
				Spec: podSpecInner{},
			},
			expected: false,
		},
		{
			name: "When volume has no image it should not panic and should return false",
			pod: &podSpec{
				Spec: podSpecInner{
					Volumes: []podVolume{
						{Image: nil},
					},
				},
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tt.expected, isVMWorkload(tt.pod))
		})
	}
}

func TestParsePodYAMLTargets(t *testing.T) {
	tests := []struct {
		name            string
		podYAML         string
		expectedRefs    []string
		expectedTypes   map[string]dependency.OCIType
		expectedIsVM    bool
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
			expectedIsVM: true,
		},
		{
			name:    "When pod has no virt-launcher image it should not be classified as a VM",
			podYAML: nonVMPodYAML,
			expectedRefs: []string{
				"redis:7",
				"nginx:latest",
			},
			expectedTypes: map[string]dependency.OCIType{
				"redis:7":      dependency.OCITypeAuto,
				"nginx:latest": dependency.OCITypePodmanImage,
			},
			expectedIsVM: false,
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
			expectedIsVM: false,
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
			expectedIsVM: false,
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
			expectedIsVM: false,
		},
		{
			name:            "When pod YAML is malformed it should return an error",
			podYAML:         "this: is: not: valid: yaml: {{{",
			wantErr:         true,
			wantErrContains: "",
		},
		{
			name:         "When pod YAML is empty it should return no targets and no VM",
			podYAML:      ``,
			expectedRefs: []string{},
			expectedIsVM: false,
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
			expectedIsVM: false,
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

			targets, isVM, err := parsePodYAMLTargets([]byte(tt.podYAML), mockResolver, v1beta1.CurrentProcessUsername)
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedIsVM, isVM)

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
		expectedIsVM    bool
		wantErr         bool
		wantErrContains string
	}{
		{
			name:        "When kube unit and VM pod YAML are both present it should return OCI targets and isVM=true",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr(samplePodYAML)},
			},
			expectedRefs: []string{
				"quay.io/containerdisks/fedora:40",
				"quay.io/kubevirt/virt-launcher:v1.8.0",
			},
			expectedIsVM: true,
		},
		{
			name:        "When kube unit and non-VM pod YAML are both present it should return OCI targets and isVM=false",
			kubeContent: sampleKubeUnit,
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr(nonVMPodYAML)},
			},
			expectedRefs: []string{
				"redis:7",
				"nginx:latest",
			},
			expectedIsVM: false,
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

			targets, isVM, err := collectKubePodTargets([]byte(tt.kubeContent), tt.inlineContent, mockResolver, v1beta1.CurrentProcessUsername)
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedIsVM, isVM)

			refs := make([]string, 0, len(targets))
			for _, tgt := range targets {
				refs = append(refs, tgt.Reference)
			}
			require.ElementsMatch(tt.expectedRefs, refs)
		})
	}
}

func TestDetectVMWorkload(t *testing.T) {
	tests := []struct {
		name            string
		inlineContent   []v1beta1.ApplicationContent
		expectedIsVM    bool
		wantErr         bool
		wantErrContains string
	}{
		{
			name: "When inline content contains a .kube file referencing a VM pod YAML it should return true",
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "app.kube", Content: lo.ToPtr(sampleKubeUnit)},
				{Path: "pod.yaml", Content: lo.ToPtr(samplePodYAML)},
			},
			expectedIsVM: true,
		},
		{
			name: "When inline content contains a .kube file referencing a non-VM pod YAML it should return false",
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "app.kube", Content: lo.ToPtr(sampleKubeUnit)},
				{Path: "pod.yaml", Content: lo.ToPtr(nonVMPodYAML)},
			},
			expectedIsVM: false,
		},
		{
			name: "When inline content has no .kube file it should return false",
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "pod.yaml", Content: lo.ToPtr(samplePodYAML)},
			},
			expectedIsVM: false,
		},
		{
			name: "When .kube file references a pod YAML absent from inline content it should return an error",
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "app.kube", Content: lo.ToPtr(sampleKubeUnit)},
			},
			wantErr:         true,
			wantErrContains: "pod.yaml",
		},
		{
			name: "When .kube file has no Yaml= directive it should return an error",
			inlineContent: []v1beta1.ApplicationContent{
				{Path: "app.kube", Content: lo.ToPtr(kubeUnitNoYamlKey)},
			},
			wantErr:         true,
			wantErrContains: "Yaml",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			isVM, err := detectVMWorkload(tt.inlineContent)
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrContains != "" {
					require.Contains(err.Error(), tt.wantErrContains)
				}
				return
			}
			require.NoError(err)
			require.Equal(tt.expectedIsVM, isVM)
		})
	}
}
