package domain

import (
	"encoding/json"
	"fmt"
	"time"
)

// ApplicationLifecycleOverride is the per-application value type for the
// DeviceAnnotationApplicationLifecycle, DeviceAnnotationFleetApplicationLifecycle, and
// FleetAnnotationApplicationLifecycle annotations: a device- or fleet-level override of an
// application's desiredState/restartGeneration, set by the dedicated stop/start/restart
// device/fleet APIs and re-applied on top of the rendered application spec at render time (see
// OverlayApplicationLifecycle). DesiredStateVersion orders DesiredState changes across the
// fleet-level and device-level layers so that whichever was set most recently wins regardless
// of which layer it came from (see applyOverridesOnTop); RestartGeneration needs no such
// ordering since restart is device-only and never appears in a fleet-level annotation.
type ApplicationLifecycleOverride struct {
	DesiredState        *ApplicationDesiredState `json:"desiredState,omitempty"`
	DesiredStateVersion *int64                   `json:"desiredStateVersion,omitempty"`
	RestartGeneration   *int                     `json:"restartGeneration,omitempty"`
}

// NewLifecycleVersion returns a fresh ordering stamp for a desiredState change, used to
// arbitrate between a fleet-level default and a device-level override for the same
// application (see applyOverridesOnTop): whichever side's entry has the higher version was
// written more recently and wins, regardless of which layer it came from. Backed by a
// wall-clock timestamp since lifecycle actions are human/API-paced and NTP-synchronized
// clusters keep clocks within milliseconds of each other.
func NewLifecycleVersion() int64 {
	return time.Now().UnixNano()
}

// NewDesiredStateOverride builds an ApplicationLifecycleOverride that only sets desiredState,
// stamped with version so it can be arbitrated against the other layer's entry for the same
// application (see applyOverridesOnTop). Used by the stop/start device/fleet APIs.
func NewDesiredStateOverride(state ApplicationDesiredState, version int64) ApplicationLifecycleOverride {
	return ApplicationLifecycleOverride{DesiredState: &state, DesiredStateVersion: &version}
}

// NewRestartGenerationOverride builds an ApplicationLifecycleOverride that only sets
// restartGeneration, used by the restart device API.
func NewRestartGenerationOverride(generation int) ApplicationLifecycleOverride {
	return ApplicationLifecycleOverride{RestartGeneration: &generation}
}

// OverlayApplicationLifecycle applies the fleet-level lifecycle defaults (see
// FleetAnnotationApplicationLifecycle) and the device-level lifecycle overrides (see
// DeviceAnnotationApplicationLifecycle) onto the given application specs, in place. Applied by
// the device render task right before rendering, so the result is baked into
// RenderedApplications only and never persisted back into the device's Spec.
// The two layers are merged per-application, per-field: restartGeneration always comes from
// the device layer (restart is device-only). desiredState is arbitrated by recency: whichever
// layer's entry carries the higher DesiredStateVersion wins, so a fleet-wide stop/start and a
// later per-device stop/start can each override the other depending on which happened last
// (see applyOverridesOnTop). A device-level entry left unset for a given field falls back to
// the fleet-level default for that field. fleetRaw is empty for standalone devices (no owning
// fleet).
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

// mergeApplicationLifecycleLayers decodes the fleet-level default and device-level override
// annotation values and merges them per application/per-field (see applyOverridesOnTop).
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

// applyOverridesOnTop merges src's overrides into dst in place, per-field. restartGeneration
// from src always replaces dst's when set (restart is device-only, so src is always the
// device layer whenever this field is present). desiredState from src only replaces dst's
// when src's entry is not older than dst's (see desiredStateIsAtLeastAsRecent): this makes the
// function usable both to fold a freshly-minted action onto an existing same-layer annotation
// (MergeApplicationLifecycleOverrides, where src's fresh version is always the newest) and to
// merge the fleet and device layers at render time (mergeApplicationLifecycleLayers), where
// either layer may hold the more recent change.
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

// desiredStateIsAtLeastAsRecent reports whether src's desiredState should replace dst's: true
// if dst has no desiredState yet, or if src's version is greater than or equal to dst's. An
// entry with no recorded version is treated as older than any versioned entry; if neither side
// has a version (data predating this field), src wins, preserving the original device-always-
// wins behavior for that legacy case.
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

// MergeApplicationLifecycleOverrides decodes the existing DeviceAnnotationApplicationLifecycle
// or FleetAnnotationApplicationLifecycle annotation value, merges the given per-application
// overrides on top, and re-encodes the result. The merge is per-field: an incoming override
// only replaces the fields it sets (non-nil), preserving any existing field left unset by the
// incoming override, so that e.g. a stop call (which only sets desiredState) never drops a
// previously-stored restartGeneration for the same application, and vice versa. A freshly
// stamped desiredState (see NewLifecycleVersion) always replaces whatever was previously
// stored for that field, since it is by construction the most recent change.
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

// ApplicationsContainName reports whether apps contains an application named appName. Used by
// the device- and fleet-scoped lifecycle APIs to validate appName against the caller's
// declarative application list (device spec or fleet template) before recording an override.
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
// named application out of the raw DeviceAnnotationApplicationLifecycle annotation value,
// defaulting to 0 if unset. Used by the restart device API to compute the next generation.
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

// applyApplicationLifecycleOverride sets the desiredState/restartGeneration fields on the
// application's concrete underlying type (ContainerApplication, ComposeApplication, etc.).
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

// setLifecycleFields copies the non-nil fields of override onto the given desiredState/
// restartGeneration pointers.
func setLifecycleFields(override ApplicationLifecycleOverride, desiredState **ApplicationDesiredState, restartGeneration **int) {
	if override.DesiredState != nil {
		*desiredState = override.DesiredState
	}
	if override.RestartGeneration != nil {
		*restartGeneration = override.RestartGeneration
	}
}
