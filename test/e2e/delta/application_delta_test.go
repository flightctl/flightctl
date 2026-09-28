package delta

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	applicationDeltaContainerName = "delta-container"
	applicationDeltaComposeName   = "delta-compose"
	applicationDeltaQuadletName   = "delta-quadlet"
	applicationDeltaHelmName      = "delta-helm"
	applicationDeltaVMName        = "delta-vm"
	applicationDeltaNamespace     = "delta-apps"
	applicationDeltaRepository    = "flightctl/delta-applications"
)

var applicationDeltaAppNames = []string{
	applicationDeltaContainerName,
	applicationDeltaComposeName,
	applicationDeltaQuadletName,
	applicationDeltaHelmName,
	applicationDeltaVMName,
}

var _ = Describe("application delta applications", Label("delta", "microshift", "slow", "vm"), Serial, func() {
	It("When a standalone device updates all application types it should apply application deltas and remain healthy", Label("standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device and preparing the MicroShift OS")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)
		requireDeltaGenerationSupport(harness, deviceID)

		By("installing all five V1 application types and waiting for health")
		registry := applicationRegistryEndpoint()
		v1Apps, err := applicationDeltaSpecs(registry, applicationDeltaVersionV1, applicationDeltaOverrides{})
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v1Apps
		})).To(Succeed())
		waitForApplicationDeltaApps(harness, deviceID)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with all V1 applications")

		By("capturing the reported-event baseline and building valid application delta artifacts")
		eventBaseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		before := getDeltaDevice(harness, deviceID)

		tagPrefix := applicationDeltaArtifactTag(harness)
		containerTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-container",
			"flightctl-tests/nginx",
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
			applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim"),
		)
		Expect(err).NotTo(HaveOccurred())
		composeTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-compose",
			"flightctl-tests/alpine",
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		quadletTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-quadlet",
			"flightctl-tests/nginx",
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		helmTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-helm",
			"flightctl-tests/alpine",
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
			applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim"),
		)
		Expect(err).NotTo(HaveOccurred())

		By("removing each test target tag so only its delta can supply the V2 image")
		for _, target := range []applicationDeltaTarget{containerTarget, composeTarget, quadletTarget} {
			requireDeviceImageAbsent(harness, target.image)
		}
		requireCRIImageAbsent(harness, helmTarget.image)
		vmV2Image := applicationDeltaVMImageV2
		removeDeviceImageIfPresent(harness, vmV2Image)

		v2Apps, err := applicationDeltaSpecs(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
			Container: &containerTarget,
			Compose:   &composeTarget,
			Quadlet:   &quadletTarget,
			Helm:      &helmTarget,
		})
		Expect(err).NotTo(HaveOccurred())
		By("updating all five application definitions in one device mutation")
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v2Apps
		})).To(Succeed())
		waitForRenderedContainerDeltaHint(harness, deviceID, containerTarget.image, containerTarget.deltaImage)
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, composeTarget)
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaQuadletName, v1beta1.AppTypeQuadlet, quadletTarget)
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaHelmName, v1beta1.AppTypeHelm, helmTarget)

		By("checking reported delta-generation events, application health, and each application delta result")
		waitForApplicationDeltaApps(harness, deviceID)
		vmTargetDigest, err := resolveApplicationImageDigest(harness, "quay.io", "containerdisks/fedora", "41")
		Expect(err).NotTo(HaveOccurred())
		waitForApplicationDeltaEvents(harness, "", deviceID, eventBaseline, applicationDeltaGenerationTarget{
			repository: "quay.io/containerdisks/fedora",
			digest:     vmTargetDigest,
		})
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with all V2 applications")
		for _, target := range []struct {
			name  string
			image string
		}{
			{applicationDeltaContainerName, containerTarget.image},
			{applicationDeltaComposeName, composeTarget.image},
			{applicationDeltaQuadletName, quadletTarget.image},
			{applicationDeltaHelmName, helmTarget.image},
			{applicationDeltaVMName, vmV2Image},
		} {
			waitForApplicationDeltaOutcome(harness, deviceID, target.name, target.image, false)
		}
		waitForRenderedVMDeltaHint(harness, deviceID, vmV2Image)
		after := getDeltaDevice(harness, deviceID)
		Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
	})

	It("When a fleet updates all application types it should apply application deltas and remain healthy", Label("fleet"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a device and preparing the MicroShift OS")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		fleetName := "delta-app-" + strings.TrimPrefix(applicationDeltaArtifactTag(harness), "e2e-")
		v1Apps, err := applicationDeltaSpecs(registry, applicationDeltaVersionV1, applicationDeltaOverrides{})
		Expect(err).NotTo(HaveOccurred())
		By("applying the V1 application set through fleet ownership")
		v1FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v1Apps)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v1FleetSpec)).To(Succeed())
		attachDeviceToApplicationDeltaFleet(harness, deviceID, fleetName)
		waitForApplicationDeltaApps(harness, deviceID)
		waitDeviceUpToDate(harness, deviceID, "fleet-owned device UpToDate with all V1 applications")

		eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceID)
		Expect(err).NotTo(HaveOccurred())
		before := getDeltaDevice(harness, deviceID)

		By("building valid application delta artifacts for the fleet-owned update")
		tagPrefix := applicationDeltaArtifactTag(harness) + "-fleet"
		containerTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-container",
			"flightctl-tests/nginx",
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
			applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim"),
		)
		Expect(err).NotTo(HaveOccurred())
		composeTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-compose",
			"flightctl-tests/alpine",
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		quadletTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-quadlet",
			"flightctl-tests/nginx",
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		helmTarget, err := buildApplicationDeltaTarget(
			harness,
			registry,
			tagPrefix+"-helm",
			"flightctl-tests/alpine",
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
			applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim"),
		)
		Expect(err).NotTo(HaveOccurred())
		requireDeviceImageAbsent(harness, containerTarget.image)
		requireDeviceImageAbsent(harness, composeTarget.image)
		requireDeviceImageAbsent(harness, quadletTarget.image)
		requireCRIImageAbsent(harness, helmTarget.image)
		vmV2Image := applicationDeltaVMImageV2
		removeDeviceImageIfPresent(harness, vmV2Image)
		v2Apps, err := applicationDeltaSpecs(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
			Container: &containerTarget,
			Compose:   &composeTarget,
			Quadlet:   &quadletTarget,
			Helm:      &helmTarget,
		})
		Expect(err).NotTo(HaveOccurred())
		v2FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v2Apps)

		nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v2FleetSpec)).To(Succeed())
		Expect(harness.WaitForDeviceNewRenderedVersion(deviceID, nextRenderedVersion)).To(Succeed())
		waitForRenderedContainerDeltaHint(harness, deviceID, containerTarget.image, containerTarget.deltaImage)
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, composeTarget)
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaQuadletName, v1beta1.AppTypeQuadlet, quadletTarget)
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaHelmName, v1beta1.AppTypeHelm, helmTarget)
		waitForRenderedVMDeltaHint(harness, deviceID, vmV2Image)

		By("checking reported fleet/device delta-generation events, application health, and successful delta results")
		waitForApplicationDeltaApps(harness, deviceID)
		vmTargetDigest, err := resolveApplicationImageDigest(harness, "quay.io", "containerdisks/fedora", "41")
		Expect(err).NotTo(HaveOccurred())
		waitForApplicationDeltaEvents(harness, fleetName, deviceID, eventBaseline, applicationDeltaGenerationTarget{
			repository: "quay.io/containerdisks/fedora",
			digest:     vmTargetDigest,
		})
		waitDeviceUpToDate(harness, deviceID, "fleet-owned device UpToDate with all V2 applications")
		for _, target := range []struct {
			name  string
			image string
		}{
			{applicationDeltaContainerName, containerTarget.image},
			{applicationDeltaComposeName, composeTarget.image},
			{applicationDeltaQuadletName, quadletTarget.image},
			{applicationDeltaHelmName, helmTarget.image},
			{applicationDeltaVMName, vmV2Image},
		} {
			waitForApplicationDeltaOutcome(harness, deviceID, target.name, target.image, false)
		}
		after := getDeltaDevice(harness, deviceID)
		Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
	})

	It("When a nested application delta artifact is unavailable it should full-pull and report the fallback", Label("fallback", "standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device and preparing the MicroShift OS")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)

		registry := applicationRegistryEndpoint()
		composeV1, err := applicationDeltaComposeSpec(applicationImageReference(registry, "flightctl-tests/alpine", "v1"), nil)
		Expect(err).NotTo(HaveOccurred())
		v1Apps := []v1beta1.ApplicationProviderSpec{composeV1}
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v1Apps
		})).To(Succeed())
		waitForApplicationNames(harness, deviceID, []string{applicationDeltaComposeName})
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 Compose application")

		eventBaseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		before := getDeltaDevice(harness, deviceID)

		By("preparing a full-pullable target and a missing nested delta artifact")
		tagPrefix := applicationDeltaArtifactTag(harness) + "-fallback"
		target, err := copyApplicationDeltaTarget(harness, registry, tagPrefix, applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim"))
		Expect(err).NotTo(HaveOccurred())
		target.deltaImage = missingApplicationDeltaReference(registry, tagPrefix)
		requireDeviceImageAbsent(harness, target.image)
		composeV2, err := applicationDeltaComposeSpec(target.image, &target)
		Expect(err).NotTo(HaveOccurred())
		v2Apps := []v1beta1.ApplicationProviderSpec{composeV2}

		By("updating Compose and checking the reported fallback after the full pull")
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v2Apps
		})).To(Succeed())
		waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, target)
		waitForApplicationNames(harness, deviceID, []string{applicationDeltaComposeName})
		waitForApplicationDeltaEvents(harness, "", deviceID, eventBaseline, applicationDeltaGenerationTarget{})
		waitDeviceUpToDate(harness, deviceID, "device UpToDate after Compose delta fallback")
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaComposeName, target.image, true)
		after := getDeltaDevice(harness, deviceID)
		Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application fallback must not change OS delta status")
	})
})

