package delta_worker

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExpandAppCandidatesWithPartialInspection(t *testing.T) {
	tests := []struct {
		name   string
		app    string
		images []string
	}{
		{
			name: "When Compose inspection is incomplete it should only pair declared refs",
			app: `{"appType":"compose","image":"quay.io/example/package:v2",
				"volumes":[{"name":"data","image":{"reference":"quay.io/example/data:v2"}}],
				"inline":[
					{"path":"docker-compose.yaml","content":"services:\n  web:\n    image: quay.io/example/web:v2"},
					{"path":"docker-compose.override.yaml","content":"services: [invalid"}]}`,
			images: []string{"quay.io/example/package:v2", "quay.io/example/data:v2"},
		},
		{
			name: "When a Quadlet image has multiple roles it should pair the image once",
			app: `{"appType":"quadlet",
				"inline":[{"path":"app.container","content":"[Container]\nImage=quay.io/example/web:v2\nMount=type=image,source=quay.io/example/web:v2,destination=/data"}]}`,
			images: []string{"quay.io/example/web:v2"},
		},
		{
			name: "When Quadlet units share an image unit it should pair the registry reference once",
			app: `{"appType":"quadlet","inline":[
				{"path":"app.container","content":"[Container]\nImage=local.image"},
				{"path":"data.volume","content":"[Volume]\nDriver=image\nImage=local.image"},
				{"path":"local.image","content":"[Image]\nImage=quay.io/example/web:v2"}]}`,
			images: []string{"quay.io/example/web:v2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var app domain.ApplicationProviderSpec
			require.NoError(t, json.Unmarshal([]byte(tt.app), &app))
			device := deviceWithMultipleDigests(map[string]string{
				"quay.io/example/web:v1":     "sha256:old_web",
				"quay.io/example/package:v1": "sha256:old_package",
				"quay.io/example/data:v1":    "sha256:old_data",
			})
			var inspected []string
			inspect := func(_ context.Context, _ uuid.UUID, image string) (string, error) {
				inspected = append(inspected, image)
				return "sha256:target", nil
			}
			candidates := expandAppCandidates(t.Context(), quietTestLogger(), uuid.New(), device, renderedWithApps(t, app), nil, inspect)
			require.ElementsMatch(t, tt.images, inspected)
			require.Len(t, candidates, len(tt.images))
		})
	}
}
