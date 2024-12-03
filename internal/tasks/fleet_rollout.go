package tasks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/sirupsen/logrus"
)

func fleetRollout(ctx context.Context, resourceRef *ResourceReference, store store.Store, callbackManager CallbackManager, log logrus.FieldLogger) error {
	if resourceRef.Op != FleetRolloutOpUpdate {
		log.Errorf("received unknown op %s", resourceRef.Op)
		return nil
	}
	logic := NewFleetRolloutsLogic(callbackManager, log, store, *resourceRef)
	switch resourceRef.Kind {
	case api.FleetKind:
		err := logic.RolloutFleet(ctx)
		if err != nil {
			log.Errorf("failed rolling out fleet %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
		return err
	case api.DeviceKind:
		err := logic.RolloutDevice(ctx)
		if err != nil {
			log.Errorf("failed rolling out device %s/%s: %v", resourceRef.OrgID, resourceRef.Name, err)
		}
		return err
	default:
		return fmt.Errorf("FleetRollouts called with incorrect resource kind %s", resourceRef.Kind)
	}
}

type FleetRolloutsLogic struct {
	callbackManager CallbackManager
	log             logrus.FieldLogger
	fleetStore      store.Fleet
	devStore        store.Device
	tvStore         store.TemplateVersion
	resourceRef     ResourceReference
	itemsPerPage    int
	owner           string
}

func NewFleetRolloutsLogic(callbackManager CallbackManager, log logrus.FieldLogger, storeInst store.Store, resourceRef ResourceReference) FleetRolloutsLogic {
	return FleetRolloutsLogic{
		callbackManager: callbackManager,
		log:             log,
		fleetStore:      storeInst.Fleet(),
		devStore:        storeInst.Device(),
		tvStore:         storeInst.TemplateVersion(),
		resourceRef:     resourceRef,
		itemsPerPage:    ItemsPerPage,
	}
}

func (f *FleetRolloutsLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

func (f FleetRolloutsLogic) RolloutFleet(ctx context.Context) error {
	f.log.Infof("Rolling out fleet %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	templateVersion, err := f.tvStore.GetLatest(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get templateVersion: %w", err)
	}

	failureCount := 0
	owner := util.SetResourceOwner(api.FleetKind, f.resourceRef.Name)
	f.owner = *owner

	listParams := store.ListParams{Owners: []string{*owner}, Limit: ItemsPerPage}

	for {
		devices, err := f.devStore.List(ctx, f.resourceRef.OrgID, listParams)
		if err != nil {
			// TODO: Retry when we have a mechanism that allows it
			return fmt.Errorf("failed fetching devices: %w", err)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			err = f.updateDeviceToFleetTemplate(ctx, device, templateVersion)
			if err != nil {
				f.log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, f.resourceRef.Name, err)
				failureCount++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		} else {
			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			if err != nil {
				return fmt.Errorf("failed to parse continuation for paging: %w", err)
			}
			listParams.Continue = cont
		}
	}

	if failureCount != 0 {
		// TODO: Retry when we have a mechanism that allows it
		return fmt.Errorf("failed updating %d devices", failureCount)
	}

	return nil
}

// The device's owner was changed, roll out if necessary
func (f FleetRolloutsLogic) RolloutDevice(ctx context.Context) error {
	f.log.Infof("Rolling out device %s/%s", f.resourceRef.OrgID, f.resourceRef.Name)

	device, err := f.devStore.Get(ctx, f.resourceRef.OrgID, f.resourceRef.Name)
	if err != nil {
		return fmt.Errorf("failed to get device: %w", err)
	}

	if device.Metadata.Owner == nil || len(*device.Metadata.Owner) == 0 {
		return nil
	}

	if api.IsStatusConditionTrue(device.Status.Conditions, api.DeviceMultipleOwners) {
		f.log.Warnf("Device has multiple owners, skipping rollout")
	}

	ownerName, isFleetOwner, err := getOwnerFleet(device)
	if err != nil {
		return fmt.Errorf("failed getting device owner: %w", err)
	}
	if !isFleetOwner {
		return nil
	}
	f.owner = *device.Metadata.Owner

	templateVersion, err := f.tvStore.GetLatest(ctx, f.resourceRef.OrgID, ownerName)
	if err != nil {
		return fmt.Errorf("failed to get templateVersion: %w", err)
	}

	return f.updateDeviceToFleetTemplate(ctx, device, templateVersion)
}

func (f FleetRolloutsLogic) updateDeviceToFleetTemplate(ctx context.Context, device *api.Device, templateVersion *api.TemplateVersion) error {
	currentVersion := ""
	if device.Metadata.Annotations != nil {
		v, ok := (*device.Metadata.Annotations)[api.DeviceAnnotationTemplateVersion]
		if ok {
			currentVersion = v
		}
	}

	deviceConfig, err := f.getDeviceConfig(device, templateVersion)
	if err != nil {
		return err
	}
	deviceApps, err := f.getDeviceApps(device, templateVersion)
	if err != nil {
		return err
	}

	newDeviceSpec := api.DeviceSpec{
		Config:       deviceConfig,
		Os:           templateVersion.Status.Os,
		Systemd:      templateVersion.Status.Systemd,
		Resources:    templateVersion.Status.Resources,
		Applications: deviceApps,
	}

	if currentVersion == *templateVersion.Metadata.Name && api.DeviceSpecsAreEqual(newDeviceSpec, *device.Spec) {
		f.log.Debugf("Not rolling out device %s/%s because it is already at templateVersion %s", f.resourceRef.OrgID, *device.Metadata.Name, *templateVersion.Metadata.Name)
		return nil
	}

	f.log.Infof("Rolling out device %s/%s to templateVersion %s", f.resourceRef.OrgID, *device.Metadata.Name, *templateVersion.Metadata.Name)
	err = f.updateDeviceInStore(ctx, device, &newDeviceSpec)
	if err != nil {
		return fmt.Errorf("failed updating device spec: %w", err)
	}

	annotations := map[string]string{
		api.DeviceAnnotationTemplateVersion: *templateVersion.Metadata.Name,
	}
	err = f.devStore.UpdateAnnotations(ctx, f.resourceRef.OrgID, *device.Metadata.Name, annotations, nil)
	if err != nil {
		return fmt.Errorf("failed updating templateVersion annotation: %w", err)
	}

	return err
}

func (f FleetRolloutsLogic) getDeviceApps(device *api.Device, templateVersion *api.TemplateVersion) (*[]api.ApplicationSpec, error) {
	if templateVersion.Status.Applications == nil {
		return nil, nil
	}

	deviceApps := []api.ApplicationSpec{}
	for _, app := range *templateVersion.Status.Applications {
		appType, err := app.Type()
		if err != nil {
			return nil, fmt.Errorf("failed getting app type: %w", err)
		}
		switch appType {
		case api.ImageApplicationProviderType:
			newApp, err := f.replaceEnvVarValueParameters(device, app)
			if err != nil {
				return nil, err
			}
			deviceApps = append(deviceApps, *newApp)
		default:
			return nil, fmt.Errorf("unsupported application type: %s", appType)
		}
	}

	return &deviceApps, nil
}

func (f FleetRolloutsLogic) replaceEnvVarValueParameters(device *api.Device, app api.ApplicationSpec) (*api.ApplicationSpec, error) {
	if app.EnvVars == nil {
		return &app, nil
	}

	origEnvVars := *app.EnvVars
	newEnvVars := make(map[string]string, len(origEnvVars))
	for k, v := range origEnvVars {
		newValue, warnings := ReplaceParameters([]byte(v), device.Metadata)
		if len(warnings) > 0 {
			f.log.Infof("failed replacing application parameters for device %s/%s: %s", f.resourceRef.OrgID, *device.Metadata.Name, strings.Join(warnings, ", "))
		}
		newEnvVars[k] = string(newValue)
	}
	app.EnvVars = &newEnvVars
	return &app, nil
}

func (f FleetRolloutsLogic) getDeviceConfig(device *api.Device, templateVersion *api.TemplateVersion) (*[]api.ConfigProviderSpec, error) {
	if templateVersion.Status.Config == nil {
		return nil, nil
	}

	deviceConfig := []api.ConfigProviderSpec{}
	for _, configItem := range *templateVersion.Status.Config {
		var newConfigItem *api.ConfigProviderSpec
		var err error

		// We need to decode the inline spec contents before replacing parameters
		configType, err := configItem.Type()
		if err != nil {
			return nil, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err)
		}

		switch configType {
		case api.GitConfigProviderType:
			newConfigItem, err = f.replaceGenericConfigParameters(device, configItem)
		case api.KubernetesSecretProviderType:
			newConfigItem, err = f.replaceGenericConfigParameters(device, configItem)
		case api.InlineConfigProviderType:
			newConfigItem, err = f.replaceInlineSpecParameters(device, configItem)
		case api.HttpConfigProviderType:
			newConfigItem, err = f.replaceGenericConfigParameters(device, configItem)
		default:
			err = fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType)
		}
		if err != nil {
			return nil, err
		}

		deviceConfig = append(deviceConfig, *newConfigItem)
	}

	return &deviceConfig, nil
}

