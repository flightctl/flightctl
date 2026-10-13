package delta

import (
	"context"
	"encoding/json"
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
	applicationDeltaContainerName     = "delta-container"
	applicationDeltaComposeName       = "delta-compose"
	applicationDeltaQuadletName       = "delta-quadlet"
	applicationDeltaHelmName          = "delta-helm"
	applicationDeltaVMName            = "delta-vm"
	applicationDeltaNamespace         = "delta-apps"
	applicationDeltaQuadletVolumeRepo = "flightctl-tests/quadlet-volume"
)

var applicationDeltaNonHelmAppNames = []string{
	applicationDeltaContainerName,
	applicationDeltaComposeName,
	applicationDeltaQuadletName,
	applicationDeltaVMName,
}

var _ = Describe("application delta applications", Label("delta", "slow", "vm"), Serial, func() {
	It("When a standalone device updates container, Compose, Quadlet, and VM applications it should apply their deltas and remain healthy", Label("standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device on the delta-capable base image")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)
		requireDeltaGenerationSupport(harness, deviceID)

		By("installing the four non-Helm V1 application types and waiting for health")
		registry := applicationRegistryEndpoint()
		tagPrefix := applicationDeltaArtifactTag(harness)
		quadletVolumeSource, err := copyApplicationImageToRegistry(
			harness, registry, tagPrefix+"-quadlet-volume-v1", applicationDeltaQuadletVolumeRepo,
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{
			QuadletVolume: &quadletVolumeSource,
		}, applicationDeltaNonHelmAppNames)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v1Apps
		})).To(Succeed())
		waitForApplicationDeltaApps(harness, deviceID, applicationDeltaNonHelmAppNames)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the four non-Helm V1 applications")

		By("capturing the reported-event baseline and building full target images in the source repositories")
		eventBaseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		before := getDeltaDevice(harness, deviceID)

		nginxTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-nginx-v2", "flightctl-tests/nginx",
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		alpineTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-alpine-v2", "flightctl-tests/alpine",
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		quadletVolumeTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-quadlet-volume-v2", applicationDeltaQuadletVolumeRepo,
			quadletVolumeSource.image,
		)
		Expect(err).NotTo(HaveOccurred())
		allImageTargets := []applicationDeltaTarget{nginxTarget, alpineTarget, quadletVolumeTarget}
		By("keeping each full target image in the registry while ensuring it is absent from the device")
		for _, target := range allImageTargets {
			requireDeviceImageAbsent(harness, target.image)
		}
		vmV2Image := applicationDeltaVMImageV2
		removeDeviceImageIfPresent(harness, vmV2Image)

		v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
			Container:     &nginxTarget,
			Compose:       []applicationDeltaTarget{nginxTarget, alpineTarget},
			Quadlet:       &nginxTarget,
			QuadletImage:  &alpineTarget,
			QuadletVolume: &quadletVolumeTarget,
		}, applicationDeltaNonHelmAppNames)
		Expect(err).NotTo(HaveOccurred())
		assertNoDesiredApplicationDeltaHints(v2Apps)
		By("updating the four non-Helm application definitions in one device mutation")
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v2Apps
		})).To(Succeed())
		generationTargets := applicationDeltaTargets(allImageTargets...)
		generationTargets = append(generationTargets, applicationDeltaGenerationTarget{repository: applicationImageRepository(vmV2Image)})
		waitForApplicationDeltaGenerationEvents(harness, "", deviceID, eventBaseline, generationTargets...)
		containerHint := waitForRenderedContainerDeltaHint(harness, deviceID, nginxTarget)
		composeHints := waitForRenderedNestedDeltaHints(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, []applicationDeltaTarget{nginxTarget, alpineTarget})
		quadletTargets := []applicationDeltaTarget{nginxTarget, alpineTarget, quadletVolumeTarget}
		quadletHints := waitForRenderedNestedDeltaHints(harness, deviceID, applicationDeltaQuadletName, v1beta1.AppTypeQuadlet, quadletTargets)
		vmHint := waitForRenderedVMDeltaHint(harness, deviceID, vmV2Image)
		allHints := append([]v1beta1.ImageDeltaHint{
			{TargetImage: nginxTarget.image, TargetDigest: nginxTarget.targetDigest, DeltaImage: containerHint},
		}, composeHints...)
		allHints = append(allHints, quadletHints...)
		allHints = append(allHints, vmHint)
		registerApplicationDeltaArtifactsCleanup(allHints)

		By("checking application health and each application delta result")
		waitForApplicationDeltaApps(harness, deviceID, applicationDeltaNonHelmAppNames)
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, eventBaseline)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the four non-Helm V2 applications")
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaContainerName, nginxTarget, v1beta1.DeviceDeltaApplyOutcomeApplied)
		for _, target := range []applicationDeltaTarget{nginxTarget, alpineTarget} {
			waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaComposeName, target, v1beta1.DeviceDeltaApplyOutcomeApplied)
		}
		for _, target := range quadletTargets {
			waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaQuadletName, target, v1beta1.DeviceDeltaApplyOutcomeApplied)
		}
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaVMName, applicationDeltaTarget{image: vmV2Image, repository: applicationImageRepository(vmV2Image), targetDigest: vmHint.TargetDigest}, v1beta1.DeviceDeltaApplyOutcomeApplied)
		after := getDeltaDevice(harness, deviceID)
		Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
	})

	It("When a fleet updates container, Compose, Quadlet, and VM applications it should apply their deltas and remain healthy", Label("fleet"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a device on the delta-capable base image")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		fleetName := "delta-app-" + strings.TrimPrefix(applicationDeltaArtifactTag(harness), "e2e-")
		tagPrefix := applicationDeltaArtifactTag(harness) + "-fleet"
		quadletVolumeSource, err := copyApplicationImageToRegistry(
			harness, registry, tagPrefix+"-quadlet-volume-v1", applicationDeltaQuadletVolumeRepo,
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{
			QuadletVolume: &quadletVolumeSource,
		}, applicationDeltaNonHelmAppNames)
		Expect(err).NotTo(HaveOccurred())
		By("applying the V1 application set through fleet ownership")
		v1FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v1Apps, "")
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v1FleetSpec)).To(Succeed())
		attachDeviceToApplicationDeltaFleet(harness, deviceID, fleetName)
		waitForApplicationDeltaApps(harness, deviceID, applicationDeltaNonHelmAppNames)
		waitDeviceUpToDate(harness, deviceID, "fleet-owned device UpToDate with the four non-Helm V1 applications")

		eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceID)
		Expect(err).NotTo(HaveOccurred())
		before := getDeltaDevice(harness, deviceID)

		By("building full target images for the fleet-owned update while leaving delta generation to the server")
		nginxTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-nginx-v2", "flightctl-tests/nginx",
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		alpineTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-alpine-v2", "flightctl-tests/alpine",
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		quadletVolumeTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-quadlet-volume-v2", applicationDeltaQuadletVolumeRepo,
			quadletVolumeSource.image,
		)
		Expect(err).NotTo(HaveOccurred())
		allImageTargets := []applicationDeltaTarget{nginxTarget, alpineTarget, quadletVolumeTarget}
		for _, target := range allImageTargets {
			requireDeviceImageAbsent(harness, target.image)
		}
		vmV2Image := applicationDeltaVMImageV2
		removeDeviceImageIfPresent(harness, vmV2Image)
		v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
			Container:     &nginxTarget,
			Compose:       []applicationDeltaTarget{nginxTarget, alpineTarget},
			Quadlet:       &nginxTarget,
			QuadletImage:  &alpineTarget,
			QuadletVolume: &quadletVolumeTarget,
		}, applicationDeltaNonHelmAppNames)
		Expect(err).NotTo(HaveOccurred())
		assertNoDesiredApplicationDeltaHints(v2Apps)
		v2FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v2Apps, "")

		Expect(harness.CreateOrUpdateTestFleet(fleetName, v2FleetSpec)).To(Succeed())
		generationTargets := applicationDeltaTargets(allImageTargets...)
		generationTargets = append(generationTargets, applicationDeltaGenerationTarget{repository: applicationImageRepository(vmV2Image)})
		waitForApplicationDeltaGenerationEvents(harness, fleetName, deviceID, eventBaseline, generationTargets...)
		containerHint := waitForRenderedContainerDeltaHint(harness, deviceID, nginxTarget)
		composeHints := waitForRenderedNestedDeltaHints(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, []applicationDeltaTarget{nginxTarget, alpineTarget})
		quadletTargets := []applicationDeltaTarget{nginxTarget, alpineTarget, quadletVolumeTarget}
		quadletHints := waitForRenderedNestedDeltaHints(harness, deviceID, applicationDeltaQuadletName, v1beta1.AppTypeQuadlet, quadletTargets)
		vmHint := waitForRenderedVMDeltaHint(harness, deviceID, vmV2Image)
		allHints := append([]v1beta1.ImageDeltaHint{
			{TargetImage: nginxTarget.image, TargetDigest: nginxTarget.targetDigest, DeltaImage: containerHint},
		}, composeHints...)
		allHints = append(allHints, quadletHints...)
		allHints = append(allHints, vmHint)
		registerApplicationDeltaArtifactsCleanup(allHints)

		By("checking application health and successful delta results")
		waitForApplicationDeltaApps(harness, deviceID, applicationDeltaNonHelmAppNames)
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, eventBaseline)
		waitDeviceUpToDate(harness, deviceID, "fleet-owned device UpToDate with the four non-Helm V2 applications")
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaContainerName, nginxTarget, v1beta1.DeviceDeltaApplyOutcomeApplied)
		for _, target := range []applicationDeltaTarget{nginxTarget, alpineTarget} {
			waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaComposeName, target, v1beta1.DeviceDeltaApplyOutcomeApplied)
		}
		for _, target := range quadletTargets {
			waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaQuadletName, target, v1beta1.DeviceDeltaApplyOutcomeApplied)
		}
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaVMName, applicationDeltaTarget{image: vmV2Image, repository: applicationImageRepository(vmV2Image), targetDigest: vmHint.TargetDigest}, v1beta1.DeviceDeltaApplyOutcomeApplied)
		after := getDeltaDevice(harness, deviceID)
		Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
	})

	It("When a nested application delta artifact is unavailable it should full-pull and report the fallback", Label("fallback", "standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device on the delta-capable base image")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		composeV1, err := applicationDeltaComposeSpec([]string{applicationImageReference(registry, "flightctl-tests/alpine", "v1")})
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

		By("preparing a full-pullable target; the control plane will generate its delta")
		tagPrefix := applicationDeltaArtifactTag(harness) + "-fallback"
		target, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix, "flightctl-tests/alpine", applicationImageReference(registry, "flightctl-tests/alpine", "v1"))
		Expect(err).NotTo(HaveOccurred())
		requireDeviceImageAbsent(harness, target.image)
		composeV2, err := applicationDeltaComposeSpec([]string{target.image})
		Expect(err).NotTo(HaveOccurred())
		v2Apps := []v1beta1.ApplicationProviderSpec{composeV2}
		assertNoDesiredApplicationDeltaHints(v2Apps)
		By("pausing the agent so the generated hint can be checked before the artifact is removed")
		Expect(harness.StopFlightCtlAgent()).To(Succeed())
		agentStopped := true
		DeferCleanup(func() {
			if agentStopped {
				Expect(harness.StartFlightCtlAgent()).To(Succeed())
			}
		})
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v2Apps
		})).To(Succeed())
		waitForApplicationDeltaGenerationEvents(harness, "", deviceID, eventBaseline, applicationDeltaTargets(target)...)
		hint := waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, target)
		registerApplicationDeltaArtifactsCleanup([]v1beta1.ImageDeltaHint{hint})
		deleteApplicationDeltaArtifact(hint.DeltaImage)
		Expect(harness.StartFlightCtlAgent()).To(Succeed())
		agentStopped = false
		waitForApplicationNames(harness, deviceID, []string{applicationDeltaComposeName})
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, eventBaseline)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate after Compose delta fallback")
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaComposeName, target, v1beta1.DeviceDeltaApplyOutcomeFallback)
		after := getDeltaDevice(harness, deviceID)
		Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application fallback must not change OS delta status")
	})

	It("When a later application prepare requests an already generated image delta it should reuse and apply it", Label("prepare-reuse", "standalone"), func() {
		harness := e2e.GetWorkerHarness()
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		createWritableDeltaRepo(harness)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		sourceImage := applicationImageReference(registry, "flightctl-tests/nginx", "v1")
		containerV1, err := e2e.NewContainerApplicationSpec(applicationDeltaContainerName, sourceImage, nil, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		composeV1, err := applicationDeltaComposeSpec([]string{sourceImage})
		Expect(err).NotTo(HaveOccurred())
		v1Apps := []v1beta1.ApplicationProviderSpec{containerV1, composeV1}
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v1Apps
		})).To(Succeed())
		appNames := []string{applicationDeltaContainerName, applicationDeltaComposeName}
		waitForApplicationDeltaApps(harness, deviceID, appNames)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 container and Compose applications")

		tagPrefix := applicationDeltaArtifactTag(harness) + "-prepare-reuse"
		firstTarget, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix+"-first", "flightctl-tests/nginx", sourceImage)
		Expect(err).NotTo(HaveOccurred())
		secondTarget, err := copyApplicationImageToRegistry(harness, registry, tagPrefix+"-second", "flightctl-tests/nginx", firstTarget.image)
		Expect(err).NotTo(HaveOccurred())
		Expect(secondTarget.targetDigest).To(Equal(firstTarget.targetDigest), "both tags must identify the same target manifest")
		requireDeviceImageAbsent(harness, firstTarget.image)
		requireDeviceImageAbsent(harness, secondTarget.image)

		before := getDeltaDevice(harness, deviceID)
		firstBaseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		firstContainerV2, err := e2e.NewContainerApplicationSpec(applicationDeltaContainerName, firstTarget.image, nil, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		firstApps := []v1beta1.ApplicationProviderSpec{firstContainerV2, composeV1}
		assertNoDesiredApplicationDeltaHints(firstApps)
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &firstApps
		})).To(Succeed())
		waitForApplicationDeltaGenerationEvents(harness, "", deviceID, firstBaseline, applicationDeltaTargets(firstTarget)...)
		firstDeltaImage := waitForRenderedContainerDeltaHint(harness, deviceID, firstTarget)
		firstHint := v1beta1.ImageDeltaHint{TargetImage: firstTarget.image, TargetDigest: firstTarget.targetDigest, DeltaImage: firstDeltaImage}
		registerApplicationDeltaArtifactsCleanup([]v1beta1.ImageDeltaHint{firstHint})
		waitForApplicationDeltaApps(harness, deviceID, appNames)
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, firstBaseline)
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaContainerName, firstTarget, v1beta1.DeviceDeltaApplyOutcomeApplied)

		secondBaseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		secondComposeV2, err := applicationDeltaComposeSpec([]string{secondTarget.image})
		Expect(err).NotTo(HaveOccurred())
		secondApps := []v1beta1.ApplicationProviderSpec{firstContainerV2, secondComposeV2}
		assertNoDesiredApplicationDeltaHints(secondApps)
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &secondApps
		})).To(Succeed())
		secondHints := waitForRenderedNestedDeltaHints(harness, deviceID, applicationDeltaComposeName, v1beta1.AppTypeCompose, []applicationDeltaTarget{secondTarget})
		Expect(secondHints).To(HaveLen(1))
		Expect(secondHints[0].DeltaImage).To(Equal(firstHint.DeltaImage), "the later prepare should reuse the completed source/target delta")
		registerApplicationDeltaArtifactsCleanup(secondHints)
		waitForApplicationDeltaApps(harness, deviceID, appNames)
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, secondBaseline)
		expectNoNewApplicationDeltaGenerationEvent(harness, deviceID, secondBaseline, secondTarget)
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaComposeName, secondTarget, v1beta1.DeviceDeltaApplyOutcomeApplied)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate after the later Compose delta prepare")
		Expect(getDeltaDevice(harness, deviceID).Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
	})
})

