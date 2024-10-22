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
	HookActionTypeRun HookActionType = "run"
)

type HookConditionType string

const (
	HookConditionTypePathOp     HookConditionType = "pathOp"
	HookConditionTypeExpression HookConditionType = "expression"
)

type ConfigProviderType string

const (
	GitConfigProviderType        ConfigProviderType = "gitRef"
	HttpConfigProviderType       ConfigProviderType = "httpRef"
	InlineConfigProviderType     ConfigProviderType = "inline"
	KubernetesSecretProviderType ConfigProviderType = "secretRef"
)

type ApplicationProviderType string

const (
	ImageApplicationProviderType ApplicationProviderType = "image"
)

// Type returns the type of the action.
func (t HookAction) Type() (HookActionType, error) {
	var data map[HookActionType]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		return "", err
	}

	types := []HookActionType{
		HookActionTypeRun,
	}
	for _, t := range types {
		if _, exists := data[t]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine hook action type: %+v", data)
}

// Type returns the type of the condition.
func (t HookCondition) Type() (HookConditionType, error) {
	var data map[string]interface{}
	if err := json.Unmarshal(t.union, &data); err != nil {
		var data HookConditionExpression
		if err := json.Unmarshal(t.union, &data); err != nil {
			return "", err
		}
		return HookConditionTypeExpression, nil
	}

	types := []HookConditionType{
		HookConditionTypePathOp,
	}
	for _, t := range types {
		if _, exists := data[string(t)]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine hook condition type: %+v", data)
}

// Type returns the type of the config provider.
func (c ConfigProviderSpec) Type() (ConfigProviderType, error) {
	var data map[ConfigProviderType]interface{}
	if err := json.Unmarshal(c.union, &data); err != nil {
		return "", err
	}

	types := []ConfigProviderType{
		GitConfigProviderType,
		HttpConfigProviderType,
		InlineConfigProviderType,
		KubernetesSecretProviderType,
	}
	for _, t := range types {
		if _, exists := data[t]; exists {
			return t, nil
		}
	}

	return "", fmt.Errorf("unable to determine config provider type: %+v", data)
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

func configsAreEqual(c1, c2 *[]ConfigProviderSpec) bool {
	return slices.EqualFunc(lo.FromPtr(c1), lo.FromPtr(c2), func(item1 ConfigProviderSpec, item2 ConfigProviderSpec) bool {
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
		case GitConfigProviderType:
			c1, err := item1.AsGitConfigProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsGitConfigProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		case HttpConfigProviderType:
			c1, err := item1.AsHttpConfigProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsHttpConfigProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		case InlineConfigProviderType:
			c1, err := item1.AsInlineConfigProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsInlineConfigProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		case KubernetesSecretProviderType:
			c1, err := item1.AsKubernetesSecretProviderSpec()
			if err != nil {
				return false
			}
			c2, err := item2.AsKubernetesSecretProviderSpec()
			if err != nil {
				return false
			}
			return reflect.DeepEqual(c1, c2)
		default:
			return false
		}
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
		case ImageApplicationProviderType:
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

	// Check Applications
	if !applicationsAreEqual(d1.Applications, d2.Applications) {
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
