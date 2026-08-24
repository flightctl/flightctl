package tasks

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDesiredSpecFromTemplate(t *testing.T) {
	device := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("device-1"),
			Labels: &map[string]string{
				"site":   "edge",
				"branch": "main",
				"env":    "prod",
				"app":    "web",
			},
		},
		Spec: &domain.DeviceSpec{
			Os: &domain.DeviceOsSpec{Image: "old-image:latest"},
		},
	}

	gitItem := makeGitConfigItem(t, "git-cfg", "my-repo", "{{ .metadata.labels.branch }}")
	httpSuffix := "/{{ .metadata.labels.env }}/os.yaml"
	httpItem := makeHttpConfigItem(t, "http-cfg", "http-repo", &httpSuffix)
	inlineItem := makeInlineConfigItem(t, "inline-cfg", "/etc/{{ .metadata.labels.site }}.conf", "site={{ .metadata.labels.site }}")
	appItem := makeContainerAppItem(t, "app-1", "quay.io/apps/{{ .metadata.labels.app }}:latest")

	tv := &domain.TemplateVersion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
		Status: &domain.TemplateVersionStatus{
			Os: &domain.DeviceOsSpec{
				Image: "quay.io/os/{{ .metadata.labels.site }}:latest",
			},
			Config:       &[]domain.ConfigProviderSpec{gitItem, httpItem, inlineItem},
			Applications: &[]domain.ApplicationProviderSpec{appItem},
		},
	}

	originalImage := device.Spec.Os.Image

	spec, err := DesiredSpecFromTemplate(device, tv)
	require.NoError(t, err)
	require.NotNil(t, spec)
	require.NotNil(t, spec.Os)
	assert.Equal(t, "quay.io/os/edge:latest", spec.Os.Image)
	assert.Equal(t, originalImage, device.Spec.Os.Image)
	assert.NotSame(t, device.Spec, spec)

	require.NotNil(t, spec.Config)
	require.Len(t, *spec.Config, 3)

	gitSpec, err := (*spec.Config)[0].AsGitConfigProviderSpec()
	require.NoError(t, err)
	assert.Equal(t, "main", gitSpec.GitRef.TargetRevision)

	httpSpec, err := (*spec.Config)[1].AsHttpConfigProviderSpec()
	require.NoError(t, err)
	require.NotNil(t, httpSpec.HttpRef.Suffix)
	assert.Equal(t, "/prod/os.yaml", *httpSpec.HttpRef.Suffix)

	inlineSpec, err := (*spec.Config)[2].AsInlineConfigProviderSpec()
	require.NoError(t, err)
	require.Len(t, inlineSpec.Inline, 1)
	assert.Equal(t, "/etc/edge.conf", inlineSpec.Inline[0].Path)
	assert.Equal(t, "site=edge", inlineSpec.Inline[0].Content)

	require.NotNil(t, spec.Applications)
	require.Len(t, *spec.Applications, 1)
	containerApp, err := (*spec.Applications)[0].AsContainerApplication()
	require.NoError(t, err)
	imageSpec, err := containerApp.AsImageApplicationProviderSpec()
	require.NoError(t, err)
	assert.Equal(t, "quay.io/apps/web:latest", imageSpec.Image)
}

func TestDesiredSpecFromTemplate_WhenOsIsACatalogItemRefItShouldCopyTheRefWithoutWritingTheDevice(t *testing.T) {
	ref := domain.CatalogItemRefSpec{Catalog: "rhel", Item: "edge", Version: "9.4"}
	device := &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("device-1")},
		Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "old-image:latest"}},
	}
	tv := &domain.TemplateVersion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
		Status: &domain.TemplateVersionStatus{
			Os: &domain.DeviceOsSpec{CatalogItemRef: &ref},
		},
	}

	spec, err := DesiredSpecFromTemplate(device, tv)
	require.NoError(t, err)
	require.NotNil(t, spec.Os)
	require.NotNil(t, spec.Os.CatalogItemRef)
	assert.Equal(t, ref, *spec.Os.CatalogItemRef)
	assert.Empty(t, spec.Os.Image)
	assert.Equal(t, "old-image:latest", device.Spec.Os.Image)
}

func TestDesiredSpecFromTemplate_WhenSubstitutionFailsItShouldReturnAnError(t *testing.T) {
	device := &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("device-1")},
		Spec:     &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "old-image:latest"}},
	}
	tv := &domain.TemplateVersion{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr("tv-1")},
		Status: &domain.TemplateVersionStatus{
			Os: &domain.DeviceOsSpec{Image: "{{ .metadata.labels.missing }}"},
		},
	}

	_, err := DesiredSpecFromTemplate(device, tv)
	require.Error(t, err)
	assert.Equal(t, "old-image:latest", device.Spec.Os.Image)
}

func makeInlineConfigItem(t *testing.T, name, path, content string) domain.ConfigProviderSpec {
	t.Helper()
	item := domain.ConfigProviderSpec{}
	require.NoError(t, item.FromInlineConfigProviderSpec(domain.InlineConfigProviderSpec{
		Name: name,
		Inline: []domain.FileSpec{{
			Path:    path,
			Content: content,
		}},
	}))
	return item
}

func makeContainerAppItem(t *testing.T, name, image string) domain.ApplicationProviderSpec {
	t.Helper()
	containerApp := domain.ContainerApplication{
		Name:    lo.ToPtr(name),
		AppType: domain.AppTypeContainer,
	}
	require.NoError(t, containerApp.FromImageApplicationProviderSpec(domain.ImageApplicationProviderSpec{Image: image}))
	var app domain.ApplicationProviderSpec
	require.NoError(t, app.FromContainerApplication(containerApp))
	return app
}