type applicationDeltaVersion int

const (
	applicationDeltaVersionV1 applicationDeltaVersion = iota
	applicationDeltaVersionV2
)

type applicationDeltaTarget struct {
	image        string
	targetDigest string
	deltaImage   string
}

type applicationDeltaOverrides struct {
	Container *applicationDeltaTarget
	Compose   *applicationDeltaTarget
	Quadlet   *applicationDeltaTarget
	Helm      *applicationDeltaTarget
}

type applicationDeltaGenerationTarget struct {
	repository string
	digest     string
}

const (
	applicationDeltaVMImageV2 = "quay.io/containerdisks/fedora:41"
)

func applicationDeltaSpecs(registry string, version applicationDeltaVersion, overrides applicationDeltaOverrides) ([]v1beta1.ApplicationProviderSpec, error) {
	var containerImage, composeImage, quadletImage, chartVersion, helmImage, vmImage string
	switch version {
	case applicationDeltaVersionV1:
		containerImage = applicationImageReference(registry, "flightctl-tests/nginx", "v1")
		composeImage = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		quadletImage = applicationImageReference(registry, "flightctl-tests/nginx", "v1")
		chartVersion = "0.1.0"
		helmImage = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		vmImage = "quay.io/containerdisks/fedora:40"
	case applicationDeltaVersionV2:
		containerImage = applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim")
		composeImage = applicationImageReference(registry, "flightctl-tests/nginx", "v1")
		quadletImage = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		chartVersion = "0.2.0"
		helmImage = applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim")
		vmImage = applicationDeltaVMImageV2
	default:
		return nil, fmt.Errorf("unknown application delta version %d", version)
	}
	if overrides.Container != nil {
		containerImage = overrides.Container.image
	}
	if overrides.Compose != nil {
		composeImage = overrides.Compose.image
	}
	if overrides.Quadlet != nil {
		quadletImage = overrides.Quadlet.image
	}
	if overrides.Helm != nil {
		helmImage = overrides.Helm.image
	}

	containerSpec, err := e2e.NewContainerApplicationSpec(applicationDeltaContainerName, containerImage, nil, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("container application spec: %w", err)
	}
	if overrides.Container != nil && overrides.Container.deltaImage != "" {
		containerSpec, err = withContainerDeltaImageHint(containerSpec, overrides.Container.deltaImage)
		if err != nil {
			return nil, fmt.Errorf("container application delta hint: %w", err)
		}
	}

	composeSpec, err := applicationDeltaComposeSpec(composeImage, overrides.Compose)
	if err != nil {
		return nil, err
	}

	quadletContent := fmt.Sprintf(`[Container]
Image=%s
Exec=sleep infinity
[Install]
WantedBy=default.target
`, quadletImage)
	quadletSpec, err := e2e.NewQuadletInlineSpec(applicationDeltaQuadletName, "", []string{"delta.container"}, []string{quadletContent})
	if err != nil {
		return nil, fmt.Errorf("quadlet application spec: %w", err)
	}
	if overrides.Quadlet != nil && overrides.Quadlet.deltaImage != "" {
		quadletSpec, err = withInlineDeltaImageHint(quadletSpec, applicationDeltaTargetHint(*overrides.Quadlet), v1beta1.AppTypeQuadlet)
		if err != nil {
			return nil, fmt.Errorf("quadlet application delta hint: %w", err)
		}
	}

	chartRef := fmt.Sprintf("%s/flightctl/charts/test-app:%s", registry, chartVersion)
	helmValues, err := helmImageValues(helmImage)
	if err != nil {
		return nil, fmt.Errorf("helm image values: %w", err)
	}
	helmSpec, err := e2e.NewHelmApplicationSpecWithValues(applicationDeltaHelmName, chartRef, applicationDeltaNamespace, helmValues)
	if err != nil {
		return nil, fmt.Errorf("helm application spec: %w", err)
	}
	if overrides.Helm != nil && overrides.Helm.deltaImage != "" {
		helmSpec, err = withHelmNestedDeltaImageHint(helmSpec, applicationDeltaTargetHint(*overrides.Helm))
		if err != nil {
			return nil, fmt.Errorf("helm application delta hint: %w", err)
		}
	}

	vmSpec, err := e2e.NewVmApplicationSpec(applicationDeltaVMName, vmImage)
	if err != nil {
		return nil, fmt.Errorf("VM application spec: %w", err)
	}

	return []v1beta1.ApplicationProviderSpec{containerSpec, composeSpec, quadletSpec, helmSpec, vmSpec}, nil
}