func (f FleetRolloutsLogic) replaceInlineSpecParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, fmt.Errorf("failed to convert config to inline config: %w", err)
	}

	for fileIndex, file := range inlineSpec.Inline {
		var decodedBytes []byte
		var err error

		if file.ContentEncoding == nil {
			decodedBytes = []byte(file.Content)
		} else {
			decodedBytes, err = base64.StdEncoding.DecodeString(file.Content)
			if err != nil {
				return nil, fmt.Errorf("error base64 decoding: %w", err)
			}
		}

		contentsReplaced, warnings := ReplaceParameters(decodedBytes, device.Metadata)
		if len(warnings) > 0 {
			f.log.Infof("failed replacing parameters for device %s/%s: %s", f.resourceRef.OrgID, *device.Metadata.Name, strings.Join(warnings, ", "))
		}

		if file.ContentEncoding != nil && (*file.ContentEncoding) == api.Base64 {
			inlineSpec.Inline[fileIndex].Content = base64.StdEncoding.EncodeToString(contentsReplaced)
		} else {
			inlineSpec.Inline[fileIndex].Content = string(contentsReplaced)
		}
	}

	newConfigItem := api.ConfigProviderSpec{}
	err = newConfigItem.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return nil, fmt.Errorf("failed converting inline config: %w", err)
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) replaceGenericConfigParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, error) {
	cfgJson, err := configItem.MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("failed converting configuration to json: %w", err)
	}

	cfgJsonReplaced, warnings := ReplaceParameters(cfgJson, device.Metadata)
	if len(warnings) > 0 {
		f.log.Infof("failed replacing parameters for device %s/%s: %s", f.resourceRef.OrgID, *device.Metadata.Name, strings.Join(warnings, ", "))
	}

	var newConfigItem api.ConfigProviderSpec
	err = newConfigItem.UnmarshalJSON(cfgJsonReplaced)
	if err != nil {
		return nil, fmt.Errorf("failed converting configuration from json: %w", err)
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) updateDeviceInStore(ctx context.Context, device *api.Device, newDeviceSpec *api.DeviceSpec) error {
	var err error

	for i := 0; i < 10; i++ {
		if device.Metadata.Owner == nil || *device.Metadata.Owner != f.owner {
			return fmt.Errorf("device owner changed, skipping rollout")
		}

		device.Spec = newDeviceSpec
		_, err = f.devStore.Update(ctx, f.resourceRef.OrgID, device, nil, false, f.callbackManager.DeviceUpdatedCallback)
		if err != nil {
			if errors.Is(err, flterrors.ErrResourceVersionConflict) {
				device, err = f.devStore.Get(ctx, f.resourceRef.OrgID, *device.Metadata.Name)
				if err != nil {
					return fmt.Errorf("the device changed before we could update it, and we failed to fetch it again: %v", err)
				}
			} else {
				return err
			}
		} else {
			break
		}
	}

	return err
}
