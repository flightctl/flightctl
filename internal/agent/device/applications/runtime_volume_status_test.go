package applications

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/helm"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestKubernetesRuntimeImageVolumes(t *testing.T) {
	const ref = "quay.io/acme/data:latest"
	first := "sha256:" + strings.Repeat("a", 64)
	second := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name        string
		state       string
		mountStatus string
		kind        string
		want        []v1beta1.ApplicationVolumeStatus
	}{
		{name: "When a running container reports a mounted image digest it should report the runtime digest", state: `{"running":{}}`, mountStatus: `[{"name":"data","volumeStatus":{"image":{"imageRef":"quay.io/acme/data@` + first + `"}}}]`, want: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref, Digest: first}}},
		{name: "When a running init container mounts an image it should report the runtime volume digest", kind: "init", state: `{"running":{}}`, mountStatus: `[{"name":"data","volumeStatus":{"image":{"imageRef":"quay.io/acme/data@` + first + `"}}}]`, want: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref, Digest: first}}},
		{name: "When a running ephemeral container mounts an image it should report the runtime volume digest", kind: "ephemeral", state: `{"running":{}}`, mountStatus: `[{"name":"data","volumeStatus":{"image":{"imageRef":"quay.io/acme/data@` + first + `"}}}]`, want: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref, Digest: first}}},
		{name: "When an older cluster omits mounted-volume status it should preserve unknown", state: `{"running":{}}`, mountStatus: `[]`, want: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref}}},
		{name: "When a mounted image identity is opaque it should not use the desired digest", state: `{"running":{}}`, mountStatus: `[{"name":"data","volumeStatus":{"image":{"imageRef":"containerd://` + first + `"}}}]`, want: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref}}},
		{name: "When a container is waiting it should omit unused image volumes", state: `{"waiting":{}}`, mountStatus: `[{"name":"data","volumeStatus":{"image":{"imageRef":"quay.io/acme/data@` + first + `"}}}]`},
		{name: "When a container has terminated it should omit unused image volumes", state: `{"terminated":{"exitCode":0}}`, mountStatus: `[{"name":"data","volumeStatus":{"image":{"imageRef":"quay.io/acme/data@` + first + `"}}}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			var pod kubernetesPod
			data := fmt.Sprintf(`{"kind":"Pod","metadata":{"name":"pod","labels":{"%s":"app"}},"spec":{"containers":[{"name":"container","image":"quay.io/acme/app:latest","volumeMounts":[{"name":"data"},{"name":"local"}]}],"volumes":[{"name":"data","image":{"reference":"%s"}},{"name":"local","emptyDir":{}},{"name":"unused","image":{"reference":"quay.io/acme/unused@%s"}}]},"status":{"phase":"Running","containerStatuses":[{"name":"container","state":%s,"volumeMounts":%s}]}}`, helm.AppLabelKey, ref, second, tt.state, tt.mountStatus)
			require.NoError(json.Unmarshal([]byte(data), &pod))
			switch tt.kind {
			case "init":
				pod.Spec.InitContainers, pod.Spec.Containers = pod.Spec.Containers, nil
				pod.Status.InitContainerStatuses, pod.Status.ContainerStatuses = pod.Status.ContainerStatuses, nil
			case "ephemeral":
				pod.Spec.EphemeralContainers, pod.Spec.Containers = pod.Spec.Containers, nil
				pod.Status.EphemeralContainerStatuses, pod.Status.ContainerStatuses = pod.Status.ContainerStatuses, nil
			}
			encoded, err := json.Marshal(&pod)
			require.NoError(err)
			data = string(encoded)
			require.Equal(tt.want, collectPodImageVolumes(&pod))
			app := &application{id: "app", status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: v1beta1.AppTypeHelm}, volume: mustNewVolumeManager(t), imageDigest: second}
			monitor := &KubernetesMonitor{monitor: newMonitor(log.NewPrefixLogger("test"), "test")}
			monitor.updatePodStatus(app, &pod)
			status, _, err := app.Status()
			require.NoError(err)
			if len(tt.want) > 0 {
				require.NotNil(status.Volumes)
				require.Equal(tt.want, *status.Volumes)
				require.NotNil(status.ImageDigests)
				require.Contains(*status.ImageDigests, v1beta1.ApplicationImageDigest{Image: ref, Digest: tt.want[0].Digest})
			} else {
				require.Nil(status.Volumes)
			}
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec, rw := executer.NewMockExecuter(ctrl), fileio.NewMockReadWriter(ctrl)
			kube := client.NewKube(log.NewPrefixLogger("test"), exec, rw, client.WithBinary("kubectl"), client.WithKubeconfigPath("/config"))
			cri := client.NewCRI(log.NewPrefixLogger("test"), exec, rw, poll.NewConfig(0, 1))
			monitor.clients = client.NewCLIClients(client.WithKubeClient(kube), client.WithCRIClient(cri))
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "get", "pods", "--all-namespaces", "-o", "json", "-l", helm.AppLabelKey, "--kubeconfig", "/config").Return(`{"kind":"PodList","items":[`+data+`]}`, "", 0)
			rw.EXPECT().ReadFile("/etc/crictl.yaml").Return([]byte("image-endpoint: unix:///run/containerd.sock\n"), nil)
			results := []AppStatusResult{{ID: "app", Status: *status}}
			monitor.currentImageObservations(t.Context(), results)
			require.True(results[0].ImagesComplete)
			if len(tt.want) > 0 {
				require.Equal(tt.want, *results[0].Status.Volumes)
				require.Contains(*results[0].Status.ImageDigests, v1beta1.ApplicationImageDigest{Image: ref, Digest: tt.want[0].Digest})
			} else {
				require.Nil(results[0].Status.Volumes)
			}

			// A complete empty snapshot must remove stale volume identities even
			// when the watch has not delivered the pod's deletion event.
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "kubectl", "get", "pods", "--all-namespaces", "-o", "json", "-l", helm.AppLabelKey, "--kubeconfig", "/config").Return(`{"kind":"PodList","items":[]}`, "", 0)
			rw.EXPECT().ReadFile("/etc/crictl.yaml").Return([]byte("image-endpoint: unix:///run/containerd.sock\n"), nil)
			monitor.currentImageObservations(t.Context(), results)
			require.Empty(*results[0].Status.ImageDigests)
			require.Nil(results[0].Status.Volumes)
		})
	}
}

func TestApplicationVolumeImageDigestsPreserveOverlappingRuntimeImages(t *testing.T) {
	const ref = "quay.io/acme/guest:latest"
	first := "sha256:" + strings.Repeat("a", 64)
	second := "sha256:" + strings.Repeat("b", 64)
	for _, appType := range []v1beta1.AppType{v1beta1.AppTypeContainer, v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeVm, v1beta1.AppTypeHelm} {
		t.Run("When "+string(appType)+" uses overlapping volume images it should preserve every runtime observation", func(t *testing.T) {
			app := &application{status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: appType}, volume: mustNewVolumeManager(t), workloads: []Workload{
				{Status: StatusRunning, Volumes: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref, Digest: first}, {Name: "data", Reference: ref, Digest: first}}},
				{Status: StatusUnhealthy, Volumes: []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref, Digest: second}, {Name: "data", Reference: ref}}},
				{Status: StatusExited, Volumes: []v1beta1.ApplicationVolumeStatus{{Name: "stopped", Reference: "quay.io/acme/stopped:latest", Digest: first}}},
			}}
			status, _, err := app.Status()
			require.NoError(t, err)
			require.ElementsMatch(t, []v1beta1.ApplicationImageDigest{{Image: ref, Digest: first}, {Image: ref, Digest: second}, {Image: ref}}, *status.ImageDigests)
			require.ElementsMatch(t, []v1beta1.ApplicationVolumeStatus{{Name: "data", Reference: ref, Digest: first}, {Name: "data", Reference: ref, Digest: second}, {Name: "data", Reference: ref}}, *status.Volumes)
			app.ClearWorkloads()
			status, _, err = app.Status()
			require.NoError(t, err)
			require.Nil(t, status.ImageDigests)
			require.Nil(t, status.Volumes)
		})
	}
}

func TestManagerRuntimeVolumeReportingForPodmanAppTypes(t *testing.T) {
	const ref = "quay.io/acme/guest:latest"
	digest := "sha256:" + strings.Repeat("a", 64)
	id := strings.Repeat("b", 64)
	for _, appType := range []v1beta1.AppType{v1beta1.AppTypeContainer, v1beta1.AppTypeCompose, v1beta1.AppTypeQuadlet, v1beta1.AppTypeVm} {
		t.Run("When "+string(appType)+" runs an image-backed volume it should report the runtime digest", func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			logger := log.NewPrefixLogger("test")
			exec, rw := executer.NewMockExecuter(ctrl), fileio.NewMockReadWriter(ctrl)
			podman := client.NewPodman(logger, exec, rw, poll.NewConfig(0, 1))
			app := &application{id: "app", status: &v1beta1.DeviceApplicationStatus{Name: "app", AppType: appType, RunAs: "owner"}, volume: mustNewVolumeManager(t), workloads: []Workload{{ID: "launcher", Name: "launcher", Status: StatusRunning, Images: []WorkloadImage{{Image: "quay.io/acme/launcher:latest", Digest: digest}}}}}
			factory := func(user v1beta1.Username) (*client.Podman, error) {
				require.Equal(v1beta1.Username("owner"), user)
				return podman, nil
			}
			m := &manager{podmanMonitor: NewPodmanMonitor(logger, factory, nil, "", nil), kubernetesMonitor: &KubernetesMonitor{monitor: newMonitor(logger, "test")}}
			m.podmanMonitor.apps[app.ID()] = app
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "container", "inspect", "--format", "{{json .}}", "launcher").Return(`{"State":{"Running":true},"OCIConfigPath":"/runtime/config.json","Mounts":[{"Type":"image","Source":"`+ref+`","Destination":"/disk"}]}`, "", 0)
			rw.EXPECT().ReadFile("/runtime/config.json").Return([]byte(`{"mounts":[{"destination":"/disk","source":"overlay","options":["lowerdir=/storage/guest/merged"]}]}`), nil)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "mount", "--format", "json").Return(`[{"id":"`+id+`","mountpoint":"/storage/guest/merged"}]`, "", 0)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return(digest, "", 0)
			status := &v1beta1.DeviceStatus{}
			require.NoError(m.Status(t.Context(), status))
			require.Len(status.Applications, 1)
			require.Contains(*status.Applications[0].ImageDigests, v1beta1.ApplicationImageDigest{Image: ref, Digest: digest})
			require.Equal([]v1beta1.ApplicationVolumeStatus{{Name: "/disk", Reference: ref, Digest: digest}}, *status.Applications[0].Volumes)
		})
	}
}
