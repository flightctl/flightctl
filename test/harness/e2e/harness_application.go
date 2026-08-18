package e2e

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// =============================================================================
// Constants
// =============================================================================

const (
	// QuadletUnitPath is the quadlet unit path on device (rootful)
	QuadletUnitPath = "/etc/containers/systemd"

	// VMGuestMemoryDefault is the guest RAM for e2e KubeVirt VM manifests.
	VMGuestMemoryDefault = "1024M"
)

// VMYAML builds a KubeVirt VirtualMachine manifest for e2e tests. cloudInitVolumeYAML
// is the cloudinitdisk volume entry (including list-item indentation under volumes)
func VMYAML(name, guestMemory, image, cloudInitVolumeYAML string) string {
	return vmYAMLManifest(name, guestMemory, image, 0, cloudInitVolumeYAML)
}

// VMFedoraNoCloudUserData returns cloud-init userData that enables password SSH for the fedora user.
func VMFedoraNoCloudUserData(password string) string {
	return fmt.Sprintf(`#cloud-config
ssh_pwauth: true
password: %s
chpasswd: { expire: False }`, password)
}

// VMYAMLWithCPU builds a KubeVirt VirtualMachine manifest. cpuCores <= 0 omits the cpu block.
func VMYAMLWithCPU(name, guestMemory, image string, cpuCores int, cloudInitUserData string) string {
	return vmYAMLManifest(name, guestMemory, image, cpuCores, VMCloudInitNoCloudVolume(cloudInitUserData))
}

func vmYAMLManifest(name, guestMemory, image string, cpuCores int, cloudInitVolumeYAML string) string {
	cpuBlock := ""
	if cpuCores > 0 {
		cpuBlock = fmt.Sprintf("        cpu:\n          cores: %d\n", cpuCores)
	}
	return fmt.Sprintf(`apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: %s
spec:
  running: true
  template:
    spec:
      domain:
%s        devices:
          disks:
          - disk:
              bus: virtio
            name: containerdisk
          - disk:
              bus: virtio
            name: cloudinitdisk
          interfaces:
          - masquerade: {}
            name: default
          rng: {}
        memory:
          guest: %s
        resources: {}
      networks:
      - name: default
        pod: {}
      volumes:
      - containerdisk:
          image: %s
        name: containerdisk
%s`, name, cpuBlock, guestMemory, image, cloudInitVolumeYAML)
}

// VMYAMLWithConfigDrive builds a VM manifest using cloudInitConfigDrive userDataBase64.
func VMYAMLWithConfigDrive(name, guestMemory, image, userDataBase64 string) string {
	return VMYAML(name, guestMemory, image, vmCloudInitConfigDriveVolume(userDataBase64))
}

// VMCloudInitNoCloudVolume returns the cloudinitdisk volume using cloudInitNoCloud.
func VMCloudInitNoCloudVolume(userData string) string {
	return fmt.Sprintf(`      - cloudInitNoCloud:
          userData: |-
%s
        name: cloudinitdisk`, vmIndentCloudInitUserData(userData, 12))
}

func vmCloudInitConfigDriveVolume(userDataBase64 string) string {
	return fmt.Sprintf(`      - cloudInitConfigDrive:
          userDataBase64: %s
        name: cloudinitdisk`, userDataBase64)
}

func vmIndentCloudInitUserData(userData string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimSuffix(userData, "\n"), "\n")
	for i, line := range lines {
		lines[i] = prefix + line
	}
	return strings.Join(lines, "\n")
}

// QuadletPathForUser returns the quadlet systemd path for the given user.
// Empty or "root" returns the root path; any other user returns the user's config path.
func QuadletPathForUser(user string) string {
	if user == "" || user == "root" {
		return QuadletUnitPath
	}
	return fmt.Sprintf("/home/%s/.config/containers/systemd", user)
}

// =============================================================================
// Internal helpers (unexported)
// =============================================================================

func deviceHasApplicationWithStatus(device *v1beta1.Device, appName string, status v1beta1.ApplicationStatusType) bool {
	if device == nil || device.Status == nil {
		return false
	}
	for _, app := range device.Status.Applications {
		if app.Name == appName && app.Status == status {
			return true
		}
	}
	return false
}

func deviceSummaryMatchesAny(device *v1beta1.Device, statuses []v1beta1.ApplicationsSummaryStatusType) bool {
	if device == nil || device.Status == nil {
		return false
	}
	for _, s := range statuses {
		if device.Status.ApplicationsSummary.Status == s {
			return true
		}
	}
	return false
}

// =============================================================================
// Application spec builders
// =============================================================================

// InlineComposeVolume describes a volume for CreateFleetWithInlineComposeAndVolumes.
type InlineComposeVolume struct {
	Name       string
	Reference  string
	PullPolicy v1beta1.ImagePullPolicy
}

// CreateFleetWithInlineComposeAndVolumes creates or updates a fleet with a single inline compose application and optional image volumes.
func (h *Harness) CreateFleetWithInlineComposeAndVolumes(
	fleetName string,
	selectorValue string,
	appName string,
	composePath string,
	composeContent string,
	volumes []InlineComposeVolume,
) error {
	if h == nil {
		return fmt.Errorf("harness is nil")
	}
	if fleetName == "" || selectorValue == "" || appName == "" || composePath == "" || composeContent == "" {
		return fmt.Errorf("invalid fleet inputs: fleetName=%q selectorValue=%q appName=%q composePath=%q composeContentEmpty=%t",
			fleetName, selectorValue, appName, composePath, composeContent == "")
	}

	selector := v1beta1.LabelSelector{
		MatchLabels: &map[string]string{"fleet": selectorValue},
	}

	var vols []v1beta1.ApplicationVolume
	for _, v := range volumes {
		vol := v1beta1.ApplicationVolume{Name: v.Name}
		if err := vol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
			Image: v1beta1.ImageVolumeSource{
				Reference:  v.Reference,
				PullPolicy: lo.ToPtr(v.PullPolicy),
			},
		}); err != nil {
			return err
		}
		vols = append(vols, vol)
	}

	inline := v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Path:    composePath,
				Content: lo.ToPtr(composeContent),
			},
		},
	}

	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr(appName),
	}
	if len(vols) > 0 {
		compose.Volumes = &vols
	}
	if err := compose.FromInlineApplicationProviderSpec(inline); err != nil {
		return err
	}

	app := v1beta1.ApplicationProviderSpec{}
	if err := app.FromComposeApplication(compose); err != nil {
		return err
	}

	deviceSpec := v1beta1.DeviceSpec{
		Applications: &[]v1beta1.ApplicationProviderSpec{app},
	}

	return h.CreateOrUpdateTestFleet(fleetName, selector, deviceSpec)
}

