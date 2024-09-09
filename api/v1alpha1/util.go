package v1alpha1

import (
	"encoding/json"
	"fmt"
)

const (
	SystemdActionType    = "systemd"
	ExecutableActionType = "executable"
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
