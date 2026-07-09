package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// ApplicationLifecycleOverride is the per-application value type for the
// DeviceAnnotationApplicationLifecycle, DeviceAnnotationFleetApplicationLifecycle, and
// FleetAnnotationApplicationLifecycle annotations. DesiredStateVersion orders desiredState
// changes across the fleet-level and device-level layers so that whichever was set most
// recently wins, regardless of which layer it came from.
type ApplicationLifecycleOverride struct {
	DesiredState        *ApplicationDesiredState `json:"desiredState,omitempty"`
	DesiredStateVersion *int64                   `json:"desiredStateVersion,omitempty"`
	RestartGeneration   *int                     `json:"restartGeneration,omitempty"`
}

func NewLifecycleVersion() int64 {
	return time.Now().UnixNano()
}

func NewDesiredStateOverride(state ApplicationDesiredState, version int64) ApplicationLifecycleOverride {
	return ApplicationLifecycleOverride{DesiredState: &state, DesiredStateVersion: &version}
}

func NewRestartGenerationOverride(generation int) ApplicationLifecycleOverride {
	return ApplicationLifecycleOverride{RestartGeneration: &generation}
}

// OverlayApplicationLifecycle applies the fleet-level lifecycle defaults and the device-level
// lifecycle overrides onto the given application specs, in place. The two layers are merged
// per-application, per-field: restartGeneration always comes from the device layer;
// desiredState is arbitrated by recency (higher DesiredStateVersion wins). fleetRaw is empty
// for standalone devices.
func OverlayApplicationLifecycle(apps *[]ApplicationProviderSpec, deviceRaw string, fleetRaw string) error {
	if apps == nil || len(*apps) == 0 {
		return nil
	}
	overrides, err := mergeApplicationLifecycleLayers(fleetRaw, deviceRaw)
	if err != nil {
		return err
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
		updated, err := applyApplicationLifecycleOverride(app, override)
		if err != nil {
			return fmt.Errorf("failed to apply lifecycle override for application %q: %w", *name, err)
		}
		(*apps)[i] = updated
	}
	return nil
}

func mergeApplicationLifecycleLayers(fleetRaw, deviceRaw string) (map[string]ApplicationLifecycleOverride, error) {
	merged, err := decodeApplicationLifecycleOverrides(fleetRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to decode fleet-level application lifecycle annotation: %w", err)
	}
	deviceOverrides, err := decodeApplicationLifecycleOverrides(deviceRaw)
	if err != nil {
		return nil, fmt.Errorf("failed to decode device-level application lifecycle annotation: %w", err)
	}
	applyOverridesOnTop(merged, deviceOverrides)
	return merged, nil
}

// applyOverridesOnTop merges src's overrides into dst in place, per field: restartGeneration
// from src always replaces dst's when set; desiredState from src only replaces dst's when
// src's entry is not older than dst's.
func applyOverridesOnTop(dst map[string]ApplicationLifecycleOverride, src map[string]ApplicationLifecycleOverride) {
	for name, override := range src {
		merged := dst[name]
		if override.DesiredState != nil && desiredStateIsAtLeastAsRecent(override, merged) {
			merged.DesiredState = override.DesiredState
			merged.DesiredStateVersion = override.DesiredStateVersion
		}
		if override.RestartGeneration != nil {
			merged.RestartGeneration = override.RestartGeneration
		}
		dst[name] = merged
	}
}

// desiredStateIsAtLeastAsRecent reports whether src's desiredState should replace dst's.
// An entry with no recorded version is treated as older than any versioned entry; if neither
// side has a version, src wins.
func desiredStateIsAtLeastAsRecent(src, dst ApplicationLifecycleOverride) bool {
	if dst.DesiredState == nil {
		return true
	}
	if src.DesiredStateVersion == nil {
		return dst.DesiredStateVersion == nil
	}
	if dst.DesiredStateVersion == nil {
		return true
	}
	return *src.DesiredStateVersion >= *dst.DesiredStateVersion
}