func applicationDeltaComposeSpec(image string, target *applicationDeltaTarget) (v1beta1.ApplicationProviderSpec, error) {
	composeContent := fmt.Sprintf(`version: "3.8"
services:
  worker:
    image: %s
    command: ["sleep", "infinity"]
`, image)
	spec, err := e2e.NewComposeInlineSpec(applicationDeltaComposeName, "podman-compose.yaml", composeContent, "")
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("compose application spec: %w", err)
	}
	if target != nil && target.deltaImage != "" {
		spec, err = withInlineDeltaImageHint(spec, applicationDeltaTargetHint(*target), v1beta1.AppTypeCompose)
		if err != nil {
			return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("compose application delta hint: %w", err)
		}
	}
	return spec, nil
}

func applicationDeltaTargetHint(target applicationDeltaTarget) v1beta1.ImageDeltaHint {
	return v1beta1.ImageDeltaHint{
		TargetImage:  target.image,
		TargetDigest: target.targetDigest,
		DeltaImage:   target.deltaImage,
	}
}

func withInlineDeltaImageHint(spec v1beta1.ApplicationProviderSpec, hint v1beta1.ImageDeltaHint, appType v1beta1.AppType) (v1beta1.ApplicationProviderSpec, error) {
	inlineHint := []v1beta1.ImageDeltaHint{hint}
	switch appType {
	case v1beta1.AppTypeCompose:
		app, err := spec.AsComposeApplication()
		if err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		inlineSpec, err := app.AsInlineApplicationProviderSpec()
		if err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		inlineSpec.DeltaImages = &inlineHint
		if err := app.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		if err := spec.FromComposeApplication(app); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
	case v1beta1.AppTypeQuadlet:
		app, err := spec.AsQuadletApplication()
		if err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		inlineSpec, err := app.AsInlineApplicationProviderSpec()
		if err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		inlineSpec.DeltaImages = &inlineHint
		if err := app.FromInlineApplicationProviderSpec(inlineSpec); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
		if err := spec.FromQuadletApplication(app); err != nil {
			return v1beta1.ApplicationProviderSpec{}, err
		}
	default:
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("application type %q does not use an inline delta hint", appType)
	}
	return spec, nil
}

