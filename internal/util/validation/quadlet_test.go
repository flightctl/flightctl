package validation

import (
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestValidateQuadletReferences(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		path          string
		spec          *common.QuadletReferences
		wantErrCount  int
		wantErrSubstr string
	}{
		{
			name: "container with no image",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: nil,
			},
			wantErrCount:  1,
			wantErrSubstr: "must have an Image key",
		},
		{
			name: "container with valid OCI reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("quay.io/podman/hello:latest"),
			},
			wantErrCount: 0,
		},
		{
			name: "container with .image reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("my-app.image"),
			},
			wantErrCount: 0,
		},
		{
			name: "container with .build reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("my-app.build"),
			},
			wantErrCount:  1,
			wantErrSubstr: ".build quadlet types are unsupported",
		},
		{
			name: "container with invalid OCI reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("nginx:latest"),
			},
			wantErrCount:  1,
			wantErrSubstr: "container.image",
		},
		{
			name: "volume with no image",
			path: "data.volume",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeVolume,
				Image: nil,
			},
			wantErrCount: 0,
		},
		{
			name: "volume with valid OCI reference",
			path: "data.volume",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeVolume,
				Image: lo.ToPtr("quay.io/containers/volume:latest"),
			},
			wantErrCount: 0,
		},
		{
			name: "volume with .image reference",
			path: "data.volume",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeVolume,
				Image: lo.ToPtr("my-volume.image"),
			},
			wantErrCount: 0,
		},
		{
			name: "volume with invalid OCI reference",
			path: "data.volume",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeVolume,
				Image: lo.ToPtr("alpine:3.18"),
			},
			wantErrCount:  1,
			wantErrSubstr: "volume.image",
		},
		{
			name: "image with valid OCI reference",
			path: "myimage.image",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeImage,
				Image: lo.ToPtr("quay.io/fedora/fedora:latest"),
			},
			wantErrCount: 0,
		},
		{
			name: "image with no image",
			path: "myimage.image",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeImage,
				Image: nil,
			},
			wantErrCount:  1,
			wantErrSubstr: ".image quadlet must have an Image key",
		},
		{
			name: "image with invalid OCI reference",
			path: "myimage.image",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeImage,
				Image: lo.ToPtr("busybox"),
			},
			wantErrCount:  1,
			wantErrSubstr: "image.image",
		},
		{
			name: "image with .image reference",
			path: "myimage.image",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeImage,
				Image: lo.ToPtr("another.image"),
			},
			wantErrCount:  1,
			wantErrSubstr: "image.image",
		},
		{
			name: "network with no image",
			path: "net.network",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeNetwork,
				Image: nil,
			},
			wantErrCount: 0,
		},
		{
			name: "pod with no image",
			path: "mypod.pod",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypePod,
				Image: nil,
			},
			wantErrCount: 0,
		},
		{
			name: "container with valid OCI mount image",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"quay.io/containers/data:v1"},
			},
			wantErrCount: 0,
		},
		{
			name: "container with valid .image mount reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"my-data.image"},
			},
			wantErrCount: 0,
		},
		{
			name: "container with .build mount reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"my-app.build"},
			},
			wantErrCount:  1,
			wantErrSubstr: ".build quadlet types are unsupported",
		},
		{
			name: "container with invalid OCI mount reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"alpine:latest"},
			},
			wantErrCount:  1,
			wantErrSubstr: "container.mount.image",
		},
		{
			name: "container with multiple valid mount images",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"quay.io/containers/data:v1", "data.image", "quay.io/example/cache:latest"},
			},
			wantErrCount: 0,
		},
		{
			name: "container with multiple mount images (some invalid)",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"quay.io/containers/data:v1", "alpine:latest"},
			},
			wantErrCount:  1,
			wantErrSubstr: "container.mount.image",
		},
		{
			name: "container with no Image but valid mount images",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       nil,
				MountImages: []string{"quay.io/containers/data:v1"},
			},
			wantErrCount:  1,
			wantErrSubstr: "must have an Image key",
		},
		{
			name: "container with valid Image but .build mount reference",
			path: "app.container",
			spec: &common.QuadletReferences{
				Type:        common.QuadletTypeContainer,
				Image:       lo.ToPtr("quay.io/podman/hello:latest"),
				MountImages: []string{"build.build"},
			},
			wantErrCount:  1,
			wantErrSubstr: ".build quadlet types are unsupported",
		},
		{
			name: "container type with volume extension",
			path: "app.volume",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeContainer,
				Image: lo.ToPtr("quay.io/podman/hello:latest"),
			},
			wantErrCount:  1,
			wantErrSubstr: "does not match file extension",
		},
		{
			name: "volume type with container extension",
			path: "data.container",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeVolume,
				Image: lo.ToPtr("quay.io/containers/volume:latest"),
			},
			wantErrCount:  1,
			wantErrSubstr: "does not match file extension",
		},
		{
			name: "image type with network extension",
			path: "myimage.network",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeImage,
				Image: lo.ToPtr("quay.io/fedora/fedora:latest"),
			},
			wantErrCount:  1,
			wantErrSubstr: "does not match file extension",
		},
		{
			name: "network type with pod extension",
			path: "net.pod",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypeNetwork,
				Image: nil,
			},
			wantErrCount:  1,
			wantErrSubstr: "does not match file extension",
		},
		{
			name: "pod type with image extension",
			path: "mypod.image",
			spec: &common.QuadletReferences{
				Type:  common.QuadletTypePod,
				Image: nil,
			},
			wantErrCount:  1,
			wantErrSubstr: "does not match file extension",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateQuadletSpec(tt.spec, tt.path, false)
			require.Len(errs, tt.wantErrCount, "expected %d errors, got %d: %v", tt.wantErrCount, len(errs), errs)
			if tt.wantErrSubstr != "" && len(errs) > 0 {
				require.Contains(errs[0].Error(), tt.wantErrSubstr)
			}
		})
	}
}