type applicationDeltaVersion int

const (
	applicationDeltaVersionV1 applicationDeltaVersion = iota
	applicationDeltaVersionV2
)

type applicationDeltaTarget struct {
	repository   string
	image        string
	targetDigest string
}

type applicationDeltaOverrides struct {
	Container     *applicationDeltaTarget
	Compose       []applicationDeltaTarget
	Quadlet       *applicationDeltaTarget
	QuadletImage  *applicationDeltaTarget
	QuadletVolume *applicationDeltaTarget
	HelmValues    map[string]any
}

type applicationDeltaGenerationTarget struct {
	repository string
	digest     string
}

const (
	applicationDeltaVMImageV2 = "quay.io/containerdisks/fedora:41"
)

func applicationDeltaSpecsForNames(registry string, version applicationDeltaVersion, overrides applicationDeltaOverrides, appNames []string) ([]v1beta1.ApplicationProviderSpec, error) {
	specs, err := applicationDeltaSpecs(registry, version, overrides)
	if err != nil {
		return nil, err
	}

	wanted := make(map[string]struct{}, len(appNames))
	for _, name := range appNames {
		wanted[name] = struct{}{}
	}
	selected := make([]v1beta1.ApplicationProviderSpec, 0, len(appNames))
	for _, spec := range specs {
		name, err := spec.GetName()
		if err != nil {
			return nil, fmt.Errorf("get application spec name: %w", err)
		}
		if name == nil {
			return nil, fmt.Errorf("application spec has no name")
		}
		if _, ok := wanted[*name]; ok {
			selected = append(selected, spec)
			delete(wanted, *name)
		}
	}
	for _, name := range appNames {
		if _, ok := wanted[name]; ok {
			return nil, fmt.Errorf("application spec %q was not created", name)
		}
	}
	return selected, nil
}

