package service

import (
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

func NilOutManagedObjectMetaProperties(om *api.ObjectMeta) {
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}

func ValidateDiscriminators(config *[]api.DeviceSpec_Config_Item) error {
	if config == nil {
		return nil
	}
	for _, config := range *config {
		discriminator, err := config.Discriminator()
		if err != nil {
			return err
		}
		found := false
		discriminators := []string{
			string(api.TemplateDiscriminatorGitConfig),
			string(api.TemplateDiscriminatorKubernetesSec),
			string(api.TemplateDiscriminatorInlineConfig)}
		for _, d := range discriminators {
			if discriminator == d {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("configType must be one of %s", strings.Join(discriminators, ", "))
		}
	}
	return nil
}

func ValidateSettings(settings *api.DeviceSettings) error {
	if settings == nil {
		return nil
	}

	var warnTime *time.Duration
	var errTime *time.Duration

	if settings.HeartbeatWarningTime != nil {
		t, err := time.ParseDuration(*settings.HeartbeatWarningTime)
		if err != nil {
			return fmt.Errorf("failed parsing HeartbeatWarningTime: %v", err.Error())
		}
		warnTime = &t
	}

	if settings.HeartbeatErrorTime != nil {
		t, err := time.ParseDuration(*settings.HeartbeatErrorTime)
		if err != nil {
			return fmt.Errorf("failed parsing HeartbeatErrorTime: %v", err.Error())
		}
		errTime = &t
	}

	if warnTime != nil && errTime != nil {
		if *warnTime > *errTime {
			return fmt.Errorf("HeartbeatWarningTime cannot be greater than HeartbeatErrorTime")
		}
	}
	return nil
}