func TestValidateQuadletPaths(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		paths         []string
		wantErr       bool
		wantErrSubstr string
	}{
		// Valid cases
		{
			name:    "single container file",
			paths:   []string{"app.container"},
			wantErr: false,
		},
		{
			name:          "single volume file",
			paths:         []string{"data.volume"},
			wantErr:       true,
			wantErrSubstr: "at least one quadlet workload must be supplied",
		},
		{
			name:    "multiple valid types",
			paths:   []string{"app.container", "data.volume"},
			wantErr: false,
		},
		{
			name:    "all supported types",
			paths:   []string{"app.container", "data.volume", "net.network", "img.image", "mypod.pod"},
			wantErr: false,
		},
		{
			name:    "mix of supported quadlet and non-quadlet files",
			paths:   []string{"app.container", "config.yaml", "README.txt"},
			wantErr: false,
		},
		{
			name:    "unknown extensions mixed with valid quadlet",
			paths:   []string{"app.container", "script.sh", "data.conf"},
			wantErr: false,
		},
		{
			name:    "nested non-quadlet files with root quadlet",
			paths:   []string{"app.container", "config/settings.yaml", "scripts/deploy.sh"},
			wantErr: false,
		},

		// Invalid cases
		{
			name:          "empty paths slice",
			paths:         []string{},
			wantErr:       true,
			wantErrSubstr: "no paths provided",
		},
		{
			name:          "contains build file - unsupported",
			paths:         []string{"app.build"},
			wantErr:       true,
			wantErrSubstr: "unsupported quadlet type \".build\"",
		},
		{
			name:          "contains artifact file - unsupported",
			paths:         []string{"app.artifact"},
			wantErr:       true,
			wantErrSubstr: "unsupported quadlet type \".artifact\"",
		},
		{
			name:          "contains kube file - unsupported",
			paths:         []string{"app.kube"},
			wantErr:       true,
			wantErrSubstr: "unsupported quadlet type \".kube\"",
		},
		{
			name:          "mix of valid and unsupported",
			paths:         []string{"app.container", "build.build"},
			wantErr:       true,
			wantErrSubstr: "unsupported quadlet type \".build\"",
		},
		{
			name:          "only non-quadlet files - no supported types",
			paths:         []string{"config.txt", "data.yaml"},
			wantErr:       true,
			wantErrSubstr: "no supported quadlet",
		},
		{
			name:          "only build file - both unsupported and no valid types",
			paths:         []string{"app.build"},
			wantErr:       true,
			wantErrSubstr: "unsupported quadlet type \".build\"",
		},
		{
			name:          "nested quadlet file - must be at root",
			paths:         []string{"config/app.container"},
			wantErr:       true,
			wantErrSubstr: "quadlet file must be at root level",
		},
		{
			name:          "mix of root and nested quadlet files",
			paths:         []string{"app.container", "subdir/data.volume"},
			wantErr:       true,
			wantErrSubstr: "quadlet file must be at root level",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateQuadletPaths(tt.paths)
			if tt.wantErr {
				require.Error(err)
				if tt.wantErrSubstr != "" {
					require.Contains(err.Error(), tt.wantErrSubstr)
				}
				return
			}
			require.NoError(err)
		})
	}
}

func TestValidateQuadletNames_SkipsEmptyNames(t *testing.T) {
	require := require.New(t)

	specs := map[string]*common.QuadletReferences{
		"empty.container": {
			Type: common.QuadletTypeContainer,
			Name: lo.ToPtr(""),
		},
		"spaces.network": {
			Type: common.QuadletTypeNetwork,
			Name: lo.ToPtr("   "),
		},
		"valid.volume": {
			Type: common.QuadletTypeVolume,
			Name: lo.ToPtr("data"),
		},
	}

	errs := ValidateQuadletNames(specs)
	require.Empty(errs)
}