func applicationDeltaSpecs(registry string, version applicationDeltaVersion, overrides applicationDeltaOverrides) ([]v1beta1.ApplicationProviderSpec, error) {
	var containerImage, quadletImage, quadletImageUnit, quadletVolumeImage, chartVersion, helmImage, vmImage string
	var composeImages []string
	switch version {
	case applicationDeltaVersionV1:
		containerImage = applicationImageReference(registry, "flightctl-tests/nginx", "v1")
		quadletImage = applicationImageReference(registry, "flightctl-tests/nginx", "v1")
		quadletImageUnit = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		quadletVolumeImage = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		composeImages = []string{
			applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		}
		chartVersion = "0.1.0"
		helmImage = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		vmImage = "quay.io/containerdisks/fedora:40"
	case applicationDeltaVersionV2:
		containerImage = applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim")
		quadletImage = applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim")
		quadletImageUnit = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		quadletVolumeImage = applicationImageReference(registry, "flightctl-tests/alpine", "v1")
		composeImages = []string{
			applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim"),
			applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		}
		chartVersion = "0.2.0"
		helmImage = applicationImageReference(registry, "flightctl-tests/nginx", "1.28-alpine-slim")
		vmImage = applicationDeltaVMImageV2
	default:
		return nil, fmt.Errorf("unknown application delta version %d", version)
	}
	if overrides.Container != nil {
		containerImage = overrides.Container.image
	}
	if len(overrides.Compose) > 0 {
		composeImages = make([]string, 0, len(overrides.Compose))
		for _, target := range overrides.Compose {
			composeImages = append(composeImages, target.image)
		}
	}
	if overrides.Quadlet != nil {
		quadletImage = overrides.Quadlet.image
	}
	if overrides.QuadletImage != nil {
		quadletImageUnit = overrides.QuadletImage.image
	}
	if overrides.QuadletVolume != nil {
		quadletVolumeImage = overrides.QuadletVolume.image
	}
	containerSpec, err := e2e.NewContainerApplicationSpec(applicationDeltaContainerName, containerImage, nil, nil, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("container application spec: %w", err)
	}
	composeSpec, err := applicationDeltaComposeSpec(composeImages)
	if err != nil {
		return nil, err
	}

	// The .kube/Pod YAML fixture was removed because kube-quadlet workloads lack
	// the application labels needed by the monitor to report image digest snapshots.
	// Kube-quadlet needs separate delta e2e coverage; .pod quadlets remain covered.
	quadletPaths := []string{
		"app.network", "app.pod", "app.container", "model-data.volume", "data.volume",
		"worker-image.image", "worker.container",
	}
	quadletContents := []string{
		"[Network]\nDriver=bridge\n",
		"[Pod]\nNetwork=app.network\n",
		fmt.Sprintf(`[Container]
Image=%s
Pod=app.pod
Volume=model-data:/mnt/model:ro
Exec=sh -c "echo 'Primary container started.' && sleep infinity"
[Install]
WantedBy=default.target

`, quadletImage),
		"[Volume]\nDriver=local\n",
		fmt.Sprintf("[Volume]\nDriver=image\nImage=%s\n", quadletVolumeImage),
		fmt.Sprintf("[Image]\nImage=%s\n", quadletImageUnit),
		`[Container]
Image=worker-image.image
Pod=app.pod
Volume=data.volume:/mnt/data
Exec=sh -c "echo 'Worker started.' && sleep infinity"
[Install]
WantedBy=default.target
`,
	}
	quadletSpec, err := e2e.NewQuadletInlineSpec(applicationDeltaQuadletName, "", quadletPaths, quadletContents)
	if err != nil {
		return nil, fmt.Errorf("quadlet application spec: %w", err)
	}

	chartRef := fmt.Sprintf("%s/flightctl/charts/test-app:%s", registry, chartVersion)
	helmValues := overrides.HelmValues
	if helmValues == nil {
		helmValues, err = helmImageValues(helmImage)
		if err != nil {
			return nil, fmt.Errorf("helm image values: %w", err)
		}
	}
	helmSpec, err := e2e.NewHelmApplicationSpecWithValues(applicationDeltaHelmName, chartRef, applicationDeltaNamespace, helmValues)
	if err != nil {
		return nil, fmt.Errorf("helm application spec: %w", err)
	}
	vmSpec, err := e2e.NewVmApplicationSpec(applicationDeltaVMName, vmImage)
	if err != nil {
		return nil, fmt.Errorf("VM application spec: %w", err)
	}

	return []v1beta1.ApplicationProviderSpec{containerSpec, composeSpec, quadletSpec, helmSpec, vmSpec}, nil
}

