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
		name            string
		data            string
		wantType        QuadletType
		wantImage       *string
		wantMountImages []string
		wantErr         bool
		wantErrType     error
		wantErrSubstr   string
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
		})
	}
}
