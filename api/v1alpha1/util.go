package v1alpha1

import (
	"encoding/json"
	"fmt"
)

type HookActionType string

const (
	SystemdActionType    HookActionType = "systemd"
	ExecutableActionType HookActionType = "executable"
)

type ApplicationProviderType string

const (
	ImageApplicationProviderType ApplicationProviderType = "image"
)

// Type returns the type of the action.
func (t HookAction) Type() (HookActionType, error) {
	var data map[HookActionType]struct{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ExecutableActionType]; exists {
		return ExecutableActionType, nil
	}

	if _, exists := data[SystemdActionType]; exists {
		return SystemdActionType, nil
	}

	return "", fmt.Errorf("unable to determine action type: %+v", data)
}

// Type returns the type of the application provider.
func (a ApplicationSpec) Type() (ApplicationProviderType, error) {
	return getApplicationType(a.union)
}

// Type returns the type of the application provider.
func (a RenderedApplicationSpec) Type() (ApplicationProviderType, error) {
	return getApplicationType(a.union)
}

func getApplicationType(union json.RawMessage) (ApplicationProviderType, error) {
	var data map[ApplicationProviderType]interface{}
	if err := json.Unmarshal(union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ImageApplicationProviderType]; exists {
		return ImageApplicationProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application provider type: %+v", data)
}
