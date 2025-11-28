package client

import (
	"testing"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/fileio"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestParseQuadletReferencesFromDir(t *testing.T) {
	require := require.New(t)
	tests := []struct {
		name              string
		files             map[string][]byte
		expectedImage     *string
		expectedAuxImages []string
		expectError       bool
	}{
		{
			name: "basic container without dropins",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with dropin overriding image",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/myapp:v2.0
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v2.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with multiple dropins - last one wins",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/10-first.conf": []byte(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				"app.container.d/20-second.conf": []byte(`[Container]
Image=quay.io/app/myapp:v3.0
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v3.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with dropin adding mount",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/10-mount.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/dataset:latest,destination=/data
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/dataset:latest"},
		},
		{
			name: "container with dropin adding multiple mounts",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/10-mounts.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/dataset1:latest,destination=/data1
Mount=type=image,source=quay.io/data/dataset2:latest,destination=/data2
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/dataset1:latest", "quay.io/data/dataset2:latest"},
		},
		{
			name: "container with base mount and dropin adding more mounts",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
Mount=type=image,source=quay.io/data/base:latest,destination=/base
`),
				"app.container.d/10-extra-mounts.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/extra1:latest,destination=/extra1
Mount=type=image,source=quay.io/data/extra2:latest,destination=/extra2
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/base:latest", "quay.io/data/extra1:latest", "quay.io/data/extra2:latest"},
		},
		{
			name: "container with dropin overriding image and adding mounts",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/myapp:v2.0
Mount=type=image,source=quay.io/data/dataset:latest,destination=/data
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v2.0"),
			expectedAuxImages: []string{"quay.io/data/dataset:latest"},
		},
		{
			name: "container with multiple dropins in alphabetical order",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/30-last.conf": []byte(`[Container]
Image=quay.io/app/myapp:v4.0
`),
				"app.container.d/10-first.conf": []byte(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				"app.container.d/20-second.conf": []byte(`[Container]
Image=quay.io/app/myapp:v3.0
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v4.0"),
			expectedAuxImages: nil,
		},
		{
			name: "container with non-conf files in dropin dir - should be ignored",
			files: map[string][]byte{
				"app.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"app.container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/myapp:v2.0
`),
				"app.container.d/README.md": []byte(`This is a readme`),
				"app.container.d/backup.bak": []byte(`[Container]
Image=quay.io/app/myapp:v3.0
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v2.0"),
			expectedAuxImages: nil,
		},
		{
			name: "pod with dropin",
			files: map[string][]byte{
				"mypod.pod": []byte(`[Pod]
`),
				"mypod.pod.d/10-config.conf": []byte(`[Pod]
Network=host
`),
			},
			expectedImage:     nil,
			expectedAuxImages: nil,
		},
		{
			name: "volume with dropin",
			files: map[string][]byte{
				"myvolume.volume": []byte(`[Volume]
`),
				"myvolume.volume.d/10-config.conf": []byte(`[Volume]
Label=app=test
`),
			},
			expectedImage:     nil,
			expectedAuxImages: nil,
		},
		{
			name: "hierarchical dropins",
			files: map[string][]byte{
				"foo-bar-baz.container": []byte(`[Container]
Image=quay.io/app/base:v1.0
`),
				"container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/toplevel:v1.0
`),
				"foo-bar-baz.container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/foobarbaz:v1.0
`),
				"foo-.container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/foo:v1.0
`),
				"foo-bar-.container.d/10-override.conf": []byte(`[Container]
Image=quay.io/app/foobar:v1.0
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/foobarbaz:v1.0"),
			expectedAuxImages: nil,
		},
		{
			name: "hierarchical dropins with different filenames - all apply",
			files: map[string][]byte{
				"foo-bar-baz.container": []byte(`[Container]
Image=quay.io/app/base:v1.0
`),
				"container.d/10-toplevel.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/toplevel:latest,destination=/toplevel
`),
				"foo-.container.d/20-foo.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/foo:latest,destination=/foo
`),
				"foo-bar-.container.d/30-foobar.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/foobar:latest,destination=/foobar
`),
				"foo-bar-baz.container.d/40-foobarbaz.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/foobarbaz:latest,destination=/foobarbaz
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/base:v1.0"),
			expectedAuxImages: []string{"quay.io/data/toplevel:latest", "quay.io/data/foo:latest", "quay.io/data/foobar:latest", "quay.io/data/foobarbaz:latest"},
		},
		{
			name: "top-level container.d applies to all containers",
			files: map[string][]byte{
				"myapp.container": []byte(`[Container]
Image=quay.io/app/myapp:v1.0
`),
				"container.d/10-common.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/common:latest,destination=/common
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/myapp:v1.0"),
			expectedAuxImages: []string{"quay.io/data/common:latest"},
		},
		{
			name: "dash truncated directories - single dash",
			files: map[string][]byte{
				"my-app.container": []byte(`[Container]
Image=quay.io/app/base:v1.0
`),
				"container.d/10-base.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/base:latest,destination=/base
`),
				"my-.container.d/20-my.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/my:latest,destination=/my
`),
				"my-app.container.d/30-myapp.conf": []byte(`[Container]
Mount=type=image,source=quay.io/data/myapp:latest,destination=/myapp
`),
			},
			expectedImage:     lo.ToPtr("quay.io/app/base:v1.0"),
			expectedAuxImages: []string{"quay.io/data/base:latest", "quay.io/data/my:latest", "quay.io/data/myapp:latest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			readerWriter := fileio.NewReadWriter()
			readerWriter.SetRootdir(tmpDir)

			for filename, content := range tt.files {
				if err := readerWriter.WriteFile(filename, content, fileio.DefaultFilePermissions); err != nil {
					require.NoError(err)
				}
			}

			specs, err := ParseQuadletReferencesFromDir(readerWriter, "/")
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

func TestParseQuadletReferencesFromSpec(t *testing.T) {
	require := require.New(t)
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
