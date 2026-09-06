package delta_worker

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExpandAppCandidates(t *testing.T) {
	ctx := context.Background()
	orgId := uuid.New()

	inspectOK := func(_ context.Context, _ uuid.UUID, image string) (string, error) {
		return "sha256:new_" + image, nil
	}
	inspectFail := func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
		return "", fmt.Errorf("registry down")
	}

	t.Run("When rendered spec has no applications it should return OS candidates unchanged", func(t *testing.T) {
		osCand := DeltaCandidate{ImageRepository: "quay.io/acme/os", CurrentDigest: "sha256:aaa", NewDigest: "sha256:bbb"}
		result := expandAppCandidates(ctx, orgId, nil, tasks.RenderedSpec{}, []DeltaCandidate{osCand}, inspectOK)
		require.Len(t, result, 1)
		assert.Equal(t, osCand, result[0])
	})

	t.Run("When a container app has a matching imageDigest it should produce a candidate", func(t *testing.T) {
		app := containerApp("quay.io/acme/nginx:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithImageDigests("test-app", "quay.io/acme/nginx:v2", "sha256:old_nginx")

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		require.Len(t, result, 1)
		assert.Equal(t, "quay.io/acme/nginx", result[0].ImageRepository)
		assert.Equal(t, "sha256:old_nginx", result[0].CurrentDigest)
		assert.Equal(t, "sha256:new_quay.io/acme/nginx:v2", result[0].NewDigest)
	})

	t.Run("When imageDigests is missing for an image it should skip that pair", func(t *testing.T) {
		app := containerApp("quay.io/acme/nginx:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithImageDigests("other-app", "quay.io/other/image:v1", "sha256:xxx")

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		assert.Empty(t, result)
	})

	t.Run("When device has no status it should return candidates unchanged", func(t *testing.T) {
		app := containerApp("quay.io/acme/nginx:v2")
		rendered := renderedWithApps(t, app)

		result := expandAppCandidates(ctx, orgId, nil, rendered, nil, inspectOK)
		assert.Empty(t, result)
	})

	t.Run("When inspect fails it should skip that pair", func(t *testing.T) {
		app := containerApp("quay.io/acme/nginx:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithImageDigests("test-app", "quay.io/acme/nginx:v2", "sha256:old")

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectFail)
		assert.Empty(t, result)
	})

	t.Run("When current and new digest are the same it should skip", func(t *testing.T) {
		app := containerApp("quay.io/acme/nginx@sha256:same")
		rendered := renderedWithApps(t, app)
		device := deviceWithImageDigests("test-app", "quay.io/acme/nginx@sha256:same", "sha256:same")
		sameInspect := func(_ context.Context, _ uuid.UUID, _ string) (string, error) {
			return "sha256:same", nil
		}

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, sameInspect)
		assert.Empty(t, result)
	})

	t.Run("When a compose app is inline it should extract service images", func(t *testing.T) {
		app := inlineComposeApp("quay.io/acme/svc-a:v2", "quay.io/acme/svc-b:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithMultipleDigests(map[string]string{
			"quay.io/acme/svc-a:v2": "sha256:old_a",
			"quay.io/acme/svc-b:v2": "sha256:old_b",
		})

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		require.Len(t, result, 2)
	})

	t.Run("When a compose app is image-based with inline content it should extract parent and service images", func(t *testing.T) {
		app := imageBasedComposeAppWithInline("quay.io/acme/compose-pkg:v2", "quay.io/acme/svc-a:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithMultipleDigests(map[string]string{
			"quay.io/acme/compose-pkg:v2": "sha256:old_pkg",
			"quay.io/acme/svc-a:v2":       "sha256:old_svc_a",
		})

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		require.Len(t, result, 2, "should produce candidates for both parent artifact and nested service")
	})

	t.Run("When a quadlet app is inline it should extract Image= refs", func(t *testing.T) {
		app := inlineQuadletApp("quay.io/acme/worker:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithImageDigests("quad-app", "quay.io/acme/worker:v2", "sha256:old_worker")

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		require.Len(t, result, 1)
		assert.Equal(t, "sha256:old_worker", result[0].CurrentDigest)
	})

	t.Run("When a helm app has a chart image it should produce a candidate", func(t *testing.T) {
		app := helmApp("quay.io/acme/chart:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithImageDigests("helm-app", "quay.io/acme/chart:v2", "sha256:old_chart")

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		require.Len(t, result, 1)
	})

	t.Run("When applications JSON is invalid it should return candidates unchanged", func(t *testing.T) {
		rendered := tasks.RenderedSpec{Applications: []byte("invalid json")}
		osCand := DeltaCandidate{ImageRepository: "quay.io/acme/os", CurrentDigest: "sha256:aaa", NewDigest: "sha256:bbb"}
		result := expandAppCandidates(ctx, orgId, nil, rendered, []DeltaCandidate{osCand}, inspectOK)
		require.Len(t, result, 1)
		assert.Equal(t, osCand, result[0])
	})

	t.Run("When volume has an OCI image it should produce a candidate", func(t *testing.T) {
		app := containerAppWithVolume("quay.io/acme/nginx:v2", "quay.io/acme/data:v2")
		rendered := renderedWithApps(t, app)
		device := deviceWithMultipleDigests(map[string]string{
			"quay.io/acme/nginx:v2": "sha256:old_nginx",
			"quay.io/acme/data:v2":  "sha256:old_data",
		})

		result := expandAppCandidates(ctx, orgId, device, rendered, nil, inspectOK)
		require.Len(t, result, 2)
	})
}

func TestBuildDigestIndex(t *testing.T) {
	t.Run("When device is nil it should return nil", func(t *testing.T) {
		idx := buildDigestIndex(nil)
		assert.Nil(t, idx)
	})

	t.Run("When device has no status it should return nil", func(t *testing.T) {
		idx := buildDigestIndex(&domain.Device{})
		assert.Nil(t, idx)
	})

	t.Run("When device has imageDigests it should index them", func(t *testing.T) {
		device := deviceWithMultipleDigests(map[string]string{
			"quay.io/a:v1": "sha256:aaa",
			"quay.io/b:v1": "sha256:bbb",
		})
		idx := buildDigestIndex(device)
		require.Len(t, idx, 2)
		assert.Equal(t, "sha256:aaa", idx["quay.io/a:v1"])
		assert.Equal(t, "sha256:bbb", idx["quay.io/b:v1"])
	})
}

func TestExtractNewImageRefs(t *testing.T) {
	t.Run("When container app has an image it should return it", func(t *testing.T) {
		app := containerApp("quay.io/acme/nginx:v2")
		refs := extractNewImageRefs(&app)
		require.Len(t, refs, 1)
		assert.Equal(t, "quay.io/acme/nginx:v2", refs[0])
	})

	t.Run("When compose app is inline it should extract service images", func(t *testing.T) {
		app := inlineComposeApp("quay.io/a:v1", "quay.io/b:v1")
		refs := extractNewImageRefs(&app)
		require.Len(t, refs, 2)
	})

	t.Run("When quadlet app is inline it should extract Image= refs", func(t *testing.T) {
		app := inlineQuadletApp("quay.io/acme/worker:v2")
		refs := extractNewImageRefs(&app)
		require.Len(t, refs, 1)
		assert.Equal(t, "quay.io/acme/worker:v2", refs[0])
	})

	t.Run("When helm app has a chart image it should return it", func(t *testing.T) {
		app := helmApp("quay.io/acme/chart:v2")
		refs := extractNewImageRefs(&app)
		require.Len(t, refs, 1)
		assert.Equal(t, "quay.io/acme/chart:v2", refs[0])
	})
}

// --- test helpers ---

func containerApp(image string) domain.ApplicationProviderSpec {
	container := v1beta1.ContainerApplication{
		AppType: v1beta1.AppTypeContainer,
		Name:    lo.ToPtr("test-app"),
	}
	var app domain.ApplicationProviderSpec
	imageSpec := v1beta1.ImageSpec{Image: image}
	_ = container.FromImageApplicationProviderSpec(imageSpec)
	_ = app.FromContainerApplication(container)
	return app
}

func containerAppWithVolume(image, volumeImage string) domain.ApplicationProviderSpec {
	container := v1beta1.ContainerApplication{
		AppType: v1beta1.AppTypeContainer,
		Name:    lo.ToPtr("test-app"),
		Volumes: &[]v1beta1.ApplicationVolume{
			imageVolume("data", volumeImage),
		},
	}
	imageSpec := v1beta1.ImageSpec{Image: image}
	_ = container.FromImageApplicationProviderSpec(imageSpec)
	var app domain.ApplicationProviderSpec
	_ = app.FromContainerApplication(container)
	return app
}

func imageVolume(name, reference string) v1beta1.ApplicationVolume {
	vol := v1beta1.ApplicationVolume{Name: name}
	imgVol := v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: reference},
	}
	_ = vol.FromImageVolumeProviderSpec(imgVol)
	return vol
}

