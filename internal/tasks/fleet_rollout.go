package tasks

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/rollout"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// The fleet rollout task updates all devices in a fleet to match the latest template
// version.
//
// Behavior:
// - Iterates over devices that belong to the fleet.
// - Skips devices that:
//     - Have no owner
//     - Have multiple owners
//     - Are already being rolled out
// - For each eligible device:
//     - Compares the device spec and template version with the latest desired version.
//     - Updates the device spec and annotation only if necessary.
//
// Idempotency:
// - The task checks whether the device is already up to date.
// - No updates are made if the spec and version match.
// - Retries on conflict (409) to safely handle concurrent updates.
// - Skips devices not eligible for rollout, avoiding partial or duplicate writes.
//
// This design ensures the task can be run repeatedly without side effects.

func fleetRollout(ctx context.Context, orgId uuid.UUID, event api.Event, serviceHandler service.Service, log logrus.FieldLogger) error {
	logic := NewFleetRolloutsLogic(log, serviceHandler, orgId, event)
	switch event.InvolvedObject.Kind {
	case api.FleetKind:
		err := logic.RolloutFleet(ctx)
		if err != nil {
			log.Errorf("failed rolling out fleet %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		}
		return err
	case api.DeviceKind:
		err := logic.RolloutDevice(ctx)
		if err != nil {
			log.Errorf("failed rolling out device %s/%s: %v", orgId, event.InvolvedObject.Name, err)
		}
		return err
	default:
		return fmt.Errorf("FleetRollouts called with incorrect resource kind %s", event.InvolvedObject.Kind)
	}
}

type FleetRolloutsLogic struct {
	log            logrus.FieldLogger
	serviceHandler service.Service
	orgId          uuid.UUID
	event          api.Event
	itemsPerPage   int
	owner          string
}

func NewFleetRolloutsLogic(log logrus.FieldLogger, serviceHandler service.Service, orgId uuid.UUID, event api.Event) FleetRolloutsLogic {
	return FleetRolloutsLogic{
		log:            log,
		serviceHandler: serviceHandler,
		orgId:          orgId,
		event:          event,
		itemsPerPage:   ItemsPerPage,
	}
}

func (f *FleetRolloutsLogic) SetItemsPerPage(items int) {
	f.itemsPerPage = items
}

func (f FleetRolloutsLogic) RolloutFleet(ctx context.Context) error {
	fleet, status := f.serviceHandler.GetFleet(ctx, f.orgId, f.event.InvolvedObject.Name, api.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get fleet %s/%s: %s", f.orgId, f.event.InvolvedObject.Name, status.Message)
	}
	f.log.Infof("Rolling out fleet %s/%s", f.orgId, f.event.InvolvedObject.Name)

	templateVersion, status := f.serviceHandler.GetLatestTemplateVersion(ctx, f.orgId, f.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get templateVersion: %s", status.Message)
	}

	failureCount := 0
	owner := util.SetResourceOwner(api.FleetKind, f.event.InvolvedObject.Name)
	f.owner = *owner

	listParams := api.ListDevicesParams{
		Limit:         lo.ToPtr(int32(ItemsPerPage)),
		FieldSelector: lo.ToPtr(fmt.Sprintf("metadata.owner=%s", *owner)),
	}
	annotationFilter := []string{
		api.MatchExpression{
			Key:      api.DeviceAnnotationTemplateVersion,
			Operator: api.NotIn,
			Values:   &[]string{lo.FromPtr(templateVersion.Metadata.Name)},
		}.String(),
	}
	if fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DeviceSelection != nil {
		annotationFilter = append(annotationFilter, api.MatchExpression{
			Key:      api.DeviceAnnotationSelectedForRollout,
			Operator: api.Exists,
		}.String())
	}
	annotationSelector := selector.NewAnnotationSelectorOrDie(strings.Join(annotationFilter, ","))
	delayDeviceRender := fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DisruptionBudget != nil

	for {
		devices, status := f.serviceHandler.ListDevices(ctx, f.orgId, listParams, annotationSelector)
		if status.Code != http.StatusOK {
			// TODO: Retry when we have a mechanism that allows it
			return fmt.Errorf("failed fetching devices: %s", status.Message)
		}

		for devIndex := range devices.Items {
			device := &devices.Items[devIndex]
			err := f.updateDeviceToFleetTemplate(ctx, device, templateVersion, delayDeviceRender)
			if err != nil {
				f.log.Errorf("failed to update target generation for device %s (fleet %s): %v", *device.Metadata.Name, f.event.InvolvedObject.Name, err)
				failureCount++
			}
		}

		if devices.Metadata.Continue == nil {
			break
		}
		listParams.Continue = devices.Metadata.Continue
	}

	if failureCount != 0 {
		// TODO: Retry when we have a mechanism that allows it
		return fmt.Errorf("failed updating %d devices", failureCount)
	}

	return nil
}

// The device's owner was changed, roll out if necessary
func (f FleetRolloutsLogic) RolloutDevice(ctx context.Context) error {
	f.log.Infof("Rolling out device %s/%s", f.orgId, f.event.InvolvedObject.Name)

	device, status := f.serviceHandler.GetDevice(ctx, f.orgId, f.event.InvolvedObject.Name)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get device: %s", status.Message)
	}

	if device.Metadata.Owner == nil || len(*device.Metadata.Owner) == 0 {
		return nil
	}

	if api.IsStatusConditionTrue(device.Status.Conditions, api.ConditionTypeDeviceMultipleOwners) {
		f.log.Errorf("Device %s has multiple owners, skipping rollout", f.event.InvolvedObject.Name)
		return nil
	}

	ownerName, isFleetOwner, err := getOwnerFleet(device)
	if err != nil {
		return fmt.Errorf("failed getting device owner: %w", err)
	}
	if !isFleetOwner {
		return nil
	}
	f.owner = *device.Metadata.Owner

	templateVersion, status := f.serviceHandler.GetLatestTemplateVersion(ctx, f.orgId, ownerName)
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get templateVersion: %s", status.Message)
	}

	fleet, status := f.serviceHandler.GetFleet(ctx, f.orgId, ownerName, api.GetFleetParams{})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed to get fleet: %s", status.Message)
	}
	rolloutProgressStage, err := rollout.ProgressStage(fleet)
	if err != nil {
		return fmt.Errorf("failed to find rollout progress stage for fleet: %w", err)
	}
	if rolloutProgressStage == rollout.ConfiguredBatch {
		// If a rollout is in progress, then the device will be rolled out by one of the next batches
		f.log.Infof("Rollout is in progress for fleet %v/%s. Skipping device %s rollout", f.orgId, lo.FromPtr(fleet.Metadata.Name), f.event.InvolvedObject.Name)
		return nil
	}
	delayDeviceRender := fleet.Spec.RolloutPolicy != nil && fleet.Spec.RolloutPolicy.DisruptionBudget != nil
	return f.updateDeviceToFleetTemplate(ctx, device, templateVersion, delayDeviceRender)
}