func TestValidateQuadletCrossReferences(t *testing.T) {
	require := require.New(t)

	tests := []struct {
		name          string
		specs         map[string]*common.QuadletReferences
		wantErrCount  int
		wantErrSubstr string
	}{
		{
			name: "all references exist",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:         common.QuadletTypeContainer,
					Image:        lo.ToPtr("myimage.image"),
					Volumes:      []string{"data.volume"},
					Networks:     []string{"mynet.network"},
					Pods:         []string{"mypod.pod"},
					MountImages:  []string{"extra.image"},
					MountVolumes: []string{"cache.volume"},
				},
				"myimage.image": {
					Type:  common.QuadletTypeImage,
					Image: lo.ToPtr("quay.io/podman/hello:latest"),
				},
				"data.volume": {
					Type: common.QuadletTypeVolume,
				},
				"mynet.network": {
					Type: common.QuadletTypeNetwork,
				},
				"mypod.pod": {
					Type: common.QuadletTypePod,
				},
				"extra.image": {
					Type:  common.QuadletTypeImage,
					Image: lo.ToPtr("quay.io/containers/data:v1"),
				},
				"cache.volume": {
					Type: common.QuadletTypeVolume,
				},
			},
			wantErrCount: 0,
		},
		{
			name: "missing image reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:  common.QuadletTypeContainer,
					Image: lo.ToPtr("missing.image"),
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.image" which is not defined`,
		},
		{
			name: "missing volume reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:    common.QuadletTypeContainer,
					Volumes: []string{"missing.volume"},
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.volume" which is not defined`,
		},
		{
			name: "missing network reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:     common.QuadletTypeContainer,
					Networks: []string{"missing.network"},
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.network" which is not defined`,
		},
		{
			name: "missing pod reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type: common.QuadletTypeContainer,
					Pods: []string{"missing.pod"},
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.pod" which is not defined`,
		},
		{
			name: "missing mount image reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:        common.QuadletTypeContainer,
					MountImages: []string{"missing.image"},
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.image" which is not defined`,
		},
		{
			name: "missing mount volume reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:         common.QuadletTypeContainer,
					MountVolumes: []string{"missing.volume"},
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.volume" which is not defined`,
		},
		{
			name: "multiple missing references",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:     common.QuadletTypeContainer,
					Image:    lo.ToPtr("missing.image"),
					Volumes:  []string{"missing.volume"},
					Networks: []string{"missing.network"},
				},
			},
			wantErrCount: 3,
		},
		{
			name: "OCI image reference - not validated as file reference",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:  common.QuadletTypeContainer,
					Image: lo.ToPtr("quay.io/podman/hello:latest"),
				},
			},
			wantErrCount: 0,
		},
		{
			name: "pod with volume and network references",
			specs: map[string]*common.QuadletReferences{
				"mypod.pod": {
					Type:     common.QuadletTypePod,
					Volumes:  []string{"shared.volume"},
					Networks: []string{"backend.network"},
				},
				"shared.volume": {
					Type: common.QuadletTypeVolume,
				},
				"backend.network": {
					Type: common.QuadletTypeNetwork,
				},
			},
			wantErrCount: 0,
		},
		{
			name: "pod with missing volume reference",
			specs: map[string]*common.QuadletReferences{
				"mypod.pod": {
					Type:    common.QuadletTypePod,
					Volumes: []string{"missing.volume"},
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.volume" which is not defined`,
		},
		{
			name: "volume with image reference",
			specs: map[string]*common.QuadletReferences{
				"data.volume": {
					Type:  common.QuadletTypeVolume,
					Image: lo.ToPtr("myimage.image"),
				},
				"myimage.image": {
					Type:  common.QuadletTypeImage,
					Image: lo.ToPtr("quay.io/fedora/fedora:latest"),
				},
			},
			wantErrCount: 0,
		},
		{
			name: "volume with missing image reference",
			specs: map[string]*common.QuadletReferences{
				"data.volume": {
					Type:  common.QuadletTypeVolume,
					Image: lo.ToPtr("missing.image"),
				},
			},
			wantErrCount:  1,
			wantErrSubstr: `references "missing.image" which is not defined`,
		},
		{
			name: "container with OCI image - non-quadlet files not in specs",
			specs: map[string]*common.QuadletReferences{
				"app.container": {
					Type:  common.QuadletTypeContainer,
					Image: lo.ToPtr("quay.io/podman/hello:latest"),
				},
			},
			wantErrCount: 0,
		},
		{
			name:         "empty specs map",
			specs:        map[string]*common.QuadletReferences{},
			wantErrCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := ValidateQuadletCrossReferences(tt.specs)
			require.Len(errs, tt.wantErrCount, "expected %d errors, got %d: %v", tt.wantErrCount, len(errs), errs)
			if tt.wantErrSubstr != "" && len(errs) > 0 {
				found := false
				for _, err := range errs {
					if strings.Contains(err.Error(), tt.wantErrSubstr) {
						found = true
						break
					}
				}
				require.True(found, "expected error containing %q, got: %v", tt.wantErrSubstr, errs)
			}
		})
	}
}
