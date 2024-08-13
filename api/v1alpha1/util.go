package v1alpha1

import (
	"encoding/json"
	"fmt"
)

const (
	systemdActionType    = "systemd"
	executableActionType = "executable"
)

// Type returns the type of the action.
func (t HookAction) Type() (string, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		return "", err
	}

	if _, exists := data[executableActionType]; exists {
		return executableActionType, nil
	}

	if _, exists := data[systemdActionType]; exists {
		return systemdActionType, nil
	}

	return "", fmt.Errorf("unable to determine action type: %+v", data)
}
