package domain

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestContainerApp(t *testing.T, name, image string) ApplicationProviderSpec {
	t.Helper()
	containerApp := ContainerApplication{
		AppType: AppTypeContainer,
		Name:    lo.ToPtr(name),
		Image:   image,
	}
	var app ApplicationProviderSpec
	require.NoError(t, app.FromContainerApplication(containerApp))
	return app
}

func TestApplyApplicationLifecycleOverride(t *testing.T) {
	t.Run("When desiredState is set it should be applied without disturbing other fields", func(t *testing.T) {
		app := newTestContainerApp(t, "my-app", "quay.io/test/app:v1")

		updated, err := ApplyApplicationLifecycleOverride(app, DeviceApplicationLifecycle{
			DesiredState: lo.ToPtr(ApplicationDesiredStateStopped),
		})
		require.NoError(t, err)

		containerApp, err := updated.AsContainerApplication()
		require.NoError(t, err)
		require.NotNil(t, containerApp.DesiredState)
		assert.Equal(t, ApplicationDesiredStateStopped, *containerApp.DesiredState)
		assert.Nil(t, containerApp.RestartGeneration)
		assert.Equal(t, "quay.io/test/app:v1", containerApp.Image)
		require.NotNil(t, containerApp.Name)
		assert.Equal(t, "my-app", *containerApp.Name)
	})

	t.Run("When restartGeneration is set it should be applied", func(t *testing.T) {
		app := newTestContainerApp(t, "my-app", "quay.io/test/app:v1")

		updated, err := ApplyApplicationLifecycleOverride(app, DeviceApplicationLifecycle{
			RestartGeneration: lo.ToPtr(3),
		})
		require.NoError(t, err)

		containerApp, err := updated.AsContainerApplication()
		require.NoError(t, err)
		require.NotNil(t, containerApp.RestartGeneration)
		assert.Equal(t, 3, *containerApp.RestartGeneration)
		assert.Nil(t, containerApp.DesiredState)
	})

	t.Run("When both fields are set it should apply both", func(t *testing.T) {
		app := newTestContainerApp(t, "my-app", "quay.io/test/app:v1")

		updated, err := ApplyApplicationLifecycleOverride(app, DeviceApplicationLifecycle{
			DesiredState:      lo.ToPtr(ApplicationDesiredStateRunning),
			RestartGeneration: lo.ToPtr(7),
		})
		require.NoError(t, err)

		containerApp, err := updated.AsContainerApplication()
		require.NoError(t, err)
		require.NotNil(t, containerApp.DesiredState)
		assert.Equal(t, ApplicationDesiredStateRunning, *containerApp.DesiredState)
		require.NotNil(t, containerApp.RestartGeneration)
		assert.Equal(t, 7, *containerApp.RestartGeneration)
	})

	t.Run("When override is empty it should leave the app unchanged", func(t *testing.T) {
		app := newTestContainerApp(t, "my-app", "quay.io/test/app:v1")

		updated, err := ApplyApplicationLifecycleOverride(app, DeviceApplicationLifecycle{})
		require.NoError(t, err)

		containerApp, err := updated.AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, containerApp.DesiredState)
		assert.Nil(t, containerApp.RestartGeneration)
	})
}

func TestOverlayApplicationLifecycle(t *testing.T) {
	t.Run("When apps is nil it should be a no-op", func(t *testing.T) {
		err := OverlayApplicationLifecycle(nil, `{"app-1":{"desiredState":"stopped"}}`)
		require.NoError(t, err)
	})

	t.Run("When apps is empty it should be a no-op", func(t *testing.T) {
		apps := []ApplicationProviderSpec{}
		err := OverlayApplicationLifecycle(&apps, `{"app-1":{"desiredState":"stopped"}}`)
		require.NoError(t, err)
		assert.Empty(t, apps)
	})

	t.Run("When raw annotation is invalid JSON it should return an error", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestContainerApp(t, "app-1", "img:v1")}
		err := OverlayApplicationLifecycle(&apps, `not-json`)
		require.Error(t, err)
	})

	t.Run("When overrides map is empty it should leave apps unchanged", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestContainerApp(t, "app-1", "img:v1")}
		err := OverlayApplicationLifecycle(&apps, `{}`)
		require.NoError(t, err)

		containerApp, err := apps[0].AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, containerApp.DesiredState)
	})

	t.Run("When an app name matches an override it should apply it", func(t *testing.T) {
		apps := []ApplicationProviderSpec{
			newTestContainerApp(t, "app-1", "img:v1"),
			newTestContainerApp(t, "app-2", "img:v2"),
		}
		err := OverlayApplicationLifecycle(&apps, `{"app-1":{"desiredState":"stopped","restartGeneration":2}}`)
		require.NoError(t, err)

		app1, err := apps[0].AsContainerApplication()
		require.NoError(t, err)
		require.NotNil(t, app1.DesiredState)
		assert.Equal(t, ApplicationDesiredStateStopped, *app1.DesiredState)
		require.NotNil(t, app1.RestartGeneration)
		assert.Equal(t, 2, *app1.RestartGeneration)

		app2, err := apps[1].AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, app2.DesiredState, "app-2 has no override and should be untouched")
	})

	t.Run("When no app name matches an override it should leave all apps unchanged", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestContainerApp(t, "app-1", "img:v1")}
		err := OverlayApplicationLifecycle(&apps, `{"other-app":{"desiredState":"stopped"}}`)
		require.NoError(t, err)

		app1, err := apps[0].AsContainerApplication()
		require.NoError(t, err)
		assert.Nil(t, app1.DesiredState)
	})
}