func withHelmNestedDeltaImageHint(spec v1beta1.ApplicationProviderSpec, hint v1beta1.ImageDeltaHint) (v1beta1.ApplicationProviderSpec, error) {
	app, err := spec.AsHelmApplication()
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	imageSpec, err := app.AsImageApplicationProviderSpec()
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	imageSpec.DeltaImages = &[]v1beta1.ImageDeltaHint{hint}
	if err := app.FromImageApplicationProviderSpec(imageSpec); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	if err := spec.FromHelmApplication(app); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

func helmImageValues(image string) (map[string]any, error) {
	slash := strings.LastIndex(image, "/")
	colon := strings.LastIndex(image, ":")
	if colon <= slash || colon == len(image)-1 {
		return nil, fmt.Errorf("image reference %q must have an explicit tag", image)
	}
	return map[string]any{"image": map[string]any{"repository": image[:colon], "tag": image[colon+1:]}}, nil
}

func withContainerDeltaImageHint(spec v1beta1.ApplicationProviderSpec, deltaImage string) (v1beta1.ApplicationProviderSpec, error) {
	containerApp, err := spec.AsContainerApplication()
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	imageSpec, err := containerApp.AsImageApplicationProviderSpec()
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	imageSpec.DeltaImage = lo.ToPtr(deltaImage)
	if err := containerApp.FromImageApplicationProviderSpec(imageSpec); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	if err := spec.FromContainerApplication(containerApp); err != nil {
		return v1beta1.ApplicationProviderSpec{}, err
	}
	return spec, nil
}

func prepareApplicationDeltaDevice(harness *e2e.Harness, deviceID string) {
	waitDeviceUpToDate(harness, deviceID, "device UpToDate before MicroShift migration")
	v12Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V12)
	Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
		device.Spec.Os = &v1beta1.DeviceOsSpec{Image: v12Image}
	})).To(Succeed())
	Expect(harness.EnsureMicroshiftConfigs()).To(Succeed())
	Expect(harness.WaitForMicroshiftReady(e2e.MicroshiftKubeconfigPath)).To(Succeed())
	waitDeviceUpToDate(harness, deviceID, "device UpToDate on the MicroShift-capable V12 OS")
}

func applicationDeltaFleetSpec(harness *e2e.Harness, fleetName string, apps []v1beta1.ApplicationProviderSpec) v1beta1.FleetSpec {
	deviceSpec, err := harness.CreateFleetDeviceSpec(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V12)
	Expect(err).NotTo(HaveOccurred())
	deviceSpec.Applications = &apps
	selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{fleetLabelKey: fleetName}}
	return v1beta1.FleetSpec{
		Selector: &selector,
		Template: struct {
			Metadata *v1beta1.ObjectMeta `json:"metadata,omitempty"`
			Spec     v1beta1.DeviceSpec  `json:"spec"`
		}{Spec: deviceSpec},
		RolloutPolicy: &v1beta1.RolloutPolicy{
			DeltaGeneration: &v1beta1.RolloutPolicyDeltaGeneration{GenerateDelta: lo.ToPtr(true)},
		},
	}
}

