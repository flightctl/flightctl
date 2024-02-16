package configcontroller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
)

type Ignition struct {
	Raw  json.RawMessage `json:"inline"`
	Name string          `json:"name"`
}

func (c *ConfigController) ensureConfig(_ context.Context, device *v1alpha1.Device) error {
	if device.Spec.Config == nil {
		return fmt.Errorf("device config is nil")
	}

	for _, config := range *device.Spec.Config {
		configBytes, err := json.Marshal(config)
		if err != nil {
			return fmt.Errorf("marshalling config failed: %w", err)
		}

		var ignition Ignition
		err = json.Unmarshal(configBytes, &ignition)
		if err != nil {
			return fmt.Errorf("unmarshalling config failed: %w", err)
		}

		ignitionConfig, err := ParseAndConvertConfig(ignition.Raw)
		if err != nil {
			return fmt.Errorf("parsing and converting config failed: %w", err)
		}

		fmt.Printf("############################ignitionConfig: %+v\n", ignitionConfig)
	}

	return nil
}