func (f FleetRolloutsLogic) updateDeviceToFleetTemplate(ctx context.Context, device *api.Device, templateVersion *api.TemplateVersion, delayDeviceRender bool) error {
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
		annotations := map[string]string{
			api.DeviceAnnotationLastRolloutError: errors.Join(errs...).Error(),
		}
		status := f.serviceHandler.UpdateDeviceAnnotations(ctx, f.orgId, *device.Metadata.Name, annotations, nil)
		if status.Code != http.StatusOK {
			errs = append(errs, service.ApiStatusToErr(status))
		}
		return fmt.Errorf("failed generating device spec for %s/%s: %w", f.orgId, *device.Metadata.Name, errors.Join(errs...))
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
		return fmt.Errorf("failed validating device spec for %s/%s: %w", f.orgId, *device.Metadata.Name, errors.Join(errs...))
	}

	if currentVersion == *templateVersion.Metadata.Name && api.DeviceSpecsAreEqual(newDeviceSpec, *device.Spec) {
		f.log.Debugf("Not rolling out device %s/%s because it is already at templateVersion %s", f.orgId, *device.Metadata.Name, *templateVersion.Metadata.Name)
		return nil
	}

	f.log.Infof("Rolling out device %s/%s to templateVersion %s", f.orgId, *device.Metadata.Name, *templateVersion.Metadata.Name)
	err := f.updateDeviceInStore(ctx, device, &newDeviceSpec, delayDeviceRender)
	if err != nil {
		return fmt.Errorf("failed updating device spec: %w", err)
	}

	annotations := map[string]string{
		api.DeviceAnnotationTemplateVersion: *templateVersion.Metadata.Name,
	}
	status := f.serviceHandler.UpdateDeviceAnnotations(ctx, f.orgId, *device.Metadata.Name, annotations, []string{api.DeviceAnnotationLastRolloutError})
	if status.Code != http.StatusOK {
		return fmt.Errorf("failed updating templateVersion annotation: %s", status.Message)
	}

	return err
}

