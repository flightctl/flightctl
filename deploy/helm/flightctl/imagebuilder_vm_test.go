package flightctl_test

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

func renderVMWorker(t *testing.T, values map[string]string) (*appsv1.Deployment, []renderedResource) {
	t.Helper()
	resources, output, err := renderNamespaceScopedChart(t, values)
	require.NoError(t, err, output)
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(output), 4096)
	for {
		var deployment appsv1.Deployment
		err := decoder.Decode(&deployment)
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if deployment.Kind == "Deployment" && deployment.Name == "flightctl-imagebuilder-worker" {
			return &deployment, resources
		}
	}
	t.Fatal("imagebuilder worker Deployment is missing")
	return nil, nil
}

func TestImageBuilderVMConfiguration(t *testing.T) {
	t.Run("When guest sizing and a KVM resource are configured it should reserve and pass them to the worker", func(t *testing.T) {
		deployment, resources := renderVMWorker(t, map[string]string{
			"global.auth.type":                       "aap",
			"imageBuilderWorker.vm.baseImage":        "/usr/share/flightctl-vm/custom.qcow2",
			"imageBuilderWorker.vm.memoryMiB":        "6144",
			"imageBuilderWorker.vm.cpus":             "3",
			"imageBuilderWorker.vm.diskSizeGiB":      "32",
			"imageBuilderWorker.vm.bootTimeout":      "7m",
			"imageBuilderWorker.vm.executionTimeout": "3h",
			"imageBuilderWorker.vm.kvmResourceName":  "example.com/kvm",
			"imageBuilderWorker.maxConcurrentBuilds": "2",
		})
		worker := deployment.Spec.Template.Spec.Containers[0]
		requestedKVM := worker.Resources.Requests["example.com/kvm"]
		limitedKVM := worker.Resources.Limits["example.com/kvm"]
		require.Equal(t, int64(2), requestedKVM.Value())
		require.Equal(t, int64(2), limitedKVM.Value())
		require.Equal(t, int64(6), worker.Resources.Requests.Cpu().Value())
		require.GreaterOrEqual(t, worker.Resources.Requests.Memory().Value(), int64(13*1024*1024*1024))
		require.Equal(t, "amd64", deployment.Spec.Template.Spec.NodeSelector["kubernetes.io/arch"])
		configs := findResources(resources, "ConfigMap", "flightctl-imagebuilder-worker-config")
		require.Len(t, configs, 1)
		var config struct {
			Worker struct {
				VM struct {
					BaseImage        string `yaml:"baseImage"`
					Accelerator      string `yaml:"accelerator"`
					MemoryMiB        int    `yaml:"memoryMiB"`
					CPUs             int    `yaml:"cpus"`
					DiskSizeGiB      int    `yaml:"diskSizeGiB"`
					BootTimeout      string `yaml:"bootTimeout"`
					ExecutionTimeout string `yaml:"executionTimeout"`
				} `yaml:"vm"`
			} `yaml:"imageBuilderWorker"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(configs[0].Data["config.yaml"]), &config))
		require.Equal(t, "/usr/share/flightctl-vm/custom.qcow2", config.Worker.VM.BaseImage)
		require.Equal(t, "kvm", config.Worker.VM.Accelerator)
		require.Equal(t, 6144, config.Worker.VM.MemoryMiB)
		require.Equal(t, 3, config.Worker.VM.CPUs)
		require.Equal(t, 32, config.Worker.VM.DiskSizeGiB)
		require.Equal(t, "7m", config.Worker.VM.BootTimeout)
		require.Equal(t, "3h", config.Worker.VM.ExecutionTimeout)
	})
}

func TestImageBuilderVMRestrictedWorkload(t *testing.T) {
	t.Run("When the VM worker is enabled in a namespace it should run with restricted privileges", func(t *testing.T) {
		deployment, resources := renderVMWorker(t, map[string]string{
			"global.namespaceScoped": "true",
			"global.auth.type":       "aap",
			"deltaWorker.enabled":    "false",
		})
		pod := deployment.Spec.Template.Spec
		require.NotNil(t, pod.SecurityContext)
		require.NotNil(t, pod.SecurityContext.RunAsNonRoot)
		require.True(t, *pod.SecurityContext.RunAsNonRoot)
		require.NotNil(t, pod.SecurityContext.SeccompProfile)
		require.Equal(t, corev1.SeccompProfileTypeRuntimeDefault, pod.SecurityContext.SeccompProfile.Type)
		for _, container := range append(pod.Containers, pod.InitContainers...) {
			require.NotNil(t, container.SecurityContext, container.Name)
			security := container.SecurityContext
			require.NotNil(t, security.AllowPrivilegeEscalation, container.Name)
			require.False(t, *security.AllowPrivilegeEscalation, container.Name)
			require.NotNil(t, security.ReadOnlyRootFilesystem, container.Name)
			require.True(t, *security.ReadOnlyRootFilesystem, container.Name)
			require.True(t, security.Privileged == nil || !*security.Privileged, container.Name)
			require.NotNil(t, security.Capabilities, container.Name)
			require.Equal(t, []corev1.Capability{"ALL"}, security.Capabilities.Drop, container.Name)
			require.Empty(t, security.Capabilities.Add, container.Name)
		}
		for _, volume := range pod.Volumes {
			require.Nil(t, volume.HostPath, volume.Name)
		}
		require.Empty(t, findResources(resources, "SecurityContextConstraints", "flightctl-imagebuilder-worker"))
		worker := pod.Containers[0]
		require.Contains(t, worker.Env, corev1.EnvVar{Name: "HOME", Value: "/var/lib/flightctl"})
		require.Contains(t, worker.Env, corev1.EnvVar{Name: "TMPDIR", Value: "/tmp"})
	})
}

func TestImageBuilderVMScratchBudget(t *testing.T) {
	t.Run("When a scratch capacity is configured it should bound the disposable VM workspace", func(t *testing.T) {
		deployment, _ := renderVMWorker(t, map[string]string{
			"global.auth.type":                       "aap",
			"imageBuilderWorker.vm.scratchSizeLimit": "80Gi",
		})
		for _, volume := range deployment.Spec.Template.Spec.Volumes {
			if volume.Name == "worker-scratch" {
				require.NotNil(t, volume.EmptyDir)
				require.NotNil(t, volume.EmptyDir.SizeLimit)
				require.Equal(t, int64(80*1024*1024*1024), volume.EmptyDir.SizeLimit.Value())
				return
			}
		}
		t.Fatal("worker scratch volume is missing")
	})
}

func TestImageBuilderVMInitWritablePaths(t *testing.T) {
	t.Run("When wait containers run with a read-only root it should provide a writable home and temporary directory", func(t *testing.T) {
		deployment, _ := renderVMWorker(t, map[string]string{"global.auth.type": "aap"})
		for _, container := range deployment.Spec.Template.Spec.InitContainers {
			require.Contains(t, container.Env, corev1.EnvVar{Name: "HOME", Value: "/tmp"}, container.Name)
			require.Contains(t, container.VolumeMounts, corev1.VolumeMount{Name: "worker-scratch", MountPath: "/tmp"}, container.Name)
		}
	})
}
