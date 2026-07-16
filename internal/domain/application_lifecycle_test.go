package domain

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestApp(t *testing.T, name string) ApplicationProviderSpec {
	t.Helper()
	containerApp := ContainerApplication{
		AppType: AppTypeContainer,
		Name:    lo.ToPtr(name),
	}
	require.NoError(t, containerApp.FromImageApplicationProviderSpec(ImageApplicationProviderSpec{Image: "quay.io/test/app:v1"}))
	var app ApplicationProviderSpec
	require.NoError(t, app.FromContainerApplication(containerApp))
	return app
}

func TestOverlayApplicationLifecycle(t *testing.T) {
	t.Run("When apps is nil it should do nothing", func(t *testing.T) {
		require.NoError(t, OverlayApplicationLifecycle(nil, `{"app-1":{"desiredState":"stopped"}}`, ""))
	})

	t.Run("When apps is empty it should do nothing", func(t *testing.T) {
		apps := []ApplicationProviderSpec{}
		require.NoError(t, OverlayApplicationLifecycle(&apps, `{"app-1":{"desiredState":"stopped"}}`, ""))
	})

	t.Run("When the raw annotation is empty it should leave apps unchanged", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		require.NoError(t, OverlayApplicationLifecycle(&apps, "", ""))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState())
	})

	t.Run("When the device-level annotation is invalid JSON it should return an error", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		err := OverlayApplicationLifecycle(&apps, "not-json", "")
		assert.Error(t, err)
	})

	t.Run("When the fleet-level annotation is invalid JSON it should return an error", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		err := OverlayApplicationLifecycle(&apps, "", "not-json")
		assert.Error(t, err)
	})

	t.Run("When an override matches an app by name it should apply only to that app", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1"), newTestApp(t, "app-2")}
		raw := `{"app-1":{"desiredState":"stopped","restartGeneration":3}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, raw, ""))

		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState())
		assert.Equal(t, 3, apps[0].GetRestartGeneration())

		assert.Equal(t, ApplicationDesiredStateRunning, apps[1].GetDesiredState(), "app-2 has no override and should be untouched")
		assert.Equal(t, 0, apps[1].GetRestartGeneration())
	})

	t.Run("When the annotation references an app not present in apps it should be ignored", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		raw := `{"unknown-app":{"desiredState":"stopped"}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, raw, ""))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState())
	})

	t.Run("When only restartGeneration is overridden desiredState should remain default", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		raw := `{"app-1":{"restartGeneration":5}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, raw, ""))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState())
		assert.Equal(t, 5, apps[0].GetRestartGeneration())
	})

	t.Run("When only a fleet-level default is set it should apply to every matching app", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1"), newTestApp(t, "app-2")}
		fleetRaw := `{"app-1":{"desiredState":"stopped"}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, "", fleetRaw))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState())
		assert.Equal(t, ApplicationDesiredStateRunning, apps[1].GetDesiredState())
	})

	t.Run("When both fleet and device set desiredState the more recently versioned one wins", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		fleetRaw := `{"app-1":{"desiredState":"stopped","desiredStateVersion":1}}`
		deviceRaw := `{"app-1":{"desiredState":"running","desiredStateVersion":2}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, deviceRaw, fleetRaw))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState(), "device's version (2) is newer than fleet's (1)")
	})

	t.Run("When the fleet's desiredState is more recent than the device's it wins", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		fleetRaw := `{"app-1":{"desiredState":"stopped","desiredStateVersion":9}}`
		deviceRaw := `{"app-1":{"desiredState":"running","desiredStateVersion":2}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, deviceRaw, fleetRaw))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState(), "fleet's version (9) is newer than device's (2): a later fleet-wide stop overrides an earlier device-level start")
	})

	t.Run("When only one side has a recorded version it wins over the unversioned side", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		fleetRaw := `{"app-1":{"desiredState":"stopped"}}`
		deviceRaw := `{"app-1":{"desiredState":"running","desiredStateVersion":1}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, deviceRaw, fleetRaw))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState(), "a versioned entry is treated as more recent than an unversioned legacy one")
	})

	t.Run("When neither side has a recorded version the device-level value wins", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		fleetRaw := `{"app-1":{"desiredState":"stopped"}}`
		deviceRaw := `{"app-1":{"desiredState":"running"}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, deviceRaw, fleetRaw))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState(), "with no ordering information, legacy behavior of device-wins is preserved")
	})

	t.Run("When the device sets a field the fleet-level default leaves unset it should fall back to the fleet default", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		fleetRaw := `{"app-1":{"desiredState":"stopped"}}`
		deviceRaw := `{"app-1":{"restartGeneration":2}}`
		require.NoError(t, OverlayApplicationLifecycle(&apps, deviceRaw, fleetRaw))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState(), "device override doesn't set desiredState, so the fleet default should still apply")
		assert.Equal(t, 2, apps[0].GetRestartGeneration())
	})
}

