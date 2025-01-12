package tasks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"text/template"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
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

	fs, err := selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": *owner}, false)
	if err != nil {
		return err
	}

	listParams := store.ListParams{
		Limit:         ItemsPerPage,
		FieldSelector: fs,
	}

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
	errs := []error{}

	var osSpec *api.DeviceOsSpec
	if templateVersion.Status.Os != nil {
		img, err := replaceParametersInString(templateVersion.Status.Os.Image, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in OS image: %w", err))
		} else {
			osSpec = &api.DeviceOsSpec{Image: img}
		}
	}

	deviceConfig, configErrs := f.getDeviceConfig(device, templateVersion)
	errs = append(errs, configErrs...)

	deviceApps, appErrs := f.getDeviceApps(device, templateVersion)
	errs = append(errs, appErrs...)

	if len(errs) > 0 {
		return fmt.Errorf("failed generating device spec for %s/%s: %w", f.resourceRef.OrgID, *device.Metadata.Name, errors.Join(errs...))
	}

	newDeviceSpec := api.DeviceSpec{
		Config:       deviceConfig,
		Os:           osSpec,
		Systemd:      templateVersion.Status.Systemd,
		Resources:    templateVersion.Status.Resources,
		Applications: deviceApps,
		UpdatePolicy: templateVersion.Status.UpdatePolicy,
	}

	errs = newDeviceSpec.Validate(false)
	if len(errs) > 0 {
		return fmt.Errorf("failed validating device spec for %s/%s: %w", f.resourceRef.OrgID, *device.Metadata.Name, errors.Join(errs...))
	}

	if currentVersion == *templateVersion.Metadata.Name && api.DeviceSpecsAreEqual(newDeviceSpec, *device.Spec) {
		f.log.Debugf("Not rolling out device %s/%s because it is already at templateVersion %s", f.resourceRef.OrgID, *device.Metadata.Name, *templateVersion.Metadata.Name)
		return nil
	}

	f.log.Infof("Rolling out device %s/%s to templateVersion %s", f.resourceRef.OrgID, *device.Metadata.Name, *templateVersion.Metadata.Name)
	err := f.updateDeviceInStore(ctx, device, &newDeviceSpec)
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

func (f FleetRolloutsLogic) getDeviceApps(device *api.Device, templateVersion *api.TemplateVersion) (*[]api.ApplicationSpec, []error) {
	if templateVersion.Status.Applications == nil {
		return nil, nil
	}
	errs := []error{}

	deviceApps := []api.ApplicationSpec{}
	for appIndex, app := range *templateVersion.Status.Applications {
		appType, err := app.Type()
		if err != nil {
			errs = append(errs, fmt.Errorf("failed getting type for app %d: %w", appIndex, err))
			continue
		}
		switch appType {
		case api.ImageApplicationProviderType:
			newApp, err := f.replaceEnvVarValueParameters(device, app)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed replacing parameters for app %d: %w", appIndex, err))
				continue
			}
			deviceApps = append(deviceApps, *newApp)
		default:
			errs = append(errs, fmt.Errorf("unsupported type for app %d: %s", appIndex, appType))
		}
	}

	return &deviceApps, errs
}

func (f FleetRolloutsLogic) replaceEnvVarValueParameters(device *api.Device, app api.ApplicationSpec) (*api.ApplicationSpec, error) {
	if app.EnvVars == nil {
		return &app, nil
	}

	origEnvVars := *app.EnvVars
	newEnvVars := make(map[string]string, len(origEnvVars))
	for k, v := range origEnvVars {
		newValue, err := replaceParametersInString(v, device)
		if err != nil {
			return nil, fmt.Errorf("failed replacing application parameters: %w", err)
		}
		newEnvVars[k] = newValue
	}
	app.EnvVars = &newEnvVars
	return &app, nil
}

func (f FleetRolloutsLogic) getDeviceConfig(device *api.Device, templateVersion *api.TemplateVersion) (*[]api.ConfigProviderSpec, []error) {
	if templateVersion.Status.Config == nil {
		return nil, nil
	}

	deviceConfig := []api.ConfigProviderSpec{}
	configErrs := []error{}
	for _, configItem := range *templateVersion.Status.Config {
		var newConfigItem *api.ConfigProviderSpec
		errs := []error{}

		configType, err := configItem.Type()
		if err != nil {
			configErrs = append(configErrs, fmt.Errorf("%w: failed getting config type: %w", ErrUnknownConfigName, err))
			continue
		}

		switch configType {
		case api.GitConfigProviderType:
			newConfigItem, errs = f.replaceGitConfigParameters(device, configItem)
		case api.KubernetesSecretProviderType:
			newConfigItem, errs = f.replaceKubeSecretConfigParameters(device, configItem)
		case api.InlineConfigProviderType:
			newConfigItem, errs = f.replaceInlineConfigParameters(device, configItem)
		case api.HttpConfigProviderType:
			newConfigItem, errs = f.replaceHTTPConfigParameters(device, configItem)
		default:
			errs = append(errs, fmt.Errorf("%w: unsupported config type %q", ErrUnknownConfigName, configType))
		}

		configErrs = append(configErrs, errs...)
		if newConfigItem != nil {
			deviceConfig = append(deviceConfig, *newConfigItem)
		}
	}

	if len(configErrs) > 0 {
		return nil, configErrs
	}

	return &deviceConfig, nil
}

