package client

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/poll"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestPodmanContainerImageVolumes(t *testing.T) {
	const ref = "quay.io/acme/guest:latest"
	id := strings.Repeat("a", 64)
	digest := "sha256:" + strings.Repeat("b", 64)
	tests := []struct {
		name       string
		container  string
		setupMocks func(*executer.MockExecuter, *fileio.MockReadWriter)
		want       []v1beta1.ApplicationVolumeStatus
		wantErr    bool
	}{
		{
			name:      "When a named image volume retains old content it should inspect its immutable backing image",
			container: `{"State":{"Running":true},"Mounts":[{"Type":"volume","Driver":"image","Name":"guest"}]}`,
			setupMocks: func(exec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "--format", "{{json .}}", "guest").Return(`{"StorageID":"backing-id","Options":{"image":"`+ref+`"}}`, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "ps", "--all", "--external", "--no-trunc", "--filter", "id=backing-id", "--format", "{{.ImageID}}").Return(id, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return(digest, "", 0)
			},
			want: []v1beta1.ApplicationVolumeStatus{{Name: "guest", Reference: ref, Digest: digest}},
		},
		{
			name:      "When a mutable VM guest image is mounted it should follow the effective overlay mount",
			container: `{"State":{"Running":true},"OCIConfigPath":"/runtime/config.json","Mounts":[{"Type":"image","Source":"` + ref + `","Destination":"/disk"}]}`,
			setupMocks: func(exec *executer.MockExecuter, rw *fileio.MockReadWriter) {
				rw.EXPECT().ReadFile("/runtime/config.json").Return([]byte(`{"mounts":[{"destination":"/disk","source":"overlay","options":["lowerdir=/storage/old-image/merged"]}]}`), nil)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "mount", "--format", "json").Return(`[{"id":"`+id+`","mountpoint":"/storage/old-image/merged"},{"id":"prepared-new-image","mountpoint":"/storage/new-image/merged"}]`, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return(digest, "", 0)
			},
			want: []v1beta1.ApplicationVolumeStatus{{Name: "/disk", Reference: ref, Digest: digest}},
		},
		{
			name:      "When rootless storage needs unshare it should list existing mounts in its user namespace",
			container: `{"State":{"Running":true},"OCIConfigPath":"/runtime/config.json","Mounts":[{"Type":"image","Source":"` + ref + `","Destination":"/disk"}]}`,
			setupMocks: func(exec *executer.MockExecuter, rw *fileio.MockReadWriter) {
				rw.EXPECT().ReadFile("/runtime/config.json").Return([]byte(`{"mounts":[{"destination":"/disk","source":"/storage/old-image/merged/subdir"}]}`), nil)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "mount", "--format", "json").Return("", "cannot mount using overlay in rootless mode", 125)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "unshare", "podman", "image", "mount", "--format", "json").Return(`[{"id":"`+id+`","mountpoint":"/storage/old-image/merged"}]`, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return(digest, "", 0)
			},
			want: []v1beta1.ApplicationVolumeStatus{{Name: "/disk", Reference: ref, Digest: digest}},
		},
		{
			name:      "When Podman binds a pre-mounted overlay it should follow the running process mount table",
			container: `{"State":{"Running":true,"Pid":42},"OCIConfigPath":"/runtime/config.json","Mounts":[{"Type":"image","Source":"` + ref + `","Destination":"/disk"}]}`,
			setupMocks: func(exec *executer.MockExecuter, rw *fileio.MockReadWriter) {
				rw.EXPECT().ReadFile("/runtime/config.json").Return([]byte(`{"mounts":[{"destination":"/disk","source":"/runtime/overlay/merged"}]}`), nil)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "mount", "--format", "json").Return(`[{"id":"`+id+`","mountpoint":"/storage/old-image/merged"}]`, "", 0)
				rw.EXPECT().ReadFile("/proc/42/mountinfo").Return([]byte("100 1 0:1 / /disk ro - overlay overlay ro,lowerdir=/storage/old-image/merged\n"), nil)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return(digest, "", 0)
			},
			want: []v1beta1.ApplicationVolumeStatus{{Name: "/disk", Reference: ref, Digest: digest}},
		},
		{
			name:      "When runtime identity is unavailable it should not inspect even an immutable configured reference",
			container: fmt.Sprintf(`{"State":{"Running":true},"Mounts":[{"Type":"image","Source":"quay.io/acme/guest@%s","Destination":"/disk"}]}`, digest),
			want:      []v1beta1.ApplicationVolumeStatus{{Name: "/disk", Reference: "quay.io/acme/guest@" + digest}},
		},
		{
			name:      "When a backing image ID is unavailable it should keep the image reference unknown",
			container: `{"State":{"Running":true},"Mounts":[{"Type":"volume","Driver":"image","Name":"guest"}]}`,
			setupMocks: func(exec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "--format", "{{json .}}", "guest").Return(`{"Options":{"image":"`+ref+`"}}`, "", 0)
			},
			want: []v1beta1.ApplicationVolumeStatus{{Name: "guest", Reference: ref}},
		},
		{
			name:      "When the mounted image lacks a manifest digest it should report an unknown digest",
			container: `{"State":{"Running":true},"Mounts":[{"Type":"volume","Driver":"image","Name":"guest"}]}`,
			setupMocks: func(exec *executer.MockExecuter, _ *fileio.MockReadWriter) {
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "volume", "inspect", "--format", "{{json .}}", "guest").Return(`{"StorageID":"backing-id","Options":{"image":"`+ref+`"}}`, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "ps", "--all", "--external", "--no-trunc", "--filter", "id=backing-id", "--format", "{{.ImageID}}").Return(id, "", 0)
				exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "image", "inspect", "--format", "{{.Digest}}", id).Return("", "", 0)
			},
			want: []v1beta1.ApplicationVolumeStatus{{Name: "guest", Reference: ref}}, wantErr: true,
		},
		{
			name:      "When the container is stopped it should omit its image volumes",
			container: `{"State":{"Running":false},"Mounts":[{"Type":"image","Source":"` + ref + `","Destination":"/disk"}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			exec := executer.NewMockExecuter(ctrl)
			rw := fileio.NewMockReadWriter(ctrl)
			exec.EXPECT().ExecuteWithContext(gomock.Any(), "podman", "container", "inspect", "--format", "{{json .}}", "launcher").Return(tt.container, "", 0)
			if tt.setupMocks != nil {
				tt.setupMocks(exec, rw)
			}
			podman := NewPodman(log.NewPrefixLogger("test"), exec, rw, poll.NewConfig(0, 1))
			volumes, err := podman.ContainerImageVolumes(t.Context(), "launcher")
			if tt.wantErr {
				require.Error(err)
			} else {
				require.NoError(err)
			}
			require.Equal(tt.want, volumes)
		})
	}
}

func TestMountedImageIDAmbiguousRuntimeMounts(t *testing.T) {
	mounts := []podmanOCIMount{{Destination: "/disk", Options: []string{"lowerdir=/storage/one/merged:/storage/two/merged"}}}
	images := []podmanImageMount{{ID: "one", Mountpoint: "/storage/one/merged"}, {ID: "two", Mountpoint: "/storage/two/merged"}}
	require.Empty(t, mountedImageID("/disk", mounts, images))
	require.Empty(t, mountedImageID("/other", mounts, images))
}