func TestMergeApplicationLifecycleOverrides(t *testing.T) {
	t.Run("When there is no existing annotation it should create one from the overrides", func(t *testing.T) {
		merged, err := MergeApplicationLifecycleOverrides("", map[string]ApplicationLifecycleOverride{
			"app-1": NewDesiredStateOverride(ApplicationDesiredStateStopped, 1),
		})
		require.NoError(t, err)

		gen, err := GetApplicationRestartGeneration(merged, "app-1")
		require.NoError(t, err)
		assert.Equal(t, 0, gen)

		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		require.NoError(t, OverlayApplicationLifecycle(&apps, merged, ""))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState())
	})

	t.Run("When an incoming override only sets desiredState it should preserve an existing restartGeneration", func(t *testing.T) {
		existing := `{"app-1":{"restartGeneration":2}}`
		merged, err := MergeApplicationLifecycleOverrides(existing, map[string]ApplicationLifecycleOverride{
			"app-1": NewDesiredStateOverride(ApplicationDesiredStateStopped, 1),
		})
		require.NoError(t, err)

		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		require.NoError(t, OverlayApplicationLifecycle(&apps, merged, ""))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState())
		assert.Equal(t, 2, apps[0].GetRestartGeneration(), "stop/start must not drop a previously-stored restartGeneration")
	})

	t.Run("When an incoming override only sets restartGeneration it should preserve an existing desiredState", func(t *testing.T) {
		existing := `{"app-1":{"desiredState":"stopped","desiredStateVersion":1}}`
		merged, err := MergeApplicationLifecycleOverrides(existing, map[string]ApplicationLifecycleOverride{
			"app-1": NewRestartGenerationOverride(4),
		})
		require.NoError(t, err)

		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		require.NoError(t, OverlayApplicationLifecycle(&apps, merged, ""))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState(), "restart must not drop a previously-stored desiredState")
		assert.Equal(t, 4, apps[0].GetRestartGeneration())
	})

	t.Run("When merging overrides for a different app it should leave other apps' overrides untouched", func(t *testing.T) {
		existing := `{"app-1":{"desiredState":"stopped","desiredStateVersion":1,"restartGeneration":1}}`
		merged, err := MergeApplicationLifecycleOverrides(existing, map[string]ApplicationLifecycleOverride{
			"app-2": NewDesiredStateOverride(ApplicationDesiredStateStopped, 1),
		})
		require.NoError(t, err)

		apps := []ApplicationProviderSpec{newTestApp(t, "app-1"), newTestApp(t, "app-2")}
		require.NoError(t, OverlayApplicationLifecycle(&apps, merged, ""))
		assert.Equal(t, ApplicationDesiredStateStopped, apps[0].GetDesiredState())
		assert.Equal(t, 1, apps[0].GetRestartGeneration())
		assert.Equal(t, ApplicationDesiredStateStopped, apps[1].GetDesiredState())
	})

	t.Run("When a freshly stamped desiredState is merged onto an older stored one it replaces it", func(t *testing.T) {
		existing := `{"app-1":{"desiredState":"stopped","desiredStateVersion":100}}`
		merged, err := MergeApplicationLifecycleOverrides(existing, map[string]ApplicationLifecycleOverride{
			"app-1": NewDesiredStateOverride(ApplicationDesiredStateRunning, 200),
		})
		require.NoError(t, err)

		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		require.NoError(t, OverlayApplicationLifecycle(&apps, merged, ""))
		assert.Equal(t, ApplicationDesiredStateRunning, apps[0].GetDesiredState(), "in practice NewLifecycleVersion always produces a fresher stamp than whatever is already stored, so a newly recorded action always takes effect")
	})

	t.Run("When the existing annotation is invalid JSON it should return an error", func(t *testing.T) {
		_, err := MergeApplicationLifecycleOverrides("not-json", map[string]ApplicationLifecycleOverride{
			"app-1": NewDesiredStateOverride(ApplicationDesiredStateStopped, 1),
		})
		assert.Error(t, err)
	})
}

