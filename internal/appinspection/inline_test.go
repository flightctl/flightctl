package appinspection_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func content(path, text string) v1beta1.ApplicationContent {
	return v1beta1.ApplicationContent{Path: path, Content: lo.ToPtr(text)}
}

func containerRef(image string, role appinspection.ArtifactRole) appinspection.Reference {
	return appinspection.Reference{Image: image, Role: role, ConsumerKind: appinspection.ConsumerContainer}
}

func TestInspectCompose(t *testing.T) {
	base := content("docker-compose.yaml", "services:\n  web:\n    image: quay.io/example/web:v1\n  other:\n    image: quay.io/example/other:v1\n")
	tests := []struct {
		name     string
		contents []v1beta1.ApplicationContent
		images   []string
		complete bool
	}{
		{"When an override is supplied it should report the effective service images", []v1beta1.ApplicationContent{base,
			content("docker-compose.override.yaml", "services:\n  web:\n    image: quay.io/example/web:v2\n")},
			[]string{"quay.io/example/web:v2", "quay.io/example/other:v1"}, true},
		{"When a later override is malformed it should retain the references found so far", []v1beta1.ApplicationContent{base,
			content("docker-compose.override.yaml", "services: [private-value")},
			[]string{"quay.io/example/web:v1", "quay.io/example/other:v1"}, false},
		{"When a valid service has no image it should report a complete empty result", []v1beta1.ApplicationContent{
			content("docker-compose.yaml", "services:\n  build-only:\n    build: .\n")}, nil, true},
		{"When there is no base file it should report an incomplete empty result", nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appinspection.InspectCompose(tt.contents)
			require.Equal(t, tt.complete, result.Complete())
			var want []appinspection.Reference
			for _, image := range tt.images {
				want = append(want, containerRef(image, appinspection.RoleWorkload))
			}
			require.ElementsMatch(t, want, result.References)
			if !tt.complete {
				require.Equal(t, []appinspection.Diagnostic{appinspection.DiagnosticParseFailed}, result.Diagnostics)
			}
		})
	}
}

func TestInspectQuadlet(t *testing.T) {
	app := content("app.container", "[Container]\nImage=quay.io/example/old:v1\n")
	workload := containerRef("quay.io/example/old:v1", appinspection.RoleWorkload)
	pod := content("pod.kube", "[Kube]\nYaml=./pod.yaml\n")
	tests := []struct {
		name     string
		contents []v1beta1.ApplicationContent
		refs     []appinspection.Reference
		complete bool
	}{
		{
			name: "When units and drop-ins use image volumes and a Pod it should preserve distinct roles",
			contents: []v1beta1.ApplicationContent{
				app,
				content("app.container.d/10-image.conf", "[Container]\nImage=local.image\nMount=type=image,source=quay.io/example/shared:v1,destination=/data\n"),
				content("local.image", "[Image]\nImage=quay.io/example/shared:v1\n"),
				content("data.volume", "[Volume]\nDriver=image\nImage=quay.io/example/data:v1\n"),
				pod,
				content("pod.yaml", "kind: Pod\nspec:\n  containers:\n  - image: quay.io/example/shared:v1\n  initContainers:\n  - image: quay.io/example/init:v1\n  volumes:\n  - image:\n      reference: quay.io/example/pod-data:v1\n"),
			},
			refs: []appinspection.Reference{
				containerRef("quay.io/example/shared:v1", appinspection.RoleWorkload),
				containerRef("quay.io/example/shared:v1", appinspection.RoleVolume),
				containerRef("quay.io/example/data:v1", appinspection.RoleVolume),
				containerRef("quay.io/example/init:v1", appinspection.RoleWorkload),
				containerRef("quay.io/example/pod-data:v1", appinspection.RoleVolume),
			},
			complete: true,
		},
		{
			name: "When a volume uses an image unit it should classify the effective image as a volume",
			contents: []v1beta1.ApplicationContent{
				app,
				content("data.volume", "[Volume]\nDriver=image\nImage=data.image\n"),
				content("data.image", "[Image]\nImage=quay.io/example/data:v1\n"),
				content("data.image.d/10-image.conf", "[Image]\nImage=quay.io/example/data:v2\n"),
			},
			refs:     []appinspection.Reference{workload, containerRef("quay.io/example/data:v2", appinspection.RoleVolume)},
			complete: true,
		},
		{
			name: "When a container and volume share an image unit it should report both roles",
			contents: []v1beta1.ApplicationContent{
				content("app.container", "[Container]\nImage=local.image\n"),
				content("data.volume", "[Volume]\nDriver=image\nImage=local.image\n"),
				content("local.image", "[Image]\nImage=quay.io/example/shared:v1\n"),
			},
			refs: []appinspection.Reference{
				containerRef("quay.io/example/shared:v1", appinspection.RoleWorkload),
				containerRef("quay.io/example/shared:v1", appinspection.RoleVolume),
			},
			complete: true,
		},
		{
			name: "When an image mount uses an image unit it should classify the image as a volume",
			contents: []v1beta1.ApplicationContent{
				content("app.container", "[Container]\nImage=quay.io/example/old:v1\nMount=type=image,source=data.image,destination=/data\n"),
				content("data.image", "[Image]\nImage=quay.io/example/data:v1\n"),
				content("standalone.image", "[Image]\nImage=quay.io/example/standalone:v1\n"),
			},
			refs: []appinspection.Reference{
				workload,
				containerRef("quay.io/example/data:v1", appinspection.RoleVolume),
				containerRef("quay.io/example/standalone:v1", appinspection.RoleWorkload),
			},
			complete: true,
		},
		{"When another unit is malformed it should retain valid unit refs", []v1beta1.ApplicationContent{app,
			content("private-name.container", "[Service]\n")}, []appinspection.Reference{workload}, false},
		{"When a matching drop-in is malformed it should keep independent units", []v1beta1.ApplicationContent{app,
			content("other.container", "[Container]\nImage=quay.io/example/other:v1\n"),
			content("other.container.d/10-bad.conf", "[unterminated")}, []appinspection.Reference{workload}, false},
		{"When a Pod file is missing it should retain other units' refs", []v1beta1.ApplicationContent{app, pod}, []appinspection.Reference{workload}, false},
		{"When Pod YAML is malformed it should retain other units' refs", []v1beta1.ApplicationContent{app, pod,
			content("pod.yaml", "spec: [private-value")}, []appinspection.Reference{workload}, false},
		{"When a Pod path escapes the inline set it should not resolve that file", []v1beta1.ApplicationContent{app,
			content("pod.kube", "[Kube]\nYaml=../pod.yaml\n"),
			content("../pod.yaml", "spec:\n  containers:\n  - image: quay.io/example/escaped:v1\n")}, []appinspection.Reference{workload}, false},
		{"When a network unit has no images it should yield a complete empty result", []v1beta1.ApplicationContent{
			content("app.network", "[Network]\n")}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := appinspection.InspectQuadlet(tt.contents)
			require.Equal(t, tt.complete, result.Complete())
			require.ElementsMatch(t, tt.refs, result.References)
			if !tt.complete {
				require.Equal(t, []appinspection.Diagnostic{appinspection.DiagnosticParseFailed}, result.Diagnostics)
				diagnostics, err := json.Marshal(result.Diagnostics)
				require.NoError(t, err)
				require.NotContains(t, string(diagnostics), "private-")
			}
		})
	}
}