func attachDeviceToApplicationDeltaFleet(harness *e2e.Harness, deviceID, fleetName string) {
	nextRenderedVersion, err := harness.PrepareNextDeviceVersion(deviceID)
	Expect(err).NotTo(HaveOccurred())
	Expect(harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
		harness.SetLabelsForDeviceMetadata(&device.Metadata, map[string]string{fleetLabelKey: fleetName})
	})).To(Succeed())
	Expect(harness.WaitForDeviceNewRenderedVersion(deviceID, nextRenderedVersion)).To(Succeed())
}

func waitForApplicationDeltaApps(harness *e2e.Harness, deviceID string) {
	waitForApplicationNames(harness, deviceID, applicationDeltaAppNames)
}

func waitForApplicationNames(harness *e2e.Harness, deviceID string, appNames []string) {
	for _, appName := range appNames {
		Expect(harness.WaitForApplicationStatus(deviceID, appName, v1beta1.ApplicationStatusRunning, util.LONG_TIMEOUT, util.POLLING)).To(Succeed(), "application %s should be Running", appName)
	}
	Expect(harness.WaitForApplicationSummary(deviceID, util.LONG_TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)).To(Succeed())
}

func waitForApplicationDeltaEvents(harness *e2e.Harness, fleetName, deviceID string, baseline deltaEventBaseline, generationTarget applicationDeltaGenerationTarget) {
	Eventually(func() error {
		deviceEvents, err := newResourceEvents(harness, v1beta1.DeviceKind, deviceID, baseline.device)
		if err != nil {
			return err
		}
		if !hasEventReason(deviceEvents, v1beta1.EventReasonDeviceContentUpToDate) {
			return fmt.Errorf("waiting for a new DeviceContentUpToDate event for device %s", deviceID)
		}
		generationEvents := deviceEvents
		generationKind, generationName := v1beta1.DeviceKind, deviceID
		fleetTemplateVersion := ""
		if fleetName != "" {
			fleetEvents, err := newResourceEvents(harness, v1beta1.FleetKind, fleetName, baseline.fleet)
			if err != nil {
				return err
			}
			fleetTemplateVersion, err = fleetRolloutStartedTemplateVersion(fleetEvents, fleetName)
			if err != nil {
				return err
			}
			generationEvents = fleetEvents
			generationKind, generationName = v1beta1.FleetKind, fleetName
		}
		if generationTarget.digest != "" {
			observation := deltaLifecycleObservation{}
			if err := observeDeltaGenerationProgress(generationEvents, generationKind, generationName, &observation); err != nil {
				return err
			}
			generated, err := hasSuccessfulApplicationDeltaGeneration(generationEvents, generationTarget, fleetTemplateVersion)
			if err != nil {
				return err
			}
			if !generated {
				message := fmt.Sprintf("waiting for a succeeded DeltaGenerationProgress event for %s target digest %s", generationTarget.repository, generationTarget.digest)
				if fleetTemplateVersion != "" {
					message += fmt.Sprintf(" in template version %s", fleetTemplateVersion)
				}
				return fmt.Errorf("%s", message)
			}
		}
		return nil
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func fleetRolloutStartedTemplateVersion(events []v1beta1.Event, fleetName string) (string, error) {
	for _, event := range events {
		if event.Reason != v1beta1.EventReasonFleetRolloutStarted {
			continue
		}
		if event.Details == nil {
			return "", fmt.Errorf("fleet %s FleetRolloutStarted event has no details", fleetName)
		}
		details, err := event.Details.AsFleetRolloutStartedDetails()
		if err != nil {
			return "", fmt.Errorf("fleet %s FleetRolloutStarted event has invalid details: %w", fleetName, err)
		}
		if details.TemplateVersion == "" {
			return "", fmt.Errorf("fleet %s FleetRolloutStarted event has no template version", fleetName)
		}
		return details.TemplateVersion, nil
	}
	return "", fmt.Errorf("waiting for a new FleetRolloutStarted event for fleet %s", fleetName)
}

func hasSuccessfulApplicationDeltaGeneration(events []v1beta1.Event, target applicationDeltaGenerationTarget, fleetTemplateVersion string) (bool, error) {
	for _, event := range events {
		if event.Reason != v1beta1.EventReasonDeltaGenerationProgress {
			continue
		}
		if event.Details == nil {
			return false, fmt.Errorf("DeltaGenerationProgress event has no details")
		}
		details, err := event.Details.AsDeltaGenerationProgressDetails()
		if err != nil {
			return false, fmt.Errorf("DeltaGenerationProgress event has invalid details: %w", err)
		}
		if details.ImageRepository != target.repository || details.TargetDigest != target.digest {
			continue
		}
		if fleetTemplateVersion != "" && (details.TemplateVersion == nil || *details.TemplateVersion != fleetTemplateVersion) {
			continue
		}
		if details.GenerationStatus == v1beta1.DeltaGenerationProgressFailed || details.GenerationStatus == v1beta1.DeltaGenerationProgressRejected {
			return false, fmt.Errorf("delta generation for %s target %s ended with status %q: %s", target.repository, target.digest, details.GenerationStatus, event.Message)
		}
		if details.GenerationStatus == v1beta1.DeltaGenerationProgressSucceeded {
			return true, nil
		}
	}
	return false, nil
}

func waitForApplicationDeltaOutcome(harness *e2e.Harness, deviceID, appName, targetImage string, expectFallback bool) {
	Eventually(func() error {
		device, err := harness.GetDevice(deviceID)
		if err != nil {
			return err
		}
		if device.Status == nil {
			return fmt.Errorf("device %s has no status", deviceID)
		}
		var appStatus *v1beta1.DeviceApplicationStatus
		for i := range device.Status.Applications {
			if device.Status.Applications[i].Name == appName {
				appStatus = &device.Status.Applications[i]
				break
			}
		}
		if appStatus == nil {
			return fmt.Errorf("device %s has no status for application %s", deviceID, appName)
		}
		if appStatus.Status != v1beta1.ApplicationStatusRunning {
			return fmt.Errorf("application %s has status %q, waiting for Running", appName, appStatus.Status)
		}
		if !applicationStatusHasImage(appStatus, targetImage) {
			return fmt.Errorf("application %s has not reported a digest for target image %s", appName, targetImage)
		}
		if expectFallback {
			if appStatus.LastDelta == nil || appStatus.LastDelta.FallbackReason == nil || *appStatus.LastDelta.FallbackReason == "" {
				return fmt.Errorf("application %s has not reported a delta fallback", appName)
			}
			return nil
		}
		if appStatus.LastDelta != nil && appStatus.LastDelta.FallbackReason != nil {
			return StopTrying(fmt.Sprintf("application %s fell back from delta apply: %s", appName, *appStatus.LastDelta.FallbackReason))
		}
		return nil
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func waitForRenderedNestedDeltaHint(harness *e2e.Harness, deviceID, appName string, appType v1beta1.AppType, target applicationDeltaTarget) {
	Eventually(func() error {
		device, err := tryRenderedDevice(harness, deviceID)
		if err != nil {
			return err
		}
		if device.Spec == nil || device.Spec.Applications == nil {
			return fmt.Errorf("rendered device %s has no applications", deviceID)
		}
		for _, spec := range *device.Spec.Applications {
			name, err := spec.GetName()
			if err != nil || name == nil || *name != appName {
				continue
			}
			var hints *[]v1beta1.ImageDeltaHint
			switch appType {
			case v1beta1.AppTypeCompose:
				app, err := spec.AsComposeApplication()
				if err != nil {
					return err
				}
				inline, err := app.AsInlineApplicationProviderSpec()
				if err != nil {
					return err
				}
				hints = inline.DeltaImages
			case v1beta1.AppTypeQuadlet:
				app, err := spec.AsQuadletApplication()
				if err != nil {
					return err
				}
				inline, err := app.AsInlineApplicationProviderSpec()
				if err != nil {
					return err
				}
				hints = inline.DeltaImages
			case v1beta1.AppTypeHelm:
				app, err := spec.AsHelmApplication()
				if err != nil {
					return err
				}
				image, err := app.AsImageApplicationProviderSpec()
				if err != nil {
					return err
				}
				hints = image.DeltaImages
			default:
				return fmt.Errorf("application type %q does not use a nested delta hint", appType)
			}
			if !applicationDeltaHintMatches(hints, target.image, target.deltaImage) {
				return fmt.Errorf("rendered application %s has no delta hint for image %s and artifact %s", appName, target.image, target.deltaImage)
			}
			return nil
		}
		return fmt.Errorf("rendered device %s has no application %s", deviceID, appName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func waitForRenderedVMDeltaHint(harness *e2e.Harness, deviceID, targetImage string) {
	Eventually(func() error {
		device, err := tryRenderedDevice(harness, deviceID)
		if err != nil {
			return err
		}
		if device.Spec == nil || device.Spec.Applications == nil {
			return fmt.Errorf("rendered device %s has no applications", deviceID)
		}
		for _, spec := range *device.Spec.Applications {
			app, err := spec.AsQuadletApplication()
			if err != nil || app.Name == nil || *app.Name != applicationDeltaVMName {
				continue
			}
			inline, err := app.AsInlineApplicationProviderSpec()
			if err != nil {
				return err
			}
			if applicationDeltaHintHasTarget(inline.DeltaImages, targetImage) {
				return nil
			}
			return fmt.Errorf("rendered VM application %s has no generated delta hint for image %s", applicationDeltaVMName, targetImage)
		}
		return fmt.Errorf("rendered device %s has no converted Quadlet for VM application %s", deviceID, applicationDeltaVMName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func applicationDeltaHintMatches(hints *[]v1beta1.ImageDeltaHint, targetImage, deltaImage string) bool {
	if hints == nil {
		return false
	}
	for _, hint := range *hints {
		if hint.TargetImage == targetImage && hint.DeltaImage == deltaImage {
			return true
		}
	}
	return false
}

func applicationDeltaHintHasTarget(hints *[]v1beta1.ImageDeltaHint, targetImage string) bool {
	if hints == nil {
		return false
	}
	for _, hint := range *hints {
		if hint.TargetImage == targetImage && hint.DeltaImage != "" {
			return true
		}
	}
	return false
}

func waitForRenderedContainerDeltaHint(harness *e2e.Harness, deviceID, image, deltaImage string) {
	Eventually(func() error {
		device, err := tryRenderedDevice(harness, deviceID)
		if err != nil {
			return err
		}
		if device.Spec == nil || device.Spec.Applications == nil {
			return fmt.Errorf("rendered device %s has no applications", deviceID)
		}
		for _, spec := range *device.Spec.Applications {
			containerApp, err := spec.AsContainerApplication()
			if err != nil {
				continue
			}
			if containerApp.Name == nil || *containerApp.Name != applicationDeltaContainerName {
				continue
			}
			imageSpec, err := containerApp.AsImageApplicationProviderSpec()
			if err != nil {
				return err
			}
			if imageSpec.Image != image || imageSpec.DeltaImage == nil || *imageSpec.DeltaImage != deltaImage {
				return fmt.Errorf("rendered container app has image %q and deltaImage %v; waiting for %q and %q", imageSpec.Image, imageSpec.DeltaImage, image, deltaImage)
			}
			return nil
		}
		return fmt.Errorf("rendered device %s has no application %s", deviceID, applicationDeltaContainerName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func applicationStatusHasImage(status *v1beta1.DeviceApplicationStatus, image string) bool {
	if status.ImageDigests == nil {
		return false
	}
	for _, imageDigest := range *status.ImageDigests {
		if imageDigest.Image == image && imageDigest.Digest != "" {
			return true
		}
	}
	return false
}

func requireDeviceImageAbsent(harness *e2e.Harness, image string) {
	output, err := harness.RunShellAsUserOnVM("root", fmt.Sprintf("if podman image exists %s; then echo present; else echo absent; fi", shellQuote(image)))
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(output)).To(Equal("absent"), "target image %s must be absent before delta prefetch", image)
}

func removeDeviceImageIfPresent(harness *e2e.Harness, image string) {
	command := fmt.Sprintf("if podman image exists %s; then podman rmi --force %s; fi", shellQuote(image), shellQuote(image))
	_, err := harness.RunShellAsUserOnVM("root", command)
	Expect(err).NotTo(HaveOccurred(), "remove cached target image %s", image)
	requireDeviceImageAbsent(harness, image)
}

func requireCRIImageAbsent(harness *e2e.Harness, image string) {
	output, err := harness.RunShellAsUserOnVM("root", fmt.Sprintf("if crictl inspecti %s >/dev/null 2>&1; then echo present; else echo absent; fi", shellQuote(image)))
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(output)).To(Equal("absent"), "target image %s must be absent from the CRI runtime before delta prefetch", image)
}

func buildApplicationDeltaTarget(harness *e2e.Harness, registry, tag, targetRepository, sourceImage, targetSourceImage string) (applicationDeltaTarget, error) {
	targetTag := applicationDeltaTargetTag(tag)
	registerApplicationDeltaCleanup(targetRepository, targetTag, tag)
	targetImage := applicationImageReference(registry, targetRepository, targetTag)
	deltaImage, err := buildApplicationDeltaArtifact(harness, sourceImage, targetSourceImage, targetImage, tag)
	if err != nil {
		return applicationDeltaTarget{}, err
	}
	targetDigest, err := resolveApplicationImageDigest(harness, registry, targetRepository, targetTag)
	if err != nil {
		return applicationDeltaTarget{}, err
	}
	deleteApplicationDeltaTargetTag(targetRepository, targetTag)
	return applicationDeltaTarget{image: targetImage, targetDigest: targetDigest, deltaImage: deltaImage}, nil
}

func copyApplicationDeltaTarget(harness *e2e.Harness, registry, tag, sourceImage string) (applicationDeltaTarget, error) {
	targetTag := applicationDeltaTargetTag(tag)
	registerApplicationDeltaTargetCleanup(targetTag)
	targetImage := applicationImageReference(registry, applicationDeltaRepository, targetTag)
	if err := copyApplicationImageToTarget(harness, sourceImage, targetImage); err != nil {
		return applicationDeltaTarget{}, err
	}
	targetDigest, err := resolveApplicationImageDigest(harness, registry, applicationDeltaRepository, targetTag)
	if err != nil {
		return applicationDeltaTarget{}, err
	}
	return applicationDeltaTarget{image: targetImage, targetDigest: targetDigest}, nil
}

func copyApplicationImageToTarget(harness *e2e.Harness, sourceImage, targetImage string) error {
	command := fmt.Sprintf("skopeo copy --preserve-digests --src-tls-verify=false --dest-tls-verify=false %s %s",
		shellQuote("docker://"+sourceImage),
		shellQuote("docker://"+targetImage),
	)
	if _, err := harness.RunShellAsUserOnVM("root", "set -eu\n"+command); err != nil {
		return fmt.Errorf("copy application target image %s to %s: %w", sourceImage, targetImage, err)
	}
	return nil
}

func resolveApplicationImageDigest(harness *e2e.Harness, registry, repository, tag string) (string, error) {
	descriptor, err := harness.ResolveImage(registry, repository, tag)
	if err != nil {
		return "", err
	}
	return descriptor.Digest.String(), nil
}

func missingApplicationDeltaReference(registry, tag string) string {
	return applicationImageReference(registry, applicationDeltaRepository, "missing-"+tag)
}

func registerApplicationDeltaTargetCleanup(tag string) {
	DeferCleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client, err := e2eRegistryHTTPClient()
		Expect(err).NotTo(HaveOccurred())
		Expect(deleteRegistryTag(ctx, client, "https://"+auxSvcs.Registry.URL, applicationDeltaRepository, tag)).To(Succeed())
	})
}

func buildApplicationDeltaArtifact(harness *e2e.Harness, sourceImage, targetSourceImage, targetImage, tag string) (string, error) {
	registry := applicationRegistryEndpoint()
	artifactTagRef := fmt.Sprintf("%s/%s:%s", registry, applicationDeltaRepository, tag)
	workDir := fmt.Sprintf("/tmp/flightctl-application-delta-%s", tag)
	sourceLayout := "oci:" + workDir + "/source:img"
	targetLayout := "oci:" + workDir + "/target:img"
	deltaLayout := "oci:" + workDir + "/delta:img"

	commands := []string{
		"set -eu",
		"work=" + shellQuote(workDir),
		"rm -rf \"$work\"",
		"mkdir -p \"$work\"",
		"trap 'rm -rf \"$work\"' EXIT",
		fmt.Sprintf("skopeo copy --preserve-digests --src-tls-verify=false --dest-tls-verify=false %s %s", shellQuote("docker://"+targetSourceImage), shellQuote("docker://"+targetImage)),
		fmt.Sprintf("skopeo copy --preserve-digests --src-tls-verify=false %s %s", shellQuote("docker://"+sourceImage), shellQuote(sourceLayout)),
		fmt.Sprintf("skopeo copy --preserve-digests --src-tls-verify=false %s %s", shellQuote("docker://"+targetImage), shellQuote(targetLayout)),
		fmt.Sprintf("oci-delta create --debug %s %s %s", shellQuote(sourceLayout), shellQuote(targetLayout), shellQuote(deltaLayout)),
		fmt.Sprintf("skopeo copy --preserve-digests --dest-tls-verify=false %s %s", shellQuote(deltaLayout), shellQuote("docker://"+artifactTagRef)),
	}
	if _, err := harness.RunShellAsUserOnVM("root", strings.Join(commands, "\n")); err != nil {
		return "", fmt.Errorf("create and push application delta artifact: %w", err)
	}
	descriptor, err := harness.ResolveImage(registry, applicationDeltaRepository, tag)
	if err != nil {
		return "", fmt.Errorf("resolve application delta artifact: %w", err)
	}
	return fmt.Sprintf("%s/%s@%s", registry, applicationDeltaRepository, descriptor.Digest), nil
}

func applicationDeltaArtifactTag(harness *e2e.Harness) string {
	testID := strings.NewReplacer("-", "", "_", "").Replace(harness.GetTestIDFromContext())
	return "e2e-" + strings.ToLower(testID)
}

func applicationDeltaTargetTag(artifactTag string) string {
	return "target-" + artifactTag
}

func registerApplicationDeltaCleanup(targetRepository, targetTag, artifactTag string) {
	DeferCleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client, err := e2eRegistryHTTPClient()
		Expect(err).NotTo(HaveOccurred())
		registryURL := "https://" + auxSvcs.Registry.URL
		Expect(deleteRegistryTag(ctx, client, registryURL, targetRepository, targetTag)).To(Succeed())
		Expect(deleteRegistryTag(ctx, client, registryURL, applicationDeltaRepository, artifactTag)).To(Succeed())
	})
}

func deleteApplicationDeltaTargetTag(repository, tag string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := e2eRegistryHTTPClient()
	Expect(err).NotTo(HaveOccurred())
	registryURL := "https://" + auxSvcs.Registry.URL
	Expect(deleteRegistryTag(ctx, client, registryURL, repository, tag)).To(Succeed())
	digest, err := registryManifestDigest(ctx, client, registryURL, repository, tag)
	Expect(err).NotTo(HaveOccurred())
	Expect(digest).To(BeEmpty(), "target image tag %s must be unavailable for a full pull", tag)
}

func applicationRegistryEndpoint() string {
	return auxSvcs.Registry.Host + ":" + auxSvcs.Registry.Port
}

func applicationImageReference(registry, repository, tag string) string {
	return fmt.Sprintf("%s/%s:%s", registry, repository, tag)
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func getDeltaDevice(harness *e2e.Harness, deviceID string) *v1beta1.Device {
	device, err := harness.GetDevice(deviceID)
	Expect(err).NotTo(HaveOccurred())
	Expect(device).NotTo(BeNil())
	Expect(device.Status).NotTo(BeNil())
	Expect(device.Status.Os).NotTo(BeNil())
	return device
}
