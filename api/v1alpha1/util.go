package v1alpha1

import (
	"encoding/json"
	"fmt"
)

const (
	SystemdActionType            = "systemd"
	ExecutableActionType         = "executable"
	ImageApplicationProviderType = "image"
)

// Type returns the type of the action.
func (t HookAction) Type() (string, error) {
	var data map[string]interface{}
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
func (a ApplicationSpec) Type() (string, error) {
	return getApplicationType(a.union)
}

// Type returns the type of the application provider.
func (a RenderedApplicationSpec) Type() (string, error) {
	return getApplicationType(a.union)
}

func getApplicationType(union json.RawMessage) (string, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(union, &data); err != nil {
		return "", err
	}

	if _, exists := data[ImageApplicationProviderType]; exists {
		return ImageApplicationProviderType, nil
	}

	return "", fmt.Errorf("unable to determine application provider type: %+v", data)
}