// QuadletImageAppSpec builds an ApplicationProviderSpec for a quadlet app from an image and env vars.
func (h *Harness) QuadletImageAppSpec(name, image string, envVars map[string]string) (v1beta1.ApplicationProviderSpec, error) {
	quadletApp := v1beta1.QuadletApplication{
		Name:    &name,
		AppType: v1beta1.AppTypeQuadlet,
		EnvVars: &envVars,
	}
	if err := quadletApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromQuadletApplication(quadletApp); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

// NewContainerApplicationSpec creates a ContainerApplication spec with the given parameters (runs as root).
func NewContainerApplicationSpec(
	name string,
	image string,
	ports []v1beta1.ApplicationPort,
	cpu, memory *string,
	volumes *[]v1beta1.ApplicationVolume,
) (v1beta1.ApplicationProviderSpec, error) {
	return NewContainerApplicationSpecWithRunAs(name, image, ports, cpu, memory, volumes, "")
}

// NewContainerApplicationSpecWithRunAs creates a ContainerApplication spec with the given parameters and optional runAs user.
func NewContainerApplicationSpecWithRunAs(
	name string,
	image string,
	ports []v1beta1.ApplicationPort,
	cpu, memory *string,
	volumes *[]v1beta1.ApplicationVolume,
	runAs string,
) (v1beta1.ApplicationProviderSpec, error) {
	var resources *v1beta1.ApplicationResources
	if cpu != nil || memory != nil {
		resources = &v1beta1.ApplicationResources{
			Limits: &v1beta1.ApplicationResourceLimits{
				Cpu:    cpu,
				Memory: memory,
			},
		}
	}

	containerApp := v1beta1.ContainerApplication{
		Name:      lo.ToPtr(name),
		AppType:   v1beta1.AppTypeContainer,
		Resources: resources,
		Volumes:   volumes,
	}
	if err := containerApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	// Only set Ports when non-nil. A non-nil pointer to a nil slice marshals as
	// "ports": null, which OpenAPI validation rejects.
	if ports != nil {
		containerApp.Ports = &ports
	}
	if runAs != "" {
		containerApp.RunAs = v1beta1.Username(runAs)
	}

	var appSpec v1beta1.ApplicationProviderSpec
	err := appSpec.FromContainerApplication(containerApp)
	return appSpec, err
}

// NewMountVolume creates a named volume mount for container apps
func NewMountVolume(name, mountPath string) (v1beta1.ApplicationVolume, error) {
	var volume v1beta1.ApplicationVolume
	volume.Name = name

	mountVolumeProvider := v1beta1.MountVolumeProviderSpec{
		Mount: v1beta1.VolumeMount{
			Path: mountPath,
		},
	}

	err := volume.FromMountVolumeProviderSpec(mountVolumeProvider)
	return volume, err
}

// NewVmApplicationSpec creates an inline VmApplication spec containing a KubeVirt
// VirtualMachine manifest (vm.yaml). The port mapping is expressed via
// publishPorts so that the server-side renderer can inject it into the
// generated .pod unit.
func NewVmApplicationSpec(name, image string) (v1beta1.ApplicationProviderSpec, error) {
	vmYAML := VMYAML(name, VMGuestMemoryDefault, image, VMCloudInitNoCloudVolume(VMFedoraNoCloudUserData("fedora")))
	return NewVmApplicationSpecFromYAML(name, []string{"2222:22"}, vmYAML)
}

// NewVmApplicationSpecFromYAML creates an inline VmApplication spec from a pre-built
// KubeVirt VirtualMachine manifest and publishPorts list.
func NewVmApplicationSpecFromYAML(name string, publishPorts []string, vmYAML string) (v1beta1.ApplicationProviderSpec, error) {
	vmApp := v1beta1.VmApplication{
		AppType:      v1beta1.AppTypeVm,
		Name:         lo.ToPtr(name),
		PublishPorts: &publishPorts,
	}
	if err := vmApp.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{Path: "vm.yaml", Content: lo.ToPtr(vmYAML)},
		},
	}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("building inline VM application spec: %w", err)
	}
	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromVmApplication(vmApp); err != nil {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("converting VM application to provider spec: %w", err)
	}
	return appSpec, nil
}

// NewHelmApplicationSpec creates a HelmApplication spec with optional values files.
func NewHelmApplicationSpec(name, image, namespace string, valuesFiles []string) (v1beta1.ApplicationProviderSpec, error) {
	helmApp := v1beta1.HelmApplication{
		AppType:   v1beta1.AppTypeHelm,
		Name:      lo.ToPtr(name),
		Namespace: lo.ToPtr(namespace),
	}
	if err := helmApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	if len(valuesFiles) > 0 {
		helmApp.ValuesFiles = &valuesFiles
	}
	var appSpec v1beta1.ApplicationProviderSpec
	err := appSpec.FromHelmApplication(helmApp)
	return appSpec, err
}

