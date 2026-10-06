package appspec

import (
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestParseQuadletReferencesFromSpec(t *testing.T) {
	tests := []struct {
		name              string
		contents          []v1beta1.ApplicationContent
		expectedImage     *string
		expectedAuxImages []string
		expectError       bool
	}{
		{
			name: "basic container without dropins",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with dropin overriding image",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app.container.d/10-override.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v2.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with multiple dropins - last one wins",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app.container.d/10-first.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				},
				{
					Path: "app.container.d/20-second.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v3.0
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v3.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with dropin adding mount",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app.container.d/10-mount.conf",
					Content: lo.ToPtr(`[Container]
Mount=type=image,source=quay.io/data/dataset:latest,destination=/data
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/dataset:latest"},
		},
		{
			name: "container with dropin adding multiple mounts",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app.container.d/10-mounts.conf",
					Content: lo.ToPtr(`[Container]
Mount=type=image,source=quay.io/data/dataset1:latest,destination=/data1
Mount=type=image,source=quay.io/data/dataset2:latest,destination=/data2
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/dataset1:latest", "quay.io/data/dataset2:latest"},
		},
		{
			name: "container with base mount and dropin adding more mounts",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
Mount=type=image,source=quay.io/data/base:latest,destination=/base
`),
				},
				{
					Path: "app.container.d/10-extra-mounts.conf",
					Content: lo.ToPtr(`[Container]
Mount=type=image,source=quay.io/data/extra1:latest,destination=/extra1
Mount=type=image,source=quay.io/data/extra2:latest,destination=/extra2
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/base:latest", "quay.io/data/extra1:latest", "quay.io/data/extra2:latest"},
		},
		{
			name: "container hierarchy",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app-one.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app-.container.d/10-image.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				},
				{
					Path: "app-one.container.d/10-image.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v4.0
`),
				},
				{
					Path: "container.d/10-image.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v3.0
`),
				},
			},
			expectedImage: lo.ToPtr("quay.io/app/myapp:v4.0"),
		},
		{
			name: "container with dropin overriding image and adding mounts",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app.container.d/10-override.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v2.0
Mount=type=image,source=quay.io/data/dataset:latest,destination=/data
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v2.0"),
			expectedAuxImages: []string{"quay.io/data/dataset:latest"},
		},
		{
			name: "container with multiple dropins in alphabetical order",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path: "app.container.d/30-last.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v4.0
`),
				},
				{
					Path: "app.container.d/10-first.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				},
				{
					Path: "app.container.d/20-second.conf",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v3.0
`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v4.0"),
			expectedAuxImages: nil,
		},
		{
			name: "pod with dropin",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "mypod.pod",
					Content: lo.ToPtr(`[Pod]
`),
				},
				{
					Path: "mypod.pod.d/10-config.conf",
					Content: lo.ToPtr(`[Pod]
Network=host
`),
				},
			},
			expectedImage:     nil,
			expectedAuxImages: nil,
		},
		{
			name: "volume with dropin",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "myvolume.volume",
					Content: lo.ToPtr(`[Volume]
`),
				},
				{
					Path: "myvolume.volume.d/10-config.conf",
					Content: lo.ToPtr(`[Volume]
Label=app=test
`),
				},
			},
			expectedImage:     nil,
			expectedAuxImages: nil,
		},
		{
			name: "non-quadlet files ignored",
			contents: []v1beta1.ApplicationContent{
				{
					Path: "app.container",
					Content: lo.ToPtr(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				},
				{
					Path:    "README.md",
					Content: lo.ToPtr(`This is a readme`),
				},
				{
					Path:    "app.container.d/README.md",
					Content: lo.ToPtr(`This is a dropin readme`),
				},
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			specs, err := ParseQuadletReferencesFromSpec(tt.contents)
			if tt.expectError {
				require.Error(err)
				return
			}

			require.NoError(err)
			require.NotNil(specs)

			require.Len(specs, 1)

			var spec *common.QuadletReferences
			for _, s := range specs {
				spec = s
				break
			}

			if tt.expectedImage != nil {
				require.NotNil(spec.Image)
				require.Equal(*tt.expectedImage, *spec.Image)
			} else {
				require.Nil(spec.Image)
			}

			require.Equal(tt.expectedAuxImages, spec.MountImages)
		})
	}
}
