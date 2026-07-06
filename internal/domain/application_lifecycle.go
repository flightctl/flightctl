package domain

import (
	"encoding/json"
	"fmt"
)

// OverlayApplicationLifecycle applies the device's per-application lifecycle control
// overrides (see DeviceAnnotationApplicationLifecycle) on top of the given application
// specs, in place. Callers apply this both when serving the device's rendered
// applications (see model.Device.ToApiResource) and when a fleet rollout re-templates a
// fleet-owned device's spec, so lifecycle overrides always survive fleet template
// rollouts and are never persisted as part of a fleet template or device spec.
func OverlayApplicationLifecycle(apps *[]ApplicationProviderSpec, raw string) error {
	if apps == nil || len(*apps) == 0 {
		return nil
	}
	var overrides map[string]DeviceApplicationLifecycle
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return fmt.Errorf("failed to unmarshal application lifecycle annotation: %w", err)
	}
	if len(overrides) == 0 {
		return nil
	}
	for i, app := range *apps {
		name, err := app.GetName()
		if err != nil || name == nil {
			continue
		}
		override, ok := overrides[*name]
		if !ok {
			continue
		}
		updated, err := ApplyApplicationLifecycleOverride(app, override)
		if err != nil {
			return fmt.Errorf("failed to apply lifecycle override for application %q: %w", *name, err)
		}
		(*apps)[i] = updated
	}
	return nil
}

// ApplyApplicationLifecycleOverride sets the desiredState/restartGeneration fields on the
// application's JSON representation, regardless of its concrete provider type.
func ApplyApplicationLifecycleOverride(app ApplicationProviderSpec, override DeviceApplicationLifecycle) (ApplicationProviderSpec, error) {
	raw, err := app.MarshalJSON()
	if err != nil {
		return app, err
	}
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &fields); err != nil {
		return app, err
	}
	if override.DesiredState != nil {
		b, err := json.Marshal(override.DesiredState)
		if err != nil {
			return app, err
		}
		fields["desiredState"] = b
	}
	if override.RestartGeneration != nil {
		b, err := json.Marshal(override.RestartGeneration)
		if err != nil {
			return app, err
		}
		fields["restartGeneration"] = b
	}
	newRaw, err := json.Marshal(fields)
	if err != nil {
		return app, err
	}
	var newApp ApplicationProviderSpec
	if err := newApp.UnmarshalJSON(newRaw); err != nil {
		return app, err
	}
	return newApp, nil
}
