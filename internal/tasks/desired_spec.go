package tasks

import (
	"errors"
	"fmt"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
)

func DesiredSpecFromTemplate(device *domain.Device, tv *domain.TemplateVersion) (*domain.DeviceSpec, error) {
	spec, _, errs := assembleDesiredSpec(device, tv)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	if verrs := spec.Validate(false); len(verrs) > 0 {
		return nil, errors.Join(verrs...)
	}
	return spec, nil
}

func assembleDesiredSpec(device *domain.Device, tv *domain.TemplateVersion) (*domain.DeviceSpec, []model.DependencyRef, []error) {
	var f FleetRolloutsLogic
	errs := []error{}

	var osSpec *domain.DeviceOsSpec
	if tv.Status != nil && tv.Status.Os != nil {
		img, err := ReplaceParametersInString(tv.Status.Os.Image, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in OS image: %w", err))
		} else {
			osSpec = &domain.DeviceOsSpec{Image: img}
		}
	}

	deviceConfig, depRefs, configErrs := f.getDeviceConfig(device, tv)
	errs = append(errs, configErrs...)

	deviceApps, appErrs := f.getDeviceApps(device, tv)
	errs = append(errs, appErrs...)

	if len(errs) > 0 {
		return nil, nil, errs
	}

	spec := &domain.DeviceSpec{
		Config:       deviceConfig,
		Os:           osSpec,
		Applications: deviceApps,
	}
	if tv.Status != nil {
		spec.Systemd = tv.Status.Systemd
		spec.Resources = tv.Status.Resources
		spec.UpdatePolicy = tv.Status.UpdatePolicy
	}
	return spec, depRefs, nil
}