func TestGetApplicationRestartGeneration(t *testing.T) {
	t.Run("When the annotation is empty it should return 0", func(t *testing.T) {
		gen, err := GetApplicationRestartGeneration("", "app-1")
		require.NoError(t, err)
		assert.Equal(t, 0, gen)
	})

	t.Run("When the app has no override it should return 0", func(t *testing.T) {
		gen, err := GetApplicationRestartGeneration(`{"app-2":{"restartGeneration":7}}`, "app-1")
		require.NoError(t, err)
		assert.Equal(t, 0, gen)
	})

	t.Run("When the app has a restartGeneration override it should return it", func(t *testing.T) {
		gen, err := GetApplicationRestartGeneration(`{"app-1":{"restartGeneration":7}}`, "app-1")
		require.NoError(t, err)
		assert.Equal(t, 7, gen)
	})

	t.Run("When the annotation is invalid JSON it should return an error", func(t *testing.T) {
		_, err := GetApplicationRestartGeneration("not-json", "app-1")
		assert.Error(t, err)
	})
}

func TestNewApplicationLifecycleOverrideConstructors(t *testing.T) {
	t.Run("NewDesiredStateOverride sets DesiredState and DesiredStateVersion", func(t *testing.T) {
		override := NewDesiredStateOverride(ApplicationDesiredStateStopped, 42)
		require.NotNil(t, override.DesiredState)
		assert.Equal(t, ApplicationDesiredStateStopped, *override.DesiredState)
		require.NotNil(t, override.DesiredStateVersion)
		assert.Equal(t, int64(42), *override.DesiredStateVersion)
		assert.Nil(t, override.RestartGeneration)
	})

	t.Run("NewRestartGenerationOverride sets only RestartGeneration", func(t *testing.T) {
		override := NewRestartGenerationOverride(9)
		require.NotNil(t, override.RestartGeneration)
		assert.Equal(t, 9, *override.RestartGeneration)
		assert.Nil(t, override.DesiredState)
		assert.Nil(t, override.DesiredStateVersion)
	})
}

func TestNewLifecycleVersion(t *testing.T) {
	t.Run("It should return non-decreasing values on successive calls", func(t *testing.T) {
		first := NewLifecycleVersion()
		second := NewLifecycleVersion()
		assert.GreaterOrEqual(t, second, first)
	})
}

func TestApplicationsContainName(t *testing.T) {
	t.Run("When apps is nil it should return false", func(t *testing.T) {
		assert.False(t, ApplicationsContainName(nil, "app-1"))
	})

	t.Run("When apps contains the name it should return true", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		assert.True(t, ApplicationsContainName(&apps, "app-1"))
	})

	t.Run("When apps doesn't contain the name it should return false", func(t *testing.T) {
		apps := []ApplicationProviderSpec{newTestApp(t, "app-1")}
		assert.False(t, ApplicationsContainName(&apps, "app-2"))
	})
}
