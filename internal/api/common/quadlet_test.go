package common

import (
	"errors"
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestParseQuadletSpec(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name             string
		data             string
		wantType         QuadletType
		wantImage        *string
		wantMountImages  []string
		wantVolumes      []string
		wantMountVolumes []string
		wantNetworks     []string
		wantPods         []string
		wantName         *string
		wantErr          bool
		wantErrType      error
		wantErrSubstr    string
	}{
		// Valid parsing tests
		{
			name: "valid container with image",
			data: `[Container]
Image=quay.io/podman/hello:latest
PublishPort=8080:80`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
		},
		{
			name: "valid container without image",
			data: `[Container]
PublishPort=8080:80`,
			wantType:  QuadletTypeContainer,
			wantImage: nil,
		},
		{
			name: "valid volume with image",
			data: `[Volume]
Image=quay.io/containers/volume:latest`,
			wantType:  QuadletTypeVolume,
			wantImage: lo.ToPtr("quay.io/containers/volume:latest"),
		},
		{
			name: "valid volume without image",
			data: `[Volume]
Device=/dev/sda1`,
			wantType:  QuadletTypeVolume,
			wantImage: nil,
		},
		{
			name: "valid network",
			data: `[Network]
Subnet=10.0.0.0/24`,
			wantType:  QuadletTypeNetwork,
			wantImage: nil,
		},
		{
			name: "valid image with image key",
			data: `[Image]
Image=quay.io/fedora/fedora:latest`,
			wantType:  QuadletTypeImage,
			wantImage: lo.ToPtr("quay.io/fedora/fedora:latest"),
		},
		{
			name: "valid pod",
			data: `[Pod]
PodName=my-pod`,
			wantType:  QuadletTypePod,
			wantImage: nil,
			wantName:  lo.ToPtr("my-pod"),
		},
		{
			name: "valid container with unit section",
			data: `[Unit]
Description=My container

[Container]
Image=quay.io/app/myapp:v1.0`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/app/myapp:v1.0"),
		},
		{
			name:        "invalid INI format",
			data:        `[Container\nImage=test`,
			wantErr:     true,
			wantErrType: nil,
		},
		{
			name: "multiple type sections",
			data: `[Container]
Image=test

[Volume]
Device=/dev/sda1`,
			wantErr:       true,
			wantErrSubstr: "multiple quadlet type sections",
		},
		{
			name: "unsupported type: Build",
			data: `[Build]
ContextDir=/tmp/build`,
			wantErr:       true,
			wantErrType:   ErrUnsupportedQuadletType,
			wantErrSubstr: "Build",
		},
		{
			name: "unsupported type: Kube",
			data: `[Kube]
Yaml=/path/to/kube.yaml`,
			wantErr:       true,
			wantErrType:   ErrUnsupportedQuadletType,
			wantErrSubstr: "Kube",
		},
		{
			name: "unsupported type: Artifact",
			data: `[Artifact]
Source=/path/to/artifact`,
			wantErr:       true,
			wantErrType:   ErrUnsupportedQuadletType,
			wantErrSubstr: "Artifact",
		},
		{
			name: "no recognized type section",
			data: `[Unit]
Description=Some unit`,
			wantErr:     true,
			wantErrType: ErrNonQuadletType,
		},
		{
			name: "non-quadlet systemd file",
			data: `[Service]
Type=simple
ExecStart=/usr/bin/myapp`,
			wantErr:     true,
			wantErrType: ErrNonQuadletType,
		},
		{
			name: "container with build section",
			data: `[Container]
Image=test

[Build]
ContextDir=/tmp`,
			wantErr:       true,
			wantErrType:   ErrUnsupportedQuadletType,
			wantErrSubstr: "Build",
		},
		{
			name: "container with single image mount",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=image,source=quay.io/containers/data:v1,destination=/data`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: []string{"quay.io/containers/data:v1"},
		},
		{
			name: "container with multiple image mounts",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=image,source=quay.io/containers/data:v1,destination=/data
Mount=type=image,source=quay.io/containers/cache:latest,destination=/cache`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: []string{"quay.io/containers/data:v1", "quay.io/containers/cache:latest"},
		},
		{
			name: "container with mixed mount types",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=image,source=quay.io/containers/data:v1,destination=/data
Mount=type=volume,source=my-volume,destination=/vol`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: []string{"quay.io/containers/data:v1"},
		},
		{
			name: "container with volume mount only",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=volume,source=my-volume,destination=/data`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: nil,
		},
		{
			name: "container with mount but no type specified",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=source=my-volume,destination=/data`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: nil,
		},
		{
			name: "container with Image and mount images",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=image,source=quay.io/containers/data:v1,destination=/data`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: []string{"quay.io/containers/data:v1"},
		},
		{
			name: "container with multiple Mount keys referencing different images",
			data: `[Container]
Image=quay.io/app/myapp:v1.0
Mount=type=image,source=quay.io/containers/db:v2,destination=/db
Mount=type=image,source=quay.io/containers/cache:v3,destination=/cache
Mount=type=volume,source=logs,destination=/logs`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/app/myapp:v1.0"),
			wantMountImages: []string{"quay.io/containers/db:v2", "quay.io/containers/cache:v3"},
		},
		{
			name: "container with image mount using src instead of source",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=image,src=quay.io/containers/data:v1,destination=/data`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountImages: []string{"quay.io/containers/data:v1"},
		},
		{
			name: "container with volume reference",
			data: `[Container]
Image=quay.io/podman/hello:latest
Volume=data.volume:/data`,
			wantType:    QuadletTypeContainer,
			wantImage:   lo.ToPtr("quay.io/podman/hello:latest"),
			wantVolumes: []string{"data.volume"},
		},
		{
			name: "container with multiple volume references",
			data: `[Container]
Image=quay.io/podman/hello:latest
Volume=data.volume:/data
Volume=logs.volume:/logs:ro`,
			wantType:    QuadletTypeContainer,
			wantImage:   lo.ToPtr("quay.io/podman/hello:latest"),
			wantVolumes: []string{"data.volume", "logs.volume"},
		},
		{
			name: "container with named volume (not a reference)",
			data: `[Container]
Image=quay.io/podman/hello:latest
Volume=my-named-volume:/data`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
		},
		{
			name: "container with host path (not a reference)",
			data: `[Container]
Image=quay.io/podman/hello:latest
Volume=/host/path:/container/path`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
		},
		{
			name: "container with network reference",
			data: `[Container]
Image=quay.io/podman/hello:latest
Network=mynet.network`,
			wantType:     QuadletTypeContainer,
			wantImage:    lo.ToPtr("quay.io/podman/hello:latest"),
			wantNetworks: []string{"mynet.network"},
		},
		{
			name: "container with built-in network mode (not a reference)",
			data: `[Container]
Image=quay.io/podman/hello:latest
Network=host`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
		},
		{
			name: "container with bridge network (not a reference)",
			data: `[Container]
Image=quay.io/podman/hello:latest
Network=bridge`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
		},
		{
			name: "container with pod reference",
			data: `[Container]
Image=quay.io/podman/hello:latest
Pod=mypod.pod`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
			wantPods:  []string{"mypod.pod"},
		},
		{
			name: "container with volume mount reference",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=volume,source=cache.volume,destination=/cache`,
			wantType:         QuadletTypeContainer,
			wantImage:        lo.ToPtr("quay.io/podman/hello:latest"),
			wantMountVolumes: []string{"cache.volume"},
		},
		{
			name: "container with volume mount named volume (not a reference)",
			data: `[Container]
Image=quay.io/podman/hello:latest
Mount=type=volume,source=my-cache,destination=/cache`,
			wantType:  QuadletTypeContainer,
			wantImage: lo.ToPtr("quay.io/podman/hello:latest"),
		},
		{
			name: "container with all reference types",
			data: `[Container]
Image=myimage.image
Volume=data.volume:/data
Network=mynet.network
Pod=mypod.pod
Mount=type=image,source=extra.image,destination=/extra
Mount=type=volume,source=cache.volume,destination=/cache`,
			wantType:         QuadletTypeContainer,
			wantImage:        lo.ToPtr("myimage.image"),
			wantVolumes:      []string{"data.volume"},
			wantNetworks:     []string{"mynet.network"},
			wantPods:         []string{"mypod.pod"},
			wantMountImages:  []string{"extra.image"},
			wantMountVolumes: []string{"cache.volume"},
		},
		{
			name: "pod with volume and network references",
			data: `[Pod]
PodName=my-pod
Volume=shared.volume:/shared
Network=backend.network`,
			wantType:     QuadletTypePod,
			wantVolumes:  []string{"shared.volume"},
			wantNetworks: []string{"backend.network"},
			wantName:     lo.ToPtr("my-pod"),
		},
		{
			name: "volume with image reference",
			data: `[Volume]
Image=myimage.image`,
			wantType:  QuadletTypeVolume,
			wantImage: lo.ToPtr("myimage.image"),
		},
		{
			name: "container with custom name",
			data: `[Container]
Image=quay.io/app/myapp:v1
ContainerName=my-container`,
			wantType:        QuadletTypeContainer,
			wantImage:       lo.ToPtr("quay.io/app/myapp:v1"),
			wantMountImages: nil,
			wantName:        lo.ToPtr("my-container"),
		},
		{
			name: "volume with custom name",
			data: `[Volume]
VolumeName=data-store
Driver=local`,
			wantType: QuadletTypeVolume,
			wantName: lo.ToPtr("data-store"),
		},
		{
			name: "network with custom name",
			data: `[Network]
NetworkName=mesh-net`,
			wantType: QuadletTypeNetwork,
			wantName: lo.ToPtr("mesh-net"),
		},
		{
			name: "pod with custom name",
			data: `[Pod]
PodName=pod-one`,
			wantType: QuadletTypePod,
			wantName: lo.ToPtr("pod-one"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseQuadletReferences([]byte(tt.data))

			if tt.wantErr {
				require.Error(err)
				if tt.wantErrType != nil {
					require.True(errors.Is(err, tt.wantErrType), "expected error type %v, got %v", tt.wantErrType, err)
				}
				if tt.wantErrSubstr != "" {
					require.Contains(err.Error(), tt.wantErrSubstr)
				}
				return
			}

			require.NoError(err)
			require.NotNil(spec)
			require.Equal(tt.wantType, spec.Type)

			if tt.wantImage != nil {
				require.NotNil(spec.Image)
				require.Equal(*tt.wantImage, *spec.Image)
			} else {
				require.Nil(spec.Image)
			}

			require.Equal(tt.wantMountImages, spec.MountImages)
			require.Equal(tt.wantVolumes, spec.Volumes)
			require.Equal(tt.wantMountVolumes, spec.MountVolumes)
			require.Equal(tt.wantNetworks, spec.Networks)
			require.Equal(tt.wantPods, spec.Pods)
			if tt.wantName != nil {
				require.NotNil(spec.Name)
				require.Equal(*tt.wantName, *spec.Name)
			} else {
				require.Nil(spec.Name)
			}
		})
	}
}