// NewHelmApplicationSpecWithValues creates a HelmApplication spec with inline values.
func NewHelmApplicationSpecWithValues(name, image, namespace string, values map[string]any) (v1beta1.ApplicationProviderSpec, error) {
	helmApp := v1beta1.HelmApplication{
		AppType:   v1beta1.AppTypeHelm,
		Name:      lo.ToPtr(name),
		Namespace: lo.ToPtr(namespace),
		Values:    &values,
	}
	if err := helmApp.FromImageApplicationProviderSpec(v1beta1.ImageApplicationProviderSpec{Image: image}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var appSpec v1beta1.ApplicationProviderSpec
	err := appSpec.FromHelmApplication(helmApp)
	return appSpec, err
}

// NewQuadletApplicationSpec creates a QuadletApplication spec with image provider.
func NewQuadletApplicationSpec(name, image, runAs string, envVars map[string]string, volumes ...v1beta1.ApplicationVolume) (v1beta1.ApplicationProviderSpec, error) {
	imageSpec := v1beta1.ImageApplicationProviderSpec{
		Image: image,
	}
	quadletApp := v1beta1.QuadletApplication{
		AppType: v1beta1.AppTypeQuadlet,
		Name:    lo.ToPtr(name),
		RunAs:   v1beta1.Username(runAs),
	}
	if len(envVars) > 0 {
		quadletApp.EnvVars = &envVars
	}
	if len(volumes) > 0 {
		quadletApp.Volumes = &volumes
	}
	if err := quadletApp.FromImageApplicationProviderSpec(imageSpec); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var appSpec v1beta1.ApplicationProviderSpec
	err := appSpec.FromQuadletApplication(quadletApp)
	return appSpec, err
}

// NewInlineConfigSpec creates an InlineConfigProviderSpec with the given files.
func NewInlineConfigSpec(name string, files []v1beta1.FileSpec) (v1beta1.ConfigProviderSpec, error) {
	inlineConfig := v1beta1.InlineConfigProviderSpec{
		Name:   name,
		Inline: files,
	}
	var configSpec v1beta1.ConfigProviderSpec
	err := configSpec.FromInlineConfigProviderSpec(inlineConfig)
	return configSpec, err
}

// BuildComposeWithImageVolumeSpec builds a compose ApplicationProviderSpec with inline compose content
// and a single image-backed application volume.
func BuildComposeWithImageVolumeSpec(appName, composePath, composeContent, volumeName, imageRef string) (v1beta1.ApplicationProviderSpec, error) {
	volume := v1beta1.ApplicationVolume{
		Name:          volumeName,
		ReclaimPolicy: lo.ToPtr(v1beta1.Retain),
	}
	if err := volume.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{
			Reference:  imageRef,
			PullPolicy: lo.ToPtr(v1beta1.PullIfNotPresent),
		},
	}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}

	compose := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    lo.ToPtr(appName),
		Volumes: &[]v1beta1.ApplicationVolume{volume},
	}
	if err := compose.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{
		Inline: []v1beta1.ApplicationContent{
			{
				Path:    composePath,
				Content: lo.ToPtr(composeContent),
			},
		},
	}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}

	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromComposeApplication(compose); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

// buildInlineContent builds ApplicationContent slice from paths and contents.
func buildInlineContent(paths, contents []string) []v1beta1.ApplicationContent {
	inline := make([]v1beta1.ApplicationContent, len(paths))
	for i := range paths {
		c := ""
		if i < len(contents) {
			c = contents[i]
		}
		inline[i] = v1beta1.ApplicationContent{Path: paths[i], Content: &c}
	}
	return inline
}