// MergeApplicationLifecycleOverrides decodes the existing lifecycle annotation value, merges
// the given per-application overrides on top (per field, so a stop call never drops a
// previously-stored restartGeneration and vice versa), and re-encodes the result.
func MergeApplicationLifecycleOverrides(raw string, overrides map[string]ApplicationLifecycleOverride) (string, error) {
	existing, err := decodeApplicationLifecycleOverrides(raw)
	if err != nil {
		return "", err
	}
	applyOverridesOnTop(existing, overrides)
	encoded, err := json.Marshal(existing)
	if err != nil {
		return "", fmt.Errorf("failed to marshal application lifecycle annotation: %w", err)
	}
	return string(encoded), nil
}

func ApplicationsContainName(apps *[]ApplicationProviderSpec, appName string) bool {
	if apps == nil {
		return false
	}
	for _, app := range *apps {
		if name, err := app.GetName(); err == nil && name != nil && *name == appName {
			return true
		}
	}
	return false
}

// GetApplicationRestartGeneration returns the current restartGeneration override for the
// named application, defaulting to 0 if unset.
func GetApplicationRestartGeneration(raw string, appName string) (int, error) {
	overrides, err := decodeApplicationLifecycleOverrides(raw)
	if err != nil {
		return 0, err
	}
	override, ok := overrides[appName]
	if !ok || override.RestartGeneration == nil {
		return 0, nil
	}
	return *override.RestartGeneration, nil
}

func decodeApplicationLifecycleOverrides(raw string) (map[string]ApplicationLifecycleOverride, error) {
	overrides := map[string]ApplicationLifecycleOverride{}
	if raw == "" {
		return overrides, nil
	}
	if err := json.Unmarshal([]byte(raw), &overrides); err != nil {
		return nil, fmt.Errorf("failed to unmarshal application lifecycle annotation: %w", err)
	}
	return overrides, nil
}

func applyApplicationLifecycleOverride(app ApplicationProviderSpec, override ApplicationLifecycleOverride) (ApplicationProviderSpec, error) {
	appType, err := app.GetAppType()
	if err != nil {
		return app, err
	}
	switch appType {
	case AppTypeContainer:
		typed, err := app.AsContainerApplication()
		if err != nil {
			return app, err
		}
		setLifecycleFields(override, &typed.DesiredState, &typed.RestartGeneration)
		err = app.FromContainerApplication(typed)
		return app, err
	case AppTypeHelm:
		typed, err := app.AsHelmApplication()
		if err != nil {
			return app, err
		}
		setLifecycleFields(override, &typed.DesiredState, &typed.RestartGeneration)
		err = app.FromHelmApplication(typed)
		return app, err
	case AppTypeCompose:
		typed, err := app.AsComposeApplication()
		if err != nil {
			return app, err
		}
		setLifecycleFields(override, &typed.DesiredState, &typed.RestartGeneration)
		err = app.FromComposeApplication(typed)
		return app, err
	case AppTypeQuadlet:
		typed, err := app.AsQuadletApplication()
		if err != nil {
			return app, err
		}
		setLifecycleFields(override, &typed.DesiredState, &typed.RestartGeneration)
		err = app.FromQuadletApplication(typed)
		return app, err
	case AppTypeVm:
		typed, err := app.AsVmApplication()
		if err != nil {
			return app, err
		}
		setLifecycleFields(override, &typed.DesiredState, &typed.RestartGeneration)
		err = app.FromVmApplication(typed)
		return app, err
	default:
		return app, fmt.Errorf("unknown app type: %s", appType)
	}
}

func setLifecycleFields(override ApplicationLifecycleOverride, desiredState **ApplicationDesiredState, restartGeneration **int) {
	if override.DesiredState != nil {
		*desiredState = override.DesiredState
	}
	if override.RestartGeneration != nil {
		*restartGeneration = override.RestartGeneration
	}
}
