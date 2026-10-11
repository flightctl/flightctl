package applications

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/internal/agent/device/deltastatus"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestManagerStatusDoesNotReportDesiredCachedParentDigest(t *testing.T) {
	m := &manager{log: log.NewPrefixLogger("test"), podmanMonitor: &PodmanMonitor{apps: map[string]Application{}}, kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(log.NewPrefixLogger("test"), "test")}, ociTargetCache: provider.NewOCITargetCache()}
	status := &v1beta1.DeviceStatus{}
	require.NoError(t, m.Status(context.Background(), status))
	require.Empty(t, status.Applications)
}

func TestManagerStatusPreservesRunningNestedImageDigests(t *testing.T) {
	for _, runtime := range []string{"podman", "kubernetes"} {
		t.Run("When "+runtime+" reports nested running images it should retain each runtime digest", func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			logger := log.NewPrefixLogger("test")
			exec := executer.NewMockExecuter(ctrl)
			rw := fileio.NewMockReadWriter(ctrl)
			volume, err := provider.NewVolumeManager(logger, "app", v1beta1.AppTypeCompose, v1beta1.CurrentProcessUsername, nil)
			require.NoError(err)
			volume.Add(&provider.Volume{ID: "data", Name: "data", Reference: "quay.io/acme/data:latest", Available: true})
			app := &application{
				id: "app-id", imageRef: "quay.io/acme/web:latest", imageDigest: "sha256:" + strings.Repeat("f", 64),
				status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeCompose}, volume: volume,
			}
			m := &manager{log: logger, podmanMonitor: NewPodmanMonitor(logger, nil, nil, "", nil), kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(logger, "test")}, ociTargetCache: provider.NewOCITargetCache()}
			m.ociTargetCache.Set(provider.CacheEntry{Name: app.ID(), Parent: dependency.OCIPullTarget{Reference: app.imageRef, Digest: app.imageDigest}})
			images := []v1beta1.ApplicationImageDigest{
				{Image: "quay.io/acme/web:latest", Digest: "sha256:" + strings.Repeat("a", 64)},
				{Image: "quay.io/acme/worker:latest", Digest: "sha256:" + strings.Repeat("b", 64)},
				{Image: "quay.io/acme/worker:latest", Digest: "sha256:" + strings.Repeat("c", 64)},
				{Image: "quay.io/acme/unknown:latest"},
			}
			if runtime == "podman" {
				podman := client.NewPodman(logger, exec, rw, poll.NewConfig(0, 1))
				for i, image := range images {
					id := strings.Repeat(string(rune('a'+i)), 64)
					exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "container", "inspect", "--format", "{{.Image}}", id).Return(id, "", 0)
					exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return(image.Digest, "", 0)
					state := StatusRunning
					if i == 1 {
						state = StatusUnhealthy
					}
					m.podmanMonitor.updateApplicationStatus(t.Context(), app, &client.PodmanEvent{ID: id, Name: id, Image: image.Image}, state, 0, podman)
				}
				app.AddWorkload(&Workload{Name: "stopped", Status: StatusExited, Images: []WorkloadImage{{Image: "quay.io/acme/stopped:latest", Digest: app.imageDigest}}})
				m.podmanMonitor.apps[app.ID()] = app
			} else {
				pods := make([]kubernetesPod, len(images))
				for i, image := range images {
					pod := &pods[i]
					pod.Kind = "Pod"
					pod.Metadata.Name = "pod-" + string(rune('a'+i))
					pod.Metadata.Labels = map[string]string{helm.AppLabelKey: app.ID()}
					pod.Status.Phase = podPhasePending
					container := kubernetesContainerSpec{Name: "container", Image: image.Image}
					status := kubernetesContainerStatus{Name: container.Name, State: kubernetesContainerStateInfo{Running: &kubernetesContainerStateRunning{}}}
					if image.Digest != "" {
						status.ImageID = "docker-pullable://" + strings.TrimSuffix(image.Image, ":latest") + "@" + image.Digest
					}
					switch i {
					case 1:
						pod.Spec.InitContainers = []kubernetesContainerSpec{container}
						pod.Status.InitContainerStatuses = []kubernetesContainerStatus{status}
					case 2:
						pod.Spec.EphemeralContainers = []kubernetesContainerSpec{container}
						pod.Status.EphemeralContainerStatuses = []kubernetesContainerStatus{status}
					default:
						pod.Spec.Containers = []kubernetesContainerSpec{container}
						pod.Status.ContainerStatuses = []kubernetesContainerStatus{status}
					}
					pod.Spec.Containers = append(pod.Spec.Containers, kubernetesContainerSpec{Name: "waiting", Image: "quay.io/acme/waiting:latest"})
					pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, kubernetesContainerStatus{Name: "waiting", State: kubernetesContainerStateInfo{Waiting: &kubernetesContainerStateWaiting{}}})
					m.kubernetesMonitor.updatePodStatus(app, pod)
				}
				m.kubernetesMonitor.apps[app.ID()] = app
				watchStatus := &v1beta1.DeviceStatus{}
				require.NoError(m.Status(t.Context(), watchStatus))
				require.Len(watchStatus.Applications, 1)
				require.NotNil(watchStatus.Applications[0].ImageDigests)
				require.ElementsMatch(images, *watchStatus.Applications[0].ImageDigests)

				data, err := json.Marshal(struct {
					Kind  string          `json:"kind"`
					Items []kubernetesPod `json:"items"`
				}{Kind: "PodList", Items: pods})
				require.NoError(err)
				kube := client.NewKube(logger, exec, rw, client.WithBinary("kubectl"), client.WithKubeconfigPath("/config"))
				cri := client.NewCRI(logger, exec, rw, poll.NewConfig(0, 1))
				m.kubernetesMonitor.clients = client.NewCLIClients(client.WithKubeClient(kube), client.WithCRIClient(cri))
				m.deltaStatusStore = deltastatus.NewInMemory(logger)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "get", "pods", "--all-namespaces", "-o", "json", "-l", helm.AppLabelKey, "--kubeconfig", "/config").Return(string(data), "", 0)
				rw.EXPECT().ReadFile("/etc/crictl.yaml").Return([]byte("image-endpoint: unix:///run/containerd.sock\n"), nil)
			}
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(t.Context(), status))
			require.Len(status.Applications, 1)
			require.NotNil(status.Applications[0].ImageDigests)
			require.ElementsMatch(images, *status.Applications[0].ImageDigests)
			if runtime == "podman" {
				require.NotNil(status.Applications[0].Volumes)
				require.Equal("quay.io/acme/data:latest", (*status.Applications[0].Volumes)[0].Reference)
			} else {
				require.Nil(status.Applications[0].Volumes)
			}
		})
	}
}