// newQuadletSpecFromInline builds an ApplicationProviderSpec for a quadlet app from inline content.
func newQuadletSpecFromInline(name, runAs string, inline []v1beta1.ApplicationContent, envVars *map[string]string, volumes *[]v1beta1.ApplicationVolume) (v1beta1.ApplicationProviderSpec, error) {
	app := v1beta1.QuadletApplication{
		Name:    lo.ToPtr(name),
		AppType: v1beta1.AppTypeQuadlet,
		EnvVars: envVars,
		Volumes: volumes,
	}
	if runAs != "" {
		app.RunAs = v1beta1.Username(runAs)
	}
	if err := app.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromQuadletApplication(app); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

// NewQuadletInlineSpec builds an ApplicationProviderSpec for a quadlet app from inline paths and contents.
func NewQuadletInlineSpec(name, runAs string, paths, contents []string) (v1beta1.ApplicationProviderSpec, error) {
	return newQuadletSpecFromInline(name, runAs, buildInlineContent(paths, contents), nil, nil)
}

// NewQuadletInlineSpecWithEnvs builds an ApplicationProviderSpec for a quadlet app with inline content and env vars.
func NewQuadletInlineSpecWithEnvs(name, runAs string, envVars map[string]string, paths, contents []string) (v1beta1.ApplicationProviderSpec, error) {
	return newQuadletSpecFromInline(name, runAs, buildInlineContent(paths, contents), &envVars, nil)
}

// NewQuadletInlineSpecWithVolumes builds an ApplicationProviderSpec for a quadlet app with inline content and optional volumes.
func NewQuadletInlineSpecWithVolumes(name, runAs string, volumes *[]v1beta1.ApplicationVolume, paths, contents []string) (v1beta1.ApplicationProviderSpec, error) {
	return newQuadletSpecFromInline(name, runAs, buildInlineContent(paths, contents), nil, volumes)
}

// NewImageVolume returns an ApplicationVolume backed by an image.
func NewImageVolume(name, imageRef string) (v1beta1.ApplicationVolume, error) {
	var vol v1beta1.ApplicationVolume
	vol.Name = name
	err := vol.FromImageVolumeProviderSpec(v1beta1.ImageVolumeProviderSpec{
		Image: v1beta1.ImageVolumeSource{Reference: imageRef, PullPolicy: lo.ToPtr(v1beta1.PullIfNotPresent)},
	})
	return vol, err
}

// NewComposeInlineSpec builds an ApplicationProviderSpec for a compose app from a single inline file.
func NewComposeInlineSpec(name, path, content, runAs string) (v1beta1.ApplicationProviderSpec, error) {
	_ = runAs // reserved for when ComposeApplication supports RunAs
	inline := []v1beta1.ApplicationContent{{Path: path, Content: &content}}
	app := v1beta1.ComposeApplication{
		Name:    lo.ToPtr(name),
		AppType: v1beta1.AppTypeCompose,
	}
	if err := app.FromInlineApplicationProviderSpec(v1beta1.InlineApplicationProviderSpec{Inline: inline}); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	var spec v1beta1.ApplicationProviderSpec
	if err := spec.FromComposeApplication(app); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

// =============================================================================
// Application spec getters
// =============================================================================

// GetContainerApplicationImage returns the image reference from a ContainerApplication spec.
func GetContainerApplicationImage(spec v1beta1.ApplicationProviderSpec) (string, error) {
	app, err := spec.AsContainerApplication()
	if err != nil {
		return "", err
	}
	imageSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return "", fmt.Errorf("GetContainerApplicationImage: %w", err)
	}
	return imageSpec.Image, nil
}

// GetContainerApplicationVolumeImageRef returns the image reference of the named volume in a ContainerApplication spec.
func GetContainerApplicationVolumeImageRef(spec v1beta1.ApplicationProviderSpec, volumeName string) (string, error) {
	app, err := spec.AsContainerApplication()
	if err != nil {
		return "", err
	}
	if app.Volumes == nil {
		return "", fmt.Errorf("container app has nil volumes")
	}
	for _, vol := range *app.Volumes {
		if vol.Name == volumeName {
			imageMount, err := vol.AsImageMountVolumeProviderSpec()
			if err != nil {
				return "", err
			}
			return imageMount.Image.Reference, nil
		}
	}
	return "", fmt.Errorf("volume %q not found in container app", volumeName)
}

// GetQuadletApplicationInlineContent returns the first inline content string from a QuadletApplication (inline provider).
func GetQuadletApplicationInlineContent(spec v1beta1.ApplicationProviderSpec) (string, error) {
	quadletApp, err := spec.AsQuadletApplication()
	if err != nil {
		return "", err
	}
	inlineProvider, err := quadletApp.AsInlineApplicationProviderSpec()
	if err != nil {
		return "", err
	}
	if len(inlineProvider.Inline) == 0 {
		return "", fmt.Errorf("inline provider has no content")
	}
	content := inlineProvider.Inline[0].Content
	if content == nil {
		return "", fmt.Errorf("inline content is nil")
	}
	return *content, nil
}

// RenderedAppRefs holds extracted image references and inline content from a device's applications.
type RenderedAppRefs struct {
	ContainerImage  string // image from the container app
	ContainerVolRef string // first image-backed volume ref from the container app
	QuadletImage    string // image from the quadlet image app
	QuadletVolRef   string // first image-backed volume ref from the quadlet image app
	InlineContent   string // inline content from the inline quadlet app
}

// GetDeviceRenderedAppRefs extracts rendered application references from the device spec,
// matching apps by the given names. Pass empty string for any app name to skip extraction.
func (h *Harness) GetDeviceRenderedAppRefs(deviceId, containerAppName, quadletAppName, inlineAppName string) (*RenderedAppRefs, error) {
	device, err := h.GetDevice(deviceId)
	if err != nil {
		return nil, fmt.Errorf("failed to get device %s: %w", deviceId, err)
	}
	if device == nil || device.Spec == nil || device.Spec.Applications == nil {
		return nil, fmt.Errorf("device %s has nil spec or applications", deviceId)
	}

	result := &RenderedAppRefs{}
	for _, appSpec := range *device.Spec.Applications {
		name, nameErr := appSpec.GetName()
		if nameErr != nil {
			return nil, fmt.Errorf("GetName failed: %w", nameErr)
		}
		if name == nil || *name == "" {
			return nil, fmt.Errorf("application has nil or empty name")
		}

		switch *name {
		case containerAppName:
			if containerAppName == "" {
				continue
			}
			result.ContainerImage, err = GetContainerApplicationImage(appSpec)
			if err != nil {
				return nil, fmt.Errorf("GetContainerApplicationImage: %w", err)
			}
			containerApp, cErr := appSpec.AsContainerApplication()
			if cErr == nil && containerApp.Volumes != nil {
				for _, vol := range *containerApp.Volumes {
					if imageMount, mErr := vol.AsImageMountVolumeProviderSpec(); mErr == nil {
						result.ContainerVolRef = imageMount.Image.Reference
						break
					}
				}
			}
		case quadletAppName:
			if quadletAppName == "" {
				continue
			}
			quadletApp, qErr := appSpec.AsQuadletApplication()
			if qErr != nil {
				return nil, fmt.Errorf("AsQuadletApplication: %w", qErr)
			}
			imageProvider, iErr := quadletApp.AsImageApplicationProviderSpec()
			if iErr != nil {
				return nil, fmt.Errorf("AsImageApplicationProviderSpec: %w", iErr)
			}
			result.QuadletImage = imageProvider.Image
			if quadletApp.Volumes != nil {
				for _, vol := range *quadletApp.Volumes {
					if imageVol, vErr := vol.AsImageVolumeProviderSpec(); vErr == nil {
						result.QuadletVolRef = imageVol.Image.Reference
						break
					}
				}
			}
		case inlineAppName:
			if inlineAppName == "" {
				continue
			}
			result.InlineContent, err = GetQuadletApplicationInlineContent(appSpec)
			if err != nil {
				return nil, fmt.Errorf("GetQuadletApplicationInlineContent: %w", err)
			}
		}
	}

	GinkgoWriter.Printf("GetDeviceRenderedAppRefs: container=%s containerVol=%s quadlet=%s quadletVol=%s inlineLen=%d\n",
		result.ContainerImage, result.ContainerVolRef, result.QuadletImage, result.QuadletVolRef, len(result.InlineContent))
	return result, nil
}

// =============================================================================
// Device update functions
// =============================================================================

// ClearDeviceApplications is a device update callback that removes all applications from the device spec.
func ClearDeviceApplications(device *v1beta1.Device) {
	device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{}
}

// SetDeviceApplications sets device.Spec.Applications and waits for the new rendered version.
func (h *Harness) SetDeviceApplications(deviceID string, apps *[]v1beta1.ApplicationProviderSpec) error {
	return h.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
		device.Spec.Applications = apps
	})
}

// UpdateDeviceWithQuadletInline updates the device with an inline quadlet application (no env vars).
func (h *Harness) UpdateDeviceWithQuadletInline(deviceID, appName string, paths, contents []string) error {
	return h.UpdateDeviceWithQuadletInlineAndRunAs(deviceID, appName, "", paths, contents)
}

// UpdateDeviceWithQuadletInlineAndRunAs updates the device with an inline quadlet application and optional runAs user.
func (h *Harness) UpdateDeviceWithQuadletInlineAndRunAs(deviceID, appName, runAs string, paths, contents []string) error {
	if len(paths) == 0 && len(contents) == 0 {
		return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{})
	}
	spec, err := NewQuadletInlineSpec(appName, runAs, paths, contents)
	if err != nil {
		return err
	}
	return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{spec})
}

// UpdateDeviceWithQuadletInlineAndEnvs updates the device with an inline quadlet application and env vars.
func (h *Harness) UpdateDeviceWithQuadletInlineAndEnvs(deviceID, appName string, envVars map[string]string, paths, contents []string) error {
	return h.UpdateDeviceWithQuadletInlineAndEnvsAndRunAs(deviceID, appName, "", envVars, paths, contents)
}

// UpdateDeviceWithQuadletInlineAndEnvsAndRunAs updates the device with an inline quadlet application, env vars, and optional runAs user.
func (h *Harness) UpdateDeviceWithQuadletInlineAndEnvsAndRunAs(deviceID, appName, runAs string, envVars map[string]string, paths, contents []string) error {
	spec, err := NewQuadletInlineSpecWithEnvs(appName, runAs, envVars, paths, contents)
	if err != nil {
		return err
	}
	return h.SetDeviceApplications(deviceID, &[]v1beta1.ApplicationProviderSpec{spec})
}

