package v1alpha1

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/samber/lo"
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

func configsAreEqual(c1, c2 *[]DeviceSpec_Config_Item) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 DeviceSpec_Config_Item, item2 DeviceSpec_Config_Item) bool {
		value1, err := item1.ValueByDiscriminator()
		if err != nil {
			return false
		}
		value2, err := item2.ValueByDiscriminator()
		if err != nil {
			return false
		}
		return reflect.DeepEqual(value1, value2)
	})
}

func applicationsAreEqual(c1, c2 *[]ApplicationSpec) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 ApplicationSpec, item2 ApplicationSpec) bool {
		type1, err := item1.Type()
		if err != nil {
			return false
		}
		type2, err := item2.Type()
		if err != nil {
			return false
		}

		if type1 != type2 {
			return false
		}
		switch type1 {
		case string(ImageApplicationProviderType):
			imageSpec1, err := item1.AsImageApplicationProvider()
			if err != nil {
				return false
			}
			imageSpec2, err := item2.AsImageApplicationProvider()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(imageSpec1, imageSpec2)
		default:
			return false
		}
	})
}

func resourcesAreEqual(c1, c2 *[]ResourceMonitor) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 ResourceMonitor, item2 ResourceMonitor) bool {
		value1, err := item1.ValueByDiscriminator()
		if err != nil {
			return false
		}
		value2, err := item2.ValueByDiscriminator()
		if err != nil {
			return false
		}
		return reflect.DeepEqual(value1, value2)
	})
}

func DeviceSpecsAreEqual(d1, d2 DeviceSpec) bool {
	// Check OS
	if !reflect.DeepEqual(d1.Os, d2.Os) {
		return false
	}

	// Check Config
	if !configsAreEqual(d1.Config, d2.Config) {
		return false
	}

	// Check Hooks
	if !reflect.DeepEqual(d1.Hooks, d2.Hooks) {
		return false
	}

	// Check Applications
	if !applicationsAreEqual(d1.Applications, d2.Applications) {
		return false
	}

	// Check Containers
	if !reflect.DeepEqual(d1.Containers, d2.Containers) {
		return false
	}

	// Check Systemd
	if !reflect.DeepEqual(d1.Systemd, d2.Systemd) {
		return false
	}

	// Check Resources
	if !resourcesAreEqual(d1.Resources, d2.Resources) {
		return false
	}

	return true
}

func FleetSpecsAreEqual(f1, f2 FleetSpec) bool {
	if !reflect.DeepEqual(f1.Selector, f2.Selector) {
		return false
	}

	if !reflect.DeepEqual(f1.Template.Metadata, f2.Template.Metadata) {
		return false
	}

	return DeviceSpecsAreEqual(f1.Template.Spec, f2.Template.Spec)
}