func (f FleetRolloutsLogic) getDeviceApps(device *api.Device, templateVersion *api.TemplateVersion) (*[]api.ApplicationProviderSpec, []error) {
	if templateVersion.Status.Applications == nil {
		return nil, nil
	}

	deviceApps := []api.ApplicationProviderSpec{}
	appErrs := []error{}
	for appIndex, appItem := range *templateVersion.Status.Applications {
		var newAppItem *api.ApplicationProviderSpec
		errs := []error{}
		appType, err := appItem.Type()
		if err != nil {
			appErrs = append(errs, fmt.Errorf("failed getting type for app %d: %w", appIndex, err))
			continue
		}
		switch appType {
		case api.ImageApplicationProviderType:
			newAppItem, errs = f.replaceImageApplicationParameters(device, appItem)
		case api.InlineApplicationProviderType:
			newAppItem, errs = f.replaceInlineApplicationParameters(device, appItem)
		default:
			errs = append(errs, fmt.Errorf("unsupported type for app %d: %s", appIndex, appType))
		}

		appErrs = append(appErrs, errs...)
		if newAppItem != nil {
			deviceApps = append(deviceApps, *newAppItem)
		}
	}

	if len(appErrs) > 0 {
		return nil, appErrs
	}

	return &deviceApps, nil
}

func replaceEnvVars(device *api.Device, app *api.ApplicationProviderSpec) []error {
	var errs []error
	if app.EnvVars != nil {
		origEnvVars := *app.EnvVars
		newEnvVars := make(map[string]string, len(origEnvVars))
		for k, v := range origEnvVars {
			newValue, err := replaceParametersInString(v, device)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed replacing parameters in env var %s: %w", k, err))
				continue
			}
			newEnvVars[k] = newValue
		}
		app.EnvVars = &newEnvVars
	}
	return errs
}

