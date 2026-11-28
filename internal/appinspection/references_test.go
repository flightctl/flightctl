package appinspection_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/appinspection"
	"github.com/stretchr/testify/require"
)

func TestInspectSpecWithUnreadableInlineContent(t *testing.T) {
	var app v1beta1.ApplicationProviderSpec
	require.NoError(t, json.Unmarshal([]byte(`{"appType":"compose","image":"quay.io/example/package:v1","inline":[{"path":"docker-compose.yaml","content":{"unexpected":"private-value"}}]}`), &app))
	result := appinspection.InspectSpec(&app)
	require.Equal(t, []appinspection.Reference{containerRef("quay.io/example/package:v1", appinspection.RolePackage)}, result.Declared)
	require.NotNil(t, result.Inline)
	require.False(t, result.Inline.Complete())
	require.Equal(t, []appinspection.Diagnostic{appinspection.DiagnosticParseFailed}, result.Inline.Diagnostics)
}

func TestInspectSpec(t *testing.T) {
	const image = "quay.io/example/app:v1"
	const volumes = `,"volumes":[{"name":"data","image":{"reference":"quay.io/example/app:v1"}},{"name":"mount","image":{"reference":"quay.io/example/mount:v1"},"mount":{"path":"/data"}}]`
	tests := []struct {
		name       string
		input      string
		declared   []appinspection.Reference
		inlineRefs []appinspection.Reference
	}{
		{"When a container declares a workload and image volumes it should preserve each role",
			`{"appType":"container","image":"` + image + `"` + volumes + `}`,
			[]appinspection.Reference{containerRef(image, appinspection.RoleWorkload), containerRef(image, appinspection.RoleVolume), containerRef("quay.io/example/mount:v1", appinspection.RoleVolume)}, nil},
		{"When a Compose package has inline files it should discover both the package and service",
			`{"appType":"compose","image":"` + image + `","inline":[{"path":"docker-compose.yaml","content":"services:\n  app:\n    image: quay.io/example/workload:v1\n"}]}`,
			[]appinspection.Reference{containerRef(image, appinspection.RolePackage)}, []appinspection.Reference{containerRef("quay.io/example/workload:v1", appinspection.RoleWorkload)}},
		{"When a Quadlet package has no content it should only report its declared reference",
			`{"appType":"quadlet","image":"` + image + `"}`,
			[]appinspection.Reference{containerRef(image, appinspection.RolePackage)}, nil},
		{"When a Quadlet is inline it should discover its workload image",
			`{"appType":"quadlet","inline":[{"path":"app.container","content":"[Container]\nImage=quay.io/example/app:v1\n"}]}`,
			nil, []appinspection.Reference{containerRef(image, appinspection.RoleWorkload)}},
		{"When a Helm application declares a chart it should classify its package consumer",
			`{"appType":"helm","image":"` + image + `"}`,
			[]appinspection.Reference{{Image: image, Role: appinspection.RolePackage, ConsumerKind: appinspection.ConsumerHelmChart}}, nil},
		{"When an unconverted VM declares an image it should retain the artifact",
			`{"appType":"vm","image":"` + image + `"}`,
			[]appinspection.Reference{containerRef(image, appinspection.RolePackage)}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var app v1beta1.ApplicationProviderSpec
			require.NoError(t, json.Unmarshal([]byte(tt.input), &app))
			result := appinspection.InspectSpec(&app)
			require.ElementsMatch(t, tt.declared, result.Declared)
			if tt.inlineRefs == nil {
				require.Nil(t, result.Inline)
			} else {
				require.NotNil(t, result.Inline)
				require.True(t, result.Inline.Complete())
				require.ElementsMatch(t, tt.inlineRefs, result.Inline.References)
			}
			encoded, err := json.Marshal(app)
			require.NoError(t, err)
			require.JSONEq(t, tt.input, string(encoded))
		})
	}
}