func (h *Harness) UpdateApplication(withRetries bool, deviceId string, appName string, appProvider any, envVars map[string]string) error {
	logrus.Infof("UpdateApplication called with deviceId=%s, appName=%s, withRetries=%v", deviceId, appName, withRetries)

	composeApp := v1beta1.ComposeApplication{
		AppType: v1beta1.AppTypeCompose,
		Name:    &appName,
	}

	if envVars != nil {
		logrus.Infof("Setting environment variables for app %s: %v", appName, envVars)
		composeApp.EnvVars = &envVars
	}

	switch spec := appProvider.(type) {
	case v1beta1.InlineApplicationProviderSpec:
		logrus.Infof("Processing InlineApplicationProviderSpec for %s", appName)
		if err := composeApp.FromInlineApplicationProviderSpec(spec); err != nil {
			return fmt.Errorf("converting InlineApplicationProviderSpec: %w", err)
		}
	case v1beta1.ImageApplicationProviderSpec:
		logrus.Infof("Processing ImageApplicationProviderSpec for %s", appName)
		if err := composeApp.FromImageApplicationProviderSpec(spec); err != nil {
			return fmt.Errorf("converting ImageApplicationProviderSpec: %w", err)
		}
	default:
		return fmt.Errorf("unsupported application provider type: %T for %s", appProvider, appName)
	}

	var appSpec v1beta1.ApplicationProviderSpec
	if err := appSpec.FromComposeApplication(composeApp); err != nil {
		return fmt.Errorf("creating ApplicationProviderSpec: %w", err)
	}

	updateFunc := func(device *v1beta1.Device) {
		logrus.Infof("Starting update for device: %s", *device.Metadata.Name)

		if device.Spec.Applications == nil {
			logrus.Infof("device.Spec.Applications is nil, initializing with app %s", appName)
			device.Spec.Applications = &[]v1beta1.ApplicationProviderSpec{appSpec}
			return
		}

		for i, a := range *device.Spec.Applications {
			existingName, _ := a.GetName()
			if existingName != nil && *existingName == appName {
				logrus.Infof("Updating existing application %s at index %d", appName, i)
				(*device.Spec.Applications)[i] = appSpec
				return
			}
		}

		logrus.Infof("Appending new application %s to device %s", appName, *device.Metadata.Name)
		*device.Spec.Applications = append(*device.Spec.Applications, appSpec)
	}

	if withRetries {
		logrus.Info("Updating device with retries...")
		return h.UpdateDeviceWithRetries(deviceId, updateFunc)
	}

	logrus.Info("Updating device without retries...")
	return h.UpdateDevice(deviceId, updateFunc)
}

// =============================================================================
// Wait/polling functions
// =============================================================================

// WaitForApplicationRunningStatus waits for a specific application on a device to reach the "Running" status with all
// expected workloads running within a timeout.
func (h *Harness) WaitForApplicationRunningStatus(deviceId string, applicationImage string) {
	h.WaitForApplicationStatusByName(deviceId, applicationImage, v1beta1.ApplicationStatusRunning)
}

// waitForApplicationsSummaryCondition is an internal helper that waits for the ApplicationsSummary status
// to satisfy the given condition.
func (h *Harness) waitForApplicationsSummaryCondition(deviceID string, description string, condition func(v1beta1.ApplicationsSummaryStatusType) bool) {
	GinkgoWriter.Printf("Waiting for applications summary: %s (device=%s)\n", description, deviceID)
	Eventually(func() bool {
		response, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil || response == nil || response.JSON200 == nil {
			return false
		}
		device := response.JSON200
		if device.Status == nil {
			return false
		}
		if device.Status.ApplicationsSummary.Status == "" {
			return false
		}
		return condition(device.Status.ApplicationsSummary.Status)
	}, TIMEOUT, POLLING).Should(BeTrue())
}

// WaitForApplicationStatusByName waits for an application to reach the specified status.
func (h *Harness) WaitForApplicationStatusByName(deviceId string, applicationName string, expectedStatus v1beta1.ApplicationStatusType) {
	GinkgoWriter.Printf("Waiting for application %s to reach %s status\n", applicationName, expectedStatus)
	h.WaitForDeviceContents(deviceId, fmt.Sprintf("Application %s", expectedStatus),
		func(device *v1beta1.Device) bool {
			if device == nil || device.Status == nil || device.Status.Applications == nil {
				return false
			}
			for _, application := range device.Status.Applications {
				if application.Name == applicationName && application.Status == expectedStatus {
					return true
				}
			}
			return false
		}, TIMEOUT)
}

// WaitForRunningApplicationsCount waits for a specific number of applications to reach Running status
func (h *Harness) WaitForRunningApplicationsCount(deviceId string, expectedCount int) {
	GinkgoWriter.Printf("Waiting for %d applications to reach Running status\n", expectedCount)
	Eventually(func() int {
		response, err := h.GetDeviceWithStatusSystem(deviceId)
		if err != nil {
			return -1
		}
		if response.JSON200 == nil || response.JSON200.Status == nil {
			return -1
		}
		runningCount := 0
		for _, app := range response.JSON200.Status.Applications {
			if app.Status == v1beta1.ApplicationStatusRunning {
				runningCount++
			}
		}
		return runningCount
	}, TIMEOUT, POLLING).Should(Equal(expectedCount))
}

// WaitForApplicationsSummaryStatus waits for the device's ApplicationsSummary status to match the expected status
func (h *Harness) WaitForApplicationsSummaryStatus(deviceId string, expectedStatus v1beta1.ApplicationsSummaryStatusType) {
	h.waitForApplicationsSummaryCondition(deviceId, string(expectedStatus), func(status v1beta1.ApplicationsSummaryStatusType) bool {
		return status == expectedStatus
	})
}