func applicationDeltaComposeSpec(images []string) (v1beta1.ApplicationProviderSpec, error) {
	if len(images) == 0 {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("compose application requires at least one image")
	}
	serviceNames := []string{"web", "worker"}
	var composeContent strings.Builder
	composeContent.WriteString("version: \"3.8\"\nservices:\n")
	for i, image := range images {
		name := fmt.Sprintf("service-%d", i+1)
		if i < len(serviceNames) {
			name = serviceNames[i]
		}
		fmt.Fprintf(&composeContent, "  %s:\n    image: %s\n    command: [\"sleep\", \"infinity\"]\n", name, image)
	}
	spec, err := e2e.NewComposeInlineSpec(applicationDeltaComposeName, "podman-compose.yaml", composeContent.String(), "")
	if err != nil {
		return v1beta1.ApplicationProviderSpec{}, fmt.Errorf("compose application spec: %w", err)
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

func prepareNonHelmApplicationDeltaDevice(harness *e2e.Harness, deviceID string) {
	waitDeviceUpToDate(harness, deviceID, "device UpToDate on the delta-capable base image")
}

func applicationDeltaFleetSpec(harness *e2e.Harness, fleetName string, apps []v1beta1.ApplicationProviderSpec, deviceImageTag string) v1beta1.FleetSpec {
	deviceSpec, err := harness.CreateFleetDeviceSpec(auxSvcs.Registry.Host, auxSvcs.Registry.Port, deviceImageTag)
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

func waitForApplicationDeltaApps(harness *e2e.Harness, deviceID string, appNames []string) {
	waitForApplicationNames(harness, deviceID, appNames)
}

func waitForApplicationNames(harness *e2e.Harness, deviceID string, appNames []string) {
	for _, appName := range appNames {
		Expect(harness.WaitForApplicationStatus(deviceID, appName, v1beta1.ApplicationStatusRunning, util.LONG_TIMEOUT, util.POLLING)).To(Succeed(), "application %s should be Running", appName)
	}
	Expect(harness.WaitForApplicationSummary(deviceID, util.LONG_TIMEOUT, util.POLLING, v1beta1.ApplicationsSummaryStatusHealthy)).To(Succeed())
}

func waitForApplicationDeltaGenerationEvents(harness *e2e.Harness, fleetName, deviceID string, baseline deltaEventBaseline, generationTargets ...applicationDeltaGenerationTarget) {
	if len(generationTargets) == 0 {
		return
	}
	Eventually(func() error {
		deviceEvents, err := newResourceEvents(harness, v1beta1.DeviceKind, deviceID, baseline.device)
		if err != nil {
			return err
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
		observation := newDeltaLifecycleObservation()
		if err := observeDeltaGenerationProgress(generationEvents, generationKind, generationName, &observation); err != nil {
			return err
		}
		for _, generationTarget := range generationTargets {
			if generationTarget.repository == "" {
				continue
			}
			generated, err := hasSuccessfulApplicationDeltaGeneration(generationEvents, generationTarget, fleetTemplateVersion)
			if err != nil {
				return err
			}
			if !generated {
				message := fmt.Sprintf("waiting for a succeeded DeltaGenerationProgress event for %s", generationTarget.repository)
				if generationTarget.digest != "" {
					message += fmt.Sprintf(" target digest %s", generationTarget.digest)
				}
				if fleetTemplateVersion != "" {
					message += fmt.Sprintf(" in template version %s", fleetTemplateVersion)
				}
				return fmt.Errorf("%s", message)
			}
		}
		return nil
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func waitForApplicationDeltaContentUpToDateEvent(harness *e2e.Harness, deviceID string, baseline deltaEventBaseline) {
	Eventually(func() error {
		deviceEvents, err := newResourceEvents(harness, v1beta1.DeviceKind, deviceID, baseline.device)
		if err != nil {
			return err
		}
		if !hasEventReason(deviceEvents, v1beta1.EventReasonDeviceContentUpToDate) {
			return fmt.Errorf("waiting for a new DeviceContentUpToDate event for device %s", deviceID)
		}
		return nil
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func expectNoNewApplicationDeltaGenerationEvent(harness *e2e.Harness, deviceID string, baseline deltaEventBaseline, target applicationDeltaTarget) {
	events, err := newResourceEvents(harness, v1beta1.DeviceKind, deviceID, baseline.device)
	Expect(err).NotTo(HaveOccurred())
	for _, event := range events {
		if event.Reason != v1beta1.EventReasonDeltaGenerationProgress || event.Details == nil {
			continue
		}
		details, err := event.Details.AsDeltaGenerationProgressDetails()
		Expect(err).NotTo(HaveOccurred())
		if details.ImageRepository == target.repository && details.TargetDigest == target.targetDigest {
			Fail(fmt.Sprintf("unexpected new DeltaGenerationProgress event for reused application delta %s target digest %s", target.repository, target.targetDigest))
		}
	}
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
		if details.ImageRepository != target.repository || (target.digest != "" && details.TargetDigest != target.digest) {
			continue
		}
		if fleetTemplateVersion != "" && (details.TemplateVersion == nil || *details.TemplateVersion != fleetTemplateVersion) {
			continue
		}
		if details.GenerationStatus == v1beta1.DeltaGenerationProgressFailed || details.GenerationStatus == v1beta1.DeltaGenerationProgressRejected {
			return false, fmt.Errorf("delta generation for %s target %s ended with status %q: %s", target.repository, details.TargetDigest, details.GenerationStatus, event.Message)
		}
		if details.GenerationStatus == v1beta1.DeltaGenerationProgressSucceeded {
			return true, nil
		}
	}
	return false, nil
}

func waitForApplicationDeltaOutcome(harness *e2e.Harness, deviceID, appName string, target applicationDeltaTarget, expectedOutcome v1beta1.DeviceDeltaApplyOutcomeType) {
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
		if !applicationStatusHasImageDigest(appStatus, target.image, target.targetDigest) {
			return fmt.Errorf("application %s has not reported target image %s with registry digest %s", appName, target.image, target.targetDigest)
		}
		if appStatus.DeltaSize == nil || *appStatus.DeltaSize == "" {
			return fmt.Errorf("application %s has not reported the control-plane delta size", appName)
		}
		if appStatus.LastDelta == nil {
			return fmt.Errorf("application %s has not reported a delta apply outcome", appName)
		}
		if appStatus.LastDelta.Outcome != expectedOutcome {
			reason := ""
			if appStatus.LastDelta.FallbackReason != nil {
				reason = ": " + *appStatus.LastDelta.FallbackReason
			}
			return StopTrying(fmt.Sprintf("application %s reported delta outcome %q, expected %q%s", appName, appStatus.LastDelta.Outcome, expectedOutcome, reason))
		}
		if expectedOutcome == v1beta1.DeviceDeltaApplyOutcomeFallback && (appStatus.LastDelta.FallbackReason == nil || *appStatus.LastDelta.FallbackReason == "") {
			return fmt.Errorf("application %s reported delta fallback without a reason", appName)
		}
		return nil
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
}

func waitForRenderedNestedDeltaHint(harness *e2e.Harness, deviceID, appName string, appType v1beta1.AppType, target applicationDeltaTarget) v1beta1.ImageDeltaHint {
	hints := waitForRenderedNestedDeltaHints(harness, deviceID, appName, appType, []applicationDeltaTarget{target})
	return hints[0]
}

func waitForRenderedNestedDeltaHints(harness *e2e.Harness, deviceID, appName string, appType v1beta1.AppType, targets []applicationDeltaTarget) []v1beta1.ImageDeltaHint {
	var matched []v1beta1.ImageDeltaHint
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
			if hints == nil {
				return fmt.Errorf("rendered application %s has no generated delta hints", appName)
			}
			matched = make([]v1beta1.ImageDeltaHint, 0, len(targets))
			for _, target := range targets {
				found := false
				for _, hint := range *hints {
					if hint.TargetImage != target.image || hint.TargetDigest != target.targetDigest || hint.DeltaImage == "" {
						continue
					}
					matched = append(matched, hint)
					found = true
					break
				}
				if !found {
					return fmt.Errorf("rendered application %s has no generated delta hint for image %s with target digest %s", appName, target.image, target.targetDigest)
				}
			}
			return nil
		}
		return fmt.Errorf("rendered device %s has no application %s", deviceID, appName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
	return matched
}

func waitForRenderedVMDeltaHint(harness *e2e.Harness, deviceID, targetImage string) v1beta1.ImageDeltaHint {
	var matched v1beta1.ImageDeltaHint
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
			if inline.DeltaImages != nil {
				for _, hint := range *inline.DeltaImages {
					if hint.TargetImage == targetImage && hint.TargetDigest != "" && hint.DeltaImage != "" {
						matched = hint
						return nil
					}
				}
			}
			return fmt.Errorf("rendered VM application %s has no generated delta hint for image %s", applicationDeltaVMName, targetImage)
		}
		return fmt.Errorf("rendered device %s has no converted Quadlet for VM application %s", deviceID, applicationDeltaVMName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
	return matched
}

func waitForRenderedContainerDeltaHint(harness *e2e.Harness, deviceID string, target applicationDeltaTarget) string {
	var deltaImage string
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
			if imageSpec.Image != target.image || imageSpec.DeltaImage == nil || *imageSpec.DeltaImage == "" {
				return fmt.Errorf("rendered container app has image %q and deltaImage %v; waiting for image %q with a server-generated delta hint", imageSpec.Image, imageSpec.DeltaImage, target.image)
			}
			deltaImage = *imageSpec.DeltaImage
			return nil
		}
		return fmt.Errorf("rendered device %s has no application %s", deviceID, applicationDeltaContainerName)
	}, util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
	return deltaImage
}

func applicationStatusHasImageDigest(status *v1beta1.DeviceApplicationStatus, image, digest string) bool {
	if status.ImageDigests == nil {
		return false
	}
	for _, imageDigest := range *status.ImageDigests {
		if imageDigest.Image == image && imageDigest.Digest == digest && digest != "" {
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

func requireCRIImagePresent(harness *e2e.Harness, image string) {
	output, err := harness.RunShellAsUserOnVM("root", fmt.Sprintf("if crictl inspecti %s >/dev/null 2>&1; then echo present; else echo absent; fi", shellQuote(image)))
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.TrimSpace(output)).To(Equal("present"), "target image %s must be present in the CRI runtime after delta prefetch", image)
}

func copyApplicationImageToRegistry(harness *e2e.Harness, registry, tag, targetRepository, sourceImage string) (applicationDeltaTarget, error) {
	targetTag := applicationDeltaTargetTag(tag)
	registerApplicationImageCleanup(targetRepository, targetTag)
	targetImage := applicationImageReference(registry, targetRepository, targetTag)
	if err := copyApplicationImageToTarget(harness, sourceImage, targetImage); err != nil {
		return applicationDeltaTarget{}, err
	}
	targetDigest, err := resolveApplicationImageDigest(harness, registry, targetRepository, targetTag)
	if err != nil {
		return applicationDeltaTarget{}, err
	}
	return applicationDeltaTarget{repository: applicationImageRepository(targetImage), image: targetImage, targetDigest: targetDigest}, nil
}

func buildApplicationDeltaTargetImage(harness *e2e.Harness, registry, tag, targetRepository, sourceImage string) (applicationDeltaTarget, error) {
	targetTag := applicationDeltaTargetTag(tag)
	registerApplicationImageCleanup(targetRepository, targetTag)
	targetImage := applicationImageReference(registry, targetRepository, targetTag)
	containerName := "flightctl-delta-build-" + tag
	markerPath := "/tmp/flightctl-delta-marker-" + tag
	containerCommand := fmt.Sprintf("printf '%%s\\n' %s > %s", shellQuote(tag), shellQuote(markerPath))
	commands := []string{
		"set -eu",
		fmt.Sprintf("container=%s", shellQuote(containerName)),
		fmt.Sprintf("target=%s", shellQuote(targetImage)),
		"cleanup() { podman rm --force \"$container\" >/dev/null 2>&1 || true; podman image rm --force \"$target\" >/dev/null 2>&1 || true; }",
		"trap cleanup EXIT",
		fmt.Sprintf("podman pull --tls-verify=false %s >/dev/null", shellQuote(sourceImage)),
		fmt.Sprintf("entrypoint=$(podman image inspect --format '{{json .Config.Entrypoint}}' %s)", shellQuote(sourceImage)),
		fmt.Sprintf("cmd=$(podman image inspect --format '{{json .Config.Cmd}}' %s)", shellQuote(sourceImage)),
		fmt.Sprintf("user=$(podman image inspect --format '{{.Config.User}}' %s)", shellQuote(sourceImage)),
		"[ \"$entrypoint\" != null ] || entrypoint='[]'",
		"[ \"$cmd\" != null ] || cmd='[]'",
		fmt.Sprintf("podman create --name \"$container\" --user 0 --entrypoint /bin/sh %s -c %s >/dev/null", shellQuote(sourceImage), shellQuote(containerCommand)),
		"podman start --attach \"$container\" >/dev/null",
		"podman commit --change \"ENTRYPOINT $entrypoint\" --change \"CMD $cmd\" --change \"USER ${user:-0}\" \"$container\" \"$target\" >/dev/null",
		"podman rm \"$container\" >/dev/null",
		"podman push --tls-verify=false \"$target\" \"docker://$target\" >/dev/null",
		"podman image rm --force \"$target\" >/dev/null",
	}
	if _, err := harness.RunShellAsUserOnVM("root", strings.Join(commands, "\n")); err != nil {
		return applicationDeltaTarget{}, fmt.Errorf("build and push full application target image %s from %s: %w", targetImage, sourceImage, err)
	}
	targetDigest, err := resolveApplicationImageDigest(harness, registry, targetRepository, targetTag)
	if err != nil {
		return applicationDeltaTarget{}, err
	}
	return applicationDeltaTarget{repository: applicationImageRepository(targetImage), image: targetImage, targetDigest: targetDigest}, nil
}

func copyApplicationImageToTarget(harness *e2e.Harness, sourceImage, targetImage string) error {
	command := fmt.Sprintf("skopeo copy --preserve-digests --src-tls-verify=false --dest-tls-verify=false %s %s",
		shellQuote("docker://"+sourceImage),
		shellQuote("docker://"+targetImage),
	)
	if _, err := harness.RunShellAsUserOnVM("root", command); err != nil {
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

func applicationDeltaArtifactTag(harness *e2e.Harness) string {
	testID := strings.NewReplacer("-", "", "_", "").Replace(harness.GetTestIDFromContext())
	return "e2e-" + strings.ToLower(testID)
}

func applicationDeltaTargetTag(artifactTag string) string {
	return "target-" + artifactTag
}

func registerApplicationImageCleanup(targetRepository, targetTag string) {
	DeferCleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		client, err := e2eRegistryHTTPClient()
		Expect(err).NotTo(HaveOccurred())
		registryURL := "https://" + auxSvcs.Registry.URL
		Expect(deleteRegistryTag(ctx, client, registryURL, targetRepository, targetTag)).To(Succeed())
	})
}

func registerApplicationDeltaArtifactsCleanup(hints []v1beta1.ImageDeltaHint) {
	seen := make(map[string]struct{}, len(hints))
	for _, hint := range hints {
		if hint.DeltaImage == "" {
			continue
		}
		if _, ok := seen[hint.DeltaImage]; ok {
			continue
		}
		seen[hint.DeltaImage] = struct{}{}
		deltaImage := hint.DeltaImage
		DeferCleanup(func() {
			deleteApplicationDeltaArtifact(deltaImage)
		})
	}
}

func deleteApplicationDeltaArtifact(deltaImage string) {
	registry := applicationRegistryEndpoint()
	prefix := registry + "/"
	if !strings.HasPrefix(deltaImage, prefix) {
		Fail(fmt.Sprintf("generated delta image %q is not in the e2e delta registry %q", deltaImage, registry))
	}
	reference := strings.TrimPrefix(deltaImage, prefix)
	separator := strings.LastIndex(reference, "@")
	if separator <= 0 || separator == len(reference)-1 {
		Fail(fmt.Sprintf("generated delta image %q is not a digest reference", deltaImage))
	}
	repository, digest := reference[:separator], reference[separator+1:]
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := e2eRegistryHTTPClient()
	Expect(err).NotTo(HaveOccurred())
	registryURL := "https://" + auxSvcs.Registry.URL
	Expect(deleteRegistryTag(ctx, client, registryURL, repository, digest)).To(Succeed())
}

func assertNoDesiredApplicationDeltaHints(apps []v1beta1.ApplicationProviderSpec) {
	serialized, err := json.Marshal(apps)
	Expect(err).NotTo(HaveOccurred())
	Expect(strings.Contains(string(serialized), `"deltaImage`)).To(BeFalse(), "delta hints are generated by the control plane and must not be set in the desired application specs")
}

func applicationDeltaTargets(targets ...applicationDeltaTarget) []applicationDeltaGenerationTarget {
	seen := make(map[string]struct{}, len(targets))
	result := make([]applicationDeltaGenerationTarget, 0, len(targets))
	for _, target := range targets {
		if target.repository == "" || target.targetDigest == "" {
			continue
		}
		key := target.repository + "@" + target.targetDigest
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, applicationDeltaGenerationTarget{repository: target.repository, digest: target.targetDigest})
	}
	return result
}

func applicationRegistryEndpoint() string {
	return auxSvcs.Registry.Host + ":" + auxSvcs.Registry.Port
}

func applicationImageReference(registry, repository, tag string) string {
	return fmt.Sprintf("%s/%s:%s", registry, repository, tag)
}

func applicationImageRepository(image string) string {
	reference := strings.SplitN(image, "@", 2)[0]
	lastSlash := strings.LastIndex(reference, "/")
	lastColon := strings.LastIndex(reference, ":")
	if lastColon > lastSlash {
		return reference[:lastColon]
	}
	return reference
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