// imageBasedComposeAppWithInline creates a compose app that has both an image
// ref (parent artifact) AND inline content (the rendered form). This simulates
// what the render pipeline produces: it expands the artifact into inline files
// so PrepareDeltas can parse them without pulling the artifact.
func imageBasedComposeAppWithInline(parentImage string, serviceImages ...string) domain.ApplicationProviderSpec {
	var services []string
	for i, img := range serviceImages {
		services = append(services, fmt.Sprintf("  svc%d:\n    image: %s", i, img))
	}
	content := "services:\n" + joinLines(services)
	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr("compose-app"),
	}
	// Set the image-based spec.
	imageSpec := v1beta1.ImageSpec{Image: parentImage}
	_ = compose.FromImageApplicationProviderSpec(imageSpec)
	// Also set inline content (as render does).
	inline := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{Path: "docker-compose.yaml", Content: lo.ToPtr(content)},
		},
	}
	_ = compose.MergeInlineApplicationProviderSpec(inline)
	var app domain.ApplicationProviderSpec
	_ = app.FromComposeApplication(compose)
	return app
}

func inlineComposeApp(serviceImages ...string) domain.ApplicationProviderSpec {
	var services []string
	for i, img := range serviceImages {
		services = append(services, fmt.Sprintf("  svc%d:\n    image: %s", i, img))
	}
	content := "services:\n" + joinLines(services)
	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr("compose-app"),
	}
	inline := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{Path: "docker-compose.yaml", Content: lo.ToPtr(content)},
		},
	}
	_ = compose.FromInlineApplicationProviderSpec(inline)
	var app domain.ApplicationProviderSpec
	_ = app.FromComposeApplication(compose)
	return app
}