// WaitForApplicationStatus polls until the device reports the given application with the given status, or timeout.
func (h *Harness) WaitForApplicationStatus(deviceID, appName string, status v1beta1.ApplicationStatusType, timeout, polling time.Duration) error {
	GinkgoWriter.Printf("Waiting for application %s to reach %s on device %s (timeout=%s, polling=%s)\n",
		appName, status, deviceID, timeout, polling)

	formatStatus := func(device *v1beta1.Device) string {
		if device == nil || device.Status == nil {
			return "<no device status>"
		}
		summary := string(device.Status.ApplicationsSummary.Status)
		if device.Status.ApplicationsSummary.Info != nil && *device.Status.ApplicationsSummary.Info != "" {
			summary = fmt.Sprintf("%s info=%q", summary, *device.Status.ApplicationsSummary.Info)
		}
		for _, app := range device.Status.Applications {
			if app.Name == appName {
				return fmt.Sprintf("status=%s ready=%s applicationsSummary=%s", app.Status, app.Ready, summary)
			}
		}
		return fmt.Sprintf("app not in status.applications (applicationsSummary=%s)", summary)
	}

	deadline := time.Now().Add(timeout)
	var lastStatus string
	loggedInitial := false

	for time.Now().Before(deadline) {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil {
			logrus.Debugf("WaitForApplicationStatus GetDeviceWithStatusSystem: %v", err)
			time.Sleep(polling)
			continue
		}
		if resp != nil && resp.JSON200 != nil {
			current := formatStatus(resp.JSON200)
			switch {
			case !loggedInitial:
				GinkgoWriter.Printf("Application %s initial status: %s\n", appName, current)
				lastStatus = current
				loggedInitial = true
			case current != lastStatus:
				GinkgoWriter.Printf("Application %s status changed: %s\n", appName, current)
				lastStatus = current
			}
			if deviceHasApplicationWithStatus(resp.JSON200, appName, status) {
				GinkgoWriter.Printf("Application %s reached %s: %s\n", appName, status, lastStatus)
				return nil
			}
		}
		time.Sleep(polling)
	}
	if loggedInitial {
		return fmt.Errorf("timed out after %s waiting for application %s to have status %s (last observed: %s)",
			timeout, appName, status, lastStatus)
	}
	return fmt.Errorf("timed out after %s waiting for application %s to have status %s", timeout, appName, status)
}

// WaitForApplicationSummary polls until the device's applications summary matches any of the expected statuses, or timeout.
func (h *Harness) WaitForApplicationSummary(deviceID string, timeout, polling time.Duration, expectedStatuses ...v1beta1.ApplicationsSummaryStatusType) error {
	if len(expectedStatuses) == 0 {
		return fmt.Errorf("at least one expected status must be provided")
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		resp, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil {
			logrus.Debugf("WaitForApplicationSummary GetDeviceWithStatusSystem: %v", err)
			time.Sleep(polling)
			continue
		}
		if resp != nil && resp.JSON200 != nil && deviceSummaryMatchesAny(resp.JSON200, expectedStatuses) {
			return nil
		}
		time.Sleep(polling)
	}
	return fmt.Errorf("timed out after %s waiting for applications summary to be one of %v", timeout, expectedStatuses)
}

// WaitForApplicationsSummaryNotHealthy waits until applications summary status is set and not Healthy.
func (h *Harness) WaitForApplicationsSummaryNotHealthy(deviceID string) {
	GinkgoWriter.Printf("Waiting for applications summary: not healthy (device=%s)\n", deviceID)
	Eventually(func() bool {
		response, err := h.GetDeviceWithStatusSystem(deviceID)
		if err != nil || response == nil || response.JSON200 == nil {
			return false
		}
		device := response.JSON200
		if device.Status == nil {
			return false
		}
		if device.Status.ApplicationsSummary.Status == "" {
			return false
		}
		return device.Status.ApplicationsSummary.Status != v1beta1.ApplicationsSummaryStatusHealthy
	}, TIMEOUT, POLLING).Should(BeTrue())
}

// WaitForApplicationsCount waits for the number of applications to match the expected count.
func (h *Harness) WaitForApplicationsCount(deviceId string, expectedCount int, statuses ...v1beta1.ApplicationStatusType) {
	description := fmt.Sprintf("%d total applications", expectedCount)
	if len(statuses) > 0 {
		description = fmt.Sprintf("%d applications with status in %v", expectedCount, statuses)
	}
	GinkgoWriter.Printf("Waiting for %s\n", description)
	Eventually(func() int {
		response, err := h.GetDeviceWithStatusSystem(deviceId)
		if err != nil {
			return -1
		}
		if response.JSON200 == nil || response.JSON200.Status == nil {
			return -1
		}
		if len(statuses) == 0 {
			return len(response.JSON200.Status.Applications)
		}
		count := 0
		for _, app := range response.JSON200.Status.Applications {
			for _, s := range statuses {
				if app.Status == s {
					count++
					break
				}
			}
		}
		return count
	}, TIMEOUT, POLLING).Should(Equal(expectedCount))
}

// WaitForNoApplications waits for the device to have no applications in status
func (h *Harness) WaitForNoApplications(deviceId string) {
	h.WaitForApplicationsCount(deviceId, 0)
}

// WaitForApplicationReadyCount waits until an application reports the expected ready/total pod count.
func (h *Harness) WaitForApplicationReadyCount(deviceId, appName, expectedReady string, expectedSummary v1beta1.ApplicationsSummaryStatusType) {
	GinkgoWriter.Printf("Waiting for application %s to report %s ready pods with %s summary\n", appName, expectedReady, expectedSummary)
	h.WaitForDeviceContents(deviceId, fmt.Sprintf("app %s ready=%s summary=%s", appName, expectedReady, expectedSummary),
		func(device *v1beta1.Device) bool {
			if device.Status == nil {
				return false
			}
			if device.Status.ApplicationsSummary.Status != expectedSummary {
				return false
			}
			for _, app := range device.Status.Applications {
				if app.Name == appName && app.Ready == expectedReady {
					return true
				}
			}
			return false
		}, TIMEOUT)
}

// =============================================================================
// Verification functions
// =============================================================================