func (f FleetRolloutsLogic) replaceGitConfigParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, []error) {
	gitSpec, err := configItem.AsGitConfigProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert config to git config: %w", err)}
	}

	errs := []error{}

	gitSpec.GitRef.TargetRevision, err = replaceParametersInString(gitSpec.GitRef.TargetRevision, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in targetRevision in git config %s: %w", gitSpec.Name, err))
	}

	gitSpec.GitRef.Path, err = replaceParametersInString(gitSpec.GitRef.Path, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in path in git config %s: %w", gitSpec.Name, err))
	}

	if gitSpec.GitRef.MountPath != nil {
		mountPath, err := replaceParametersInString(*gitSpec.GitRef.MountPath, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in mountPath in git config %s: %w", gitSpec.Name, err))
		}
		gitSpec.GitRef.MountPath = &mountPath
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newConfigItem := api.ConfigProviderSpec{}
	err = newConfigItem.FromGitConfigProviderSpec(gitSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting git config: %w", err)}
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) replaceKubeSecretConfigParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, []error) {
	secretSpec, err := configItem.AsKubernetesSecretProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert config to git config: %w", err)}
	}

	errs := []error{}

	secretSpec.SecretRef.Name, err = replaceParametersInString(secretSpec.SecretRef.Name, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in name in k8s secret config %s: %w", secretSpec.Name, err))
	}

	secretSpec.SecretRef.Namespace, err = replaceParametersInString(secretSpec.SecretRef.Namespace, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in namespace in k8s secret config %s: %w", secretSpec.Name, err))
	}

	secretSpec.SecretRef.MountPath, err = replaceParametersInString(secretSpec.SecretRef.MountPath, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in mountPath in k8s secret config %s: %w", secretSpec.Name, err))
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newConfigItem := api.ConfigProviderSpec{}
	err = newConfigItem.FromKubernetesSecretProviderSpec(secretSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting git config: %w", err)}
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) replaceInlineConfigParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, []error) {
	inlineSpec, err := configItem.AsInlineConfigProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert config to inline config: %w", err)}
	}

	errs := []error{}

	for fileIndex, file := range inlineSpec.Inline {
		var decodedBytes []byte
		var err error

		inlineSpec.Inline[fileIndex].Path, err = replaceParametersInString(file.Path, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in path for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
		}

		if file.ContentEncoding == nil {
			decodedBytes = []byte(file.Content)
		} else {
			decodedBytes, err = base64.StdEncoding.DecodeString(file.Content)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed base64 decoding contents for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
				continue
			}
		}

		contentsReplaced, err := replaceParametersInString(string(decodedBytes), device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in contents for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
			continue
		}

		if file.ContentEncoding != nil && (*file.ContentEncoding) == api.Base64 {
			inlineSpec.Inline[fileIndex].Content = base64.StdEncoding.EncodeToString([]byte(contentsReplaced))
		} else {
			inlineSpec.Inline[fileIndex].Content = contentsReplaced
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newConfigItem := api.ConfigProviderSpec{}
	err = newConfigItem.FromInlineConfigProviderSpec(inlineSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting inline config: %w", err)}
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) replaceHTTPConfigParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, []error) {
	httpSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert config to git config: %w", err)}
	}

	errs := []error{}

	if httpSpec.HttpRef.Suffix != nil {
		suffix, err := replaceParametersInString(*httpSpec.HttpRef.Suffix, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in suffix in http config %s: %w", httpSpec.Name, err))
		}
		httpSpec.HttpRef.Suffix = &suffix
	}

	httpSpec.HttpRef.FilePath, err = replaceParametersInString(httpSpec.HttpRef.FilePath, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in file path in http config %s: %w", httpSpec.Name, err))
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newConfigItem := api.ConfigProviderSpec{}
	err = newConfigItem.FromHttpConfigProviderSpec(httpSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting git config: %w", err)}
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

func replaceParametersInString(s string, device *api.Device) (string, error) {
	t, err := template.New("t").Option("missingkey=error").Funcs(api.GetGoTemplateFuncMap()).Parse(s)
	if err != nil {
		return "", fmt.Errorf("invalid parameter syntax: %v", err)
	}

	output, err := api.ExecuteGoTemplateOnDevice(t, device)
	if err != nil {
		return "", fmt.Errorf("cannot apply parameters, possibly because they access invalid fields: %w", err)
	}

	return output, nil
}