func inlineQuadletApp(image string) domain.ApplicationProviderSpec {
	content := fmt.Sprintf("[Container]\nImage=%s\n", image)
	quadlet := v1beta1.QuadletApplication{
		AppType: v1beta1.AppTypeQuadlet,
		Name:    lo.ToPtr("quad-app"),
	}
	inline := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{Path: "app.container", Content: lo.ToPtr(content)},
		},
	}
	_ = quadlet.FromInlineApplicationProviderSpec(inline)
	var app domain.ApplicationProviderSpec
	_ = app.FromQuadletApplication(quadlet)
	return app
}

func helmApp(chartImage string) domain.ApplicationProviderSpec {
	helm := v1beta1.HelmApplication{
		AppType: v1beta1.AppTypeHelm,
		Name:    lo.ToPtr("helm-app"),
	}
	imageSpec := v1beta1.ImageSpec{Image: chartImage}
	_ = helm.FromImageApplicationProviderSpec(imageSpec)
	var app domain.ApplicationProviderSpec
	_ = app.FromHelmApplication(helm)
	return app
}

func renderedWithApps(t *testing.T, apps ...domain.ApplicationProviderSpec) tasks.RenderedSpec {
	t.Helper()
	b, err := json.Marshal(apps)
	require.NoError(t, err)
	return tasks.RenderedSpec{Applications: b}
}

func deviceWithImageDigests(appName, image, digest string) *domain.Device {
	digests := []v1beta1.ApplicationImageDigest{{Image: image, Digest: digest}}
	return &domain.Device{
		Status: &domain.DeviceStatus{
			Applications: []v1beta1.DeviceApplicationStatus{
				{
					Name:         appName,
					AppType:      v1beta1.AppTypeContainer,
					ImageDigests: &digests,
				},
			},
		},
	}
}

func deviceWithMultipleDigests(pairs map[string]string) *domain.Device {
	var digests []v1beta1.ApplicationImageDigest
	for image, digest := range pairs {
		digests = append(digests, v1beta1.ApplicationImageDigest{Image: image, Digest: digest})
	}
	return &domain.Device{
		Status: &domain.DeviceStatus{
			Applications: []v1beta1.DeviceApplicationStatus{
				{
					Name:         "multi-app",
					AppType:      v1beta1.AppTypeCompose,
					ImageDigests: &digests,
				},
			},
		},
	}
}

func joinLines(lines []string) string {
	result := ""
	for _, line := range lines {
		result += line + "\n"
	}
	return result
}