// VerifyContainerRunning checks that podman shows containers running with the given image substring
func (h *Harness) VerifyContainerRunning(imageSubstring string) {
	Eventually(func() error {
		stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Image}}"}, nil)
		if err != nil {
			return err
		}
		if !strings.Contains(stdout.String(), imageSubstring) {
			return fmt.Errorf("container with image %q not found in running containers: %s", imageSubstring, stdout.String())
		}
		return nil
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderExists checks that the application folder exists
func (h *Harness) VerifyQuadletApplicationFolderExists(appName string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"test", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderExistsAt checks that the application folder exists at the given base path.
func (h *Harness) VerifyQuadletApplicationFolderExistsAt(appName, basePath string) {
	appPath := fmt.Sprintf("%s/%s", basePath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"sudo", "test", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderDeleted checks that the application folder was removed
func (h *Harness) VerifyQuadletApplicationFolderDeleted(appName string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"test", "!", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// VerifyQuadletApplicationFolderDeletedAt checks that the application folder does not exist at the given base path.
func (h *Harness) VerifyQuadletApplicationFolderDeletedAt(appName, basePath string) {
	appPath := fmt.Sprintf("%s/%s", basePath, appName)
	Eventually(func() error {
		_, err := h.VM.RunSSH([]string{"sudo", "test", "!", "-d", appPath}, nil)
		return err
	}, TIMEOUT, POLLING).Should(Succeed())
}

// PathExistsOnDevice checks that the given path exists on the device.
func (h *Harness) PathExistsOnDevice(path string) error {
	_, err := h.VM.RunSSH([]string{"sudo", "test", "-e", path}, nil)
	return err
}

// PathDoesNotExistOnDevice checks that the given path does not exist on the device.
func (h *Harness) PathDoesNotExistOnDevice(path string) error {
	_, err := h.VM.RunSSH([]string{"sudo", "test", "!", "-e", path}, nil)
	return err
}

// VerifyQuadletPodmanArgs verifies that a quadlet file contains the expected PodmanArgs entry
func (h *Harness) VerifyQuadletPodmanArgs(appName, flag, expectedValue string) {
	appPath := fmt.Sprintf("%s/%s", QuadletUnitPath, appName)
	expectedArg := fmt.Sprintf("PodmanArgs=%s %s", flag, expectedValue)

	GinkgoWriter.Printf("Verifying quadlet file contains %q for app %s\n", expectedArg, appName)

	Eventually(func() bool {
		findStdout, err := h.VM.RunSSH([]string{"sudo", "find", appPath, "-name", "*.container", "-type", "f"}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error finding container file: %v\n", err)
			return false
		}

		containerFile := strings.TrimSpace(findStdout.String())
		if containerFile == "" {
			GinkgoWriter.Printf("No .container file found in %s\n", appPath)
			return false
		}

		files := strings.Split(containerFile, "\n")
		containerFile = strings.TrimSpace(files[0])

		GinkgoWriter.Printf("Reading quadlet file: %s\n", containerFile)

		catStdout, err := h.VM.RunSSH([]string{"sudo", "cat", containerFile}, nil)
		if err != nil {
			GinkgoWriter.Printf("Error reading container file: %v\n", err)
			return false
		}

		contents := catStdout.String()
		if strings.Contains(contents, expectedArg) {
			GinkgoWriter.Printf("Found expected PodmanArgs in quadlet file\n")
			return true
		}

		GinkgoWriter.Printf("Expected %q not found in quadlet file contents:\n%s\n", expectedArg, contents)
		return false
	}, TIMEOUT, POLLING).Should(BeTrue(), "Expected quadlet file to contain %q", expectedArg)
}

// =============================================================================
// Podman/Container utilities
// =============================================================================

// RunPodmanPsContainerNames runs podman ps (or podman ps -a) on the VM as root and returns container names.
func (h *Harness) RunPodmanPsContainerNames(allContainers bool) (string, error) {
	return h.RunPodmanPsContainerNamesAsUser("root", allContainers)
}

// GetUserHomeOnVM returns the home directory of the given user on the VM.
func (h *Harness) GetUserHomeOnVM(user string) (string, error) {
	return h.getUserHomeOnVM(user)
}

// QuadletPathForUserOnVM returns the quadlet systemd path for the given user using the user's
// actual home on the VM (from getent).
func (h *Harness) QuadletPathForUserOnVM(user string) (string, error) {
	home, err := h.getUserHomeOnVM(user)
	if err != nil {
		return "", err
	}
	return home + "/.config/containers/systemd", nil
}

func (h *Harness) getUserHomeOnVM(user string) (string, error) {
	out, err := h.VM.RunSSH([]string{"sudo", "getent", "passwd", user}, nil)
	if err != nil {
		return "", fmt.Errorf("getent passwd %s: %w", user, err)
	}
	line := strings.TrimSpace(out.String())
	fields := strings.Split(line, ":")
	if len(fields) < 2 {
		return "", fmt.Errorf("getent passwd %s: unexpected output", user)
	}
	return fields[len(fields)-2], nil
}

// RunPodmanPsContainerNamesAsUser runs podman ps (or podman ps -a) on the VM as the given user and returns container names.
func (h *Harness) RunPodmanPsContainerNamesAsUser(user string, allContainers bool) (string, error) {
	var args []string
	if user == "root" {
		args = []string{"sudo", "-u", user, "podman", "ps", "--format", "{{.Names}}"}
		if allContainers {
			args = []string{"sudo", "-u", user, "podman", "ps", "-a", "--format", "{{.Names}}"}
		}
	} else {
		home, err := h.getUserHomeOnVM(user)
		if err != nil {
			return "", err
		}
		cmd := fmt.Sprintf("cd /tmp && env HOME=%q podman ps --format '{{.Names}}'", home)
		if allContainers {
			cmd = fmt.Sprintf("cd /tmp && env HOME=%q podman ps -a --format '{{.Names}}'", home)
		}
		args = []string{"sudo", "-u", user, "sh", "-c", cmd}
	}
	out, err := h.VM.RunSSH(args, nil)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// RunSystemctlUserStatus runs systemctl --user status for the given user on the VM.
func (h *Harness) RunSystemctlUserStatus(user, unitPattern string) (string, error) {
	cmd := fmt.Sprintf("sudo -u %s sh -c 'cd /tmp && XDG_RUNTIME_DIR=/run/user/$(id -u %s) systemctl --user status %s'", user, user, unitPattern)
	out, err := h.VM.RunSSH([]string{"sh", "-c", cmd}, nil)
	if err != nil {
		return "", err
	}
	return out.String(), nil
}

// GetContainerPorts returns the port mappings for running containers
func (h *Harness) GetContainerPorts() (string, error) {
	stdout, err := h.VM.RunSSH([]string{"sudo", "podman", "ps", "--format", "{{.Ports}}"}, nil)
	if err != nil {
		GinkgoWriter.Printf("Error getting container ports: %v\n", err)
		return "", err
	}
	return stdout.String(), nil
}

// =============================================================================
// VM operations
// =============================================================================

// RunSSHOnDeviceLocalPort runs ssh on the device host to localhost:port using password auth.
// This exercises VM publishPorts mappings (e.g. host 2222 to guest 22).
func (h *Harness) RunSSHOnDeviceLocalPort(port int, user, password string, remoteArgs ...string) (string, error) {
	if h.VM == nil {
		return "", fmt.Errorf("device VM is not configured")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}
	if user == "" {
		return "", fmt.Errorf("ssh user is required")
	}
	if password == "" {
		return "", fmt.Errorf("ssh password is required")
	}
	if len(remoteArgs) == 0 {
		return "", fmt.Errorf("remote command is required")
	}

	quotedRemoteArgs := make([]string, len(remoteArgs))
	for i, arg := range remoteArgs {
		quotedRemoteArgs[i] = shellQuote(arg)
	}
	quotedPassword := shellQuote(password)
	remoteCommand := strings.Join(quotedRemoteArgs, " ")
	sshCommand := fmt.Sprintf(
		`ssh -T -p %d -o ConnectTimeout=10 -o RequestTTY=no -o PubkeyAuthentication=no -o UserKnownHostsFile=/dev/null -o GlobalKnownHostsFile=/dev/null -o StrictHostKeyChecking=no -o LogLevel=ERROR %s %s`,
		port,
		shellQuote(user+"@127.0.0.1"),
		remoteCommand,
	)
	// Run via /bin/sh on the device. Inline the ssh command (not a shell function) because
	// sshpass/setsid execute a binary and cannot invoke shell functions.
	script := fmt.Sprintf(`set -eu
ssh_home=$(mktemp -d)
trap 'rm -rf "$ssh_home"' EXIT
export HOME="$ssh_home"
export PATH=/usr/local/bin:/usr/bin:/bin
if command -v sshpass >/dev/null 2>&1; then
  sshpass -p %s %s
else
  askpass="$ssh_home/askpass"
  {
    printf '%%s\n' '#!/bin/sh'
    printf '%%s\n' "printf '%%s\n' %s"
  } >"$askpass"
  chmod 700 "$askpass"
  SSH_ASKPASS="$askpass" SSH_ASKPASS_REQUIRE=force DISPLAY=:0 setsid %s
fi`,
		quotedPassword,
		sshCommand,
		quotedPassword,
		sshCommand,
	)
	out, err := h.VM.RunSSH([]string{"/bin/sh -c " + shellQuote(script)}, nil)
	if err != nil {
		return "", classifyDeviceLocalSSHError(fmt.Errorf(
			"running /bin/sh -c ssh script on device VM (localhost:%d user=%s remote=%s): %w",
			port, user, strings.Join(remoteArgs, " "), err,
		))
	}
	return trimSSHCommandOutput(out.String()), nil
}

var (
	ErrSSHConnectionRefused = errors.New("ssh connection refused")
	ErrSSHAuthFailed        = errors.New("ssh authentication failed")
	ErrSSHTimeout           = errors.New("ssh connection timed out")
)

func classifyDeviceLocalSSHError(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("%w: %s", ErrSSHConnectionRefused, err.Error())
	case strings.Contains(msg, "permission denied"), strings.Contains(msg, "authentication failed"):
		return fmt.Errorf("%w: %s", ErrSSHAuthFailed, err.Error())
	case strings.Contains(msg, "connection timed out"), strings.Contains(msg, "operation timed out"):
		return fmt.Errorf("%w: %s", ErrSSHTimeout, err.Error())
	default:
		return err
	}
}

// trimSSHCommandOutput returns the last non-empty line from nested SSH output. Device-side
// shell profiles can print environment noise before the guest command result on stdout.
func trimSSHCommandOutput(stdout string) string {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// UDPProbePythonCommand returns a remote shell command that sends a UDP datagram with
// payload "ping" to 127.0.0.1:port and prints the trimmed reply. Intended for nested
// SSH (device host -> guest published TCP port); uses a single-quoted python -c string.
func UDPProbePythonCommand(port int) string {
	return fmt.Sprintf(
		`python3 -c 'import socket; port=%d; s=socket.socket(socket.AF_INET, socket.SOCK_DGRAM); s.settimeout(2); s.sendto(b"ping", ("127.0.0.1", port)); print(s.recv(1024).decode().strip())'`,
		port,
	)
}

// udpProbeDeviceHostScript returns a shell script that probes localhost:port over UDP
// using socat when available, otherwise python3 via a heredoc (avoids fragile -c quoting).
func udpProbeDeviceHostScript(port int) string {
	return fmt.Sprintf(`set -eu
export PATH=/usr/local/bin:/usr/bin:/bin
port=%d
if command -v socat >/dev/null 2>&1; then
  printf 'ping\n' | socat -t2 - UDP4:127.0.0.1:${port}
elif command -v python3 >/dev/null 2>&1; then
  python3 - "$port" <<'PY'
import socket
import sys
port = int(sys.argv[1])
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
s.settimeout(2)
s.sendto(b"ping", ("127.0.0.1", port))
print(s.recv(1024).decode().strip())
PY
else
  echo "UDP probe requires socat or python3" >&2
  exit 127
fi`, port)
}

// lastNonEmptyLine returns the last non-empty line from command output.
func lastNonEmptyLine(stdout string) string {
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); line != "" {
			return line
		}
	}
	return ""
}

// RunUDPProbeOnDeviceLocalPort sends a UDP "ping" datagram to localhost:port on the device host
// and returns the response. This exercises VM publishPorts UDP mappings.
func (h *Harness) RunUDPProbeOnDeviceLocalPort(port int) (string, error) {
	if h.VM == nil {
		return "", fmt.Errorf("device VM is not configured")
	}
	if port <= 0 || port > 65535 {
		return "", fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}

	out, err := h.VM.RunSSH([]string{"/bin/sh -c " + shellQuote(udpProbeDeviceHostScript(port))}, nil)
	if err != nil {
		return "", fmt.Errorf("running UDP probe on device host localhost:%d: %w", port, err)
	}
	raw := out.String()
	reply := lastNonEmptyLine(raw)
	if reply == "" {
		return "", fmt.Errorf("UDP probe on localhost:%d returned empty output (raw stdout=%q)", port, raw)
	}
	return reply, nil
}

// RebootVMAndWaitForSSH triggers a reboot on the VM and waits for SSH to become ready again.
func (h *Harness) RebootVMAndWaitForSSH(waitInterval time.Duration, maxAttempts int) error {
	_, _ = h.VM.RunSSH([]string{"sudo", "reboot"}, nil)
	var sshErr error
	for attempt := range maxAttempts {
		_ = attempt
		time.Sleep(waitInterval)
		sshErr = h.VM.WaitForSSHToBeReady()
		if sshErr == nil {
			return nil
		}
	}
	return fmt.Errorf("SSH did not become ready after reboot within %d attempts: %w", maxAttempts, sshErr)
}