func (f FleetRolloutsLogic) replaceImageApplicationParameters(device *api.Device, app api.ApplicationProviderSpec) (*api.ApplicationProviderSpec, []error) {
	appName := lo.FromPtr(app.Name)
	imageSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to image application provider: %w", err)}
	}

	var errs []error

	imageSpec.Image, err = replaceParametersInString(imageSpec.Image, device)
	if err != nil {
		errs = append(errs, fmt.Errorf("failed replacing parameters in image for app %s: %w", appName, err))
	}

	errs = append(errs, replaceEnvVars(device, &app)...)

	if imageSpec.Volumes != nil {
		newVolumes, volErrs := f.replaceVolumeParameters(device, appName, *imageSpec.Volumes)
		errs = append(errs, volErrs...)
		if len(volErrs) == 0 {
			imageSpec.Volumes = &newVolumes
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newItem := api.ApplicationProviderSpec{
		Name:    app.Name,
		EnvVars: app.EnvVars,
		AppType: app.AppType,
	}
	err = newItem.FromImageApplicationProviderSpec(imageSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting image application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceVolumeParameters(device *api.Device, appName string, volumes []api.ApplicationVolume) ([]api.ApplicationVolume, []error) {
	var errs []error
	newVolumes := make([]api.ApplicationVolume, 0, len(volumes))

	for volIndex, vol := range volumes {
		volType, err := vol.Type()
		if err != nil {
			errs = append(errs, fmt.Errorf("failed getting volume type for volume %d in app %s: %w", volIndex, appName, err))
			continue
		}

		newVol := api.ApplicationVolume{
			Name:          vol.Name,
			ReclaimPolicy: vol.ReclaimPolicy,
		}

		switch volType {
		case api.ImageApplicationVolumeProviderType:
			imgSpec, err := vol.AsImageVolumeProviderSpec()
			if err != nil {
				errs = append(errs, fmt.Errorf("failed getting image volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			imgSpec.Image.Reference, err = replaceParametersInString(imgSpec.Image.Reference, device)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed replacing parameters in image reference for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			if err := newVol.FromImageVolumeProviderSpec(imgSpec); err != nil {
				errs = append(errs, fmt.Errorf("failed converting image volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

		case api.ImageMountApplicationVolumeProviderType:
			imgMountSpec, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				errs = append(errs, fmt.Errorf("failed getting image mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			imgMountSpec.Image.Reference, err = replaceParametersInString(imgMountSpec.Image.Reference, device)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed replacing parameters in image reference for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			if err := newVol.FromImageMountVolumeProviderSpec(imgMountSpec); err != nil {
				errs = append(errs, fmt.Errorf("failed converting image mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

		case api.MountApplicationVolumeProviderType:
			mountSpec, err := vol.AsMountVolumeProviderSpec()
			if err != nil {
				errs = append(errs, fmt.Errorf("failed getting mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

			if err := newVol.FromMountVolumeProviderSpec(mountSpec); err != nil {
				errs = append(errs, fmt.Errorf("failed converting mount volume spec for volume %d in app %s: %w", volIndex, appName, err))
				continue
			}

		default:
			errs = append(errs, fmt.Errorf("unsupported volume type %s for volume %d in app %s", volType, volIndex, appName))
			continue
		}

		newVolumes = append(newVolumes, newVol)
	}

	return newVolumes, errs
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
		return nil, []error{fmt.Errorf("failed to convert config to kubernetes secret config: %w", err)}
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

		encoding := lo.FromPtr(file.ContentEncoding)
		if encoding == api.EncodingBase64 {
			decodedBytes, err = base64.StdEncoding.DecodeString(file.Content)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed base64 decoding contents for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
				continue
			}
		} else {
			decodedBytes = []byte(file.Content)
		}

		contentsReplaced, err := replaceParametersInString(string(decodedBytes), device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in contents for file %d in inline config %s: %w", fileIndex, inlineSpec.Name, err))
			continue
		}

		if encoding == api.EncodingBase64 {
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

func (f FleetRolloutsLogic) replaceInlineApplicationParameters(device *api.Device, item api.ApplicationProviderSpec) (*api.ApplicationProviderSpec, []error) {
	appName := lo.FromPtr(item.Name)
	inlineSpec, err := item.AsInlineApplicationProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert to inline application provider: %w", err)}
	}

	errs := []error{}
	for fileIndex, file := range inlineSpec.Inline {
		var decodedBytes []byte
		var err error

		inlineSpec.Inline[fileIndex].Path, err = replaceParametersInString(file.Path, device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in path for file %d in inline app %s: %w", fileIndex, appName, err))
		}

		content := lo.FromPtr(file.Content)
		encoding := lo.FromPtr(file.ContentEncoding)
		if encoding == api.EncodingBase64 {
			decodedBytes, err = base64.StdEncoding.DecodeString(content)
			if err != nil {
				errs = append(errs, fmt.Errorf("failed base64 decoding contents for file %d in inline app %s: %w", fileIndex, appName, err))
				continue
			}
		} else {
			decodedBytes = []byte(content)
		}

		contentsReplaced, err := replaceParametersInString(string(decodedBytes), device)
		if err != nil {
			errs = append(errs, fmt.Errorf("failed replacing parameters in contents for file %d in inline app %s: %w", fileIndex, appName, err))
			continue
		}

		if encoding == api.EncodingBase64 {
			contentsReplaced = base64.StdEncoding.EncodeToString([]byte(contentsReplaced))
			inlineSpec.Inline[fileIndex].Content = &contentsReplaced
		} else {
			inlineSpec.Inline[fileIndex].Content = &contentsReplaced
		}
	}

	errs = append(errs, replaceEnvVars(device, &item)...)

	if inlineSpec.Volumes != nil {
		newVolumes, volErrs := f.replaceVolumeParameters(device, appName, *inlineSpec.Volumes)
		errs = append(errs, volErrs...)
		if len(volErrs) == 0 {
			inlineSpec.Volumes = &newVolumes
		}
	}

	if len(errs) > 0 {
		return nil, errs
	}

	newItem := api.ApplicationProviderSpec{
		Name:    &appName,
		EnvVars: item.EnvVars,
		AppType: item.AppType,
	}
	err = newItem.FromInlineApplicationProviderSpec(inlineSpec)
	if err != nil {
		return nil, []error{fmt.Errorf("failed converting inline application: %w", err)}
	}

	return &newItem, nil
}

func (f FleetRolloutsLogic) replaceHTTPConfigParameters(device *api.Device, configItem api.ConfigProviderSpec) (*api.ConfigProviderSpec, []error) {
	httpSpec, err := configItem.AsHttpConfigProviderSpec()
	if err != nil {
		return nil, []error{fmt.Errorf("failed to convert config to http config: %w", err)}
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
		return nil, []error{fmt.Errorf("failed converting http config: %w", err)}
	}

	return &newConfigItem, nil
}

func (f FleetRolloutsLogic) updateDeviceInStore(ctx context.Context, device *api.Device, newDeviceSpec *api.DeviceSpec, delayDeviceRender bool) error {
	var status api.Status
	for i := 0; i < 10; i++ {
		if device.Metadata.Owner == nil || *device.Metadata.Owner != f.owner {
			return fmt.Errorf("device owner changed, skipping rollout")
		}
		device.Spec = newDeviceSpec
		newCtx := context.WithValue(ctx, consts.DelayDeviceRenderCtxKey, delayDeviceRender)
		_, status = f.serviceHandler.ReplaceDevice(newCtx, f.orgId, *device.Metadata.Name, *device, nil)
		if status.Code != http.StatusOK {
			if status.Code == http.StatusConflict {
				device, status = f.serviceHandler.GetDevice(ctx, f.orgId, *device.Metadata.Name)
				if status.Code != http.StatusOK {
					return fmt.Errorf("the device changed before we could update it, and we failed to fetch it again: %s", status.Message)
				}
			} else {
				return service.ApiStatusToErr(status)
			}
		} else {
			break
		}
	}

	return service.ApiStatusToErr(status)
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
