package delta

import (
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var applicationDeltaHelmAppNames = []string{applicationDeltaHelmName}

type helmDeltaImages struct {
	deployment    string
	sidecar       string
	initContainer string
	pod           string
	job           string
	cronJob       string
}

type helmDeltaTargets struct {
	deployment    applicationDeltaTarget
	sidecar       applicationDeltaTarget
	initContainer applicationDeltaTarget
	pod           applicationDeltaTarget
	job           applicationDeltaTarget
	cronJob       applicationDeltaTarget
}

func (targets helmDeltaTargets) all() []applicationDeltaTarget {
	return []applicationDeltaTarget{
		targets.deployment,
		targets.sidecar,
		targets.initContainer,
		targets.pod,
		targets.job,
		targets.cronJob,
	}
}

// podBackedWorkloads excludes the suspended CronJob, which has no Pod status.
func (targets helmDeltaTargets) podBackedWorkloads() []applicationDeltaTarget {
	return []applicationDeltaTarget{
		targets.deployment,
		targets.sidecar,
		targets.initContainer,
		targets.pod,
		targets.job,
	}
}

func (targets helmDeltaTargets) images() helmDeltaImages {
	return helmDeltaImages{
		deployment:    targets.deployment.image,
		sidecar:       targets.sidecar.image,
		initContainer: targets.initContainer.image,
		pod:           targets.pod.image,
		job:           targets.job.image,
		cronJob:       targets.cronJob.image,
	}
}

func prepareHelmApplicationDeltaDevice(harness *e2e.Harness, deviceID string) {
	waitDeviceUpToDate(harness, deviceID, "device UpToDate before MicroShift migration")
	v12Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V12)
	Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
		device.Spec.Os = &v1beta1.DeviceOsSpec{Image: v12Image}
	})).To(Succeed())
	Expect(harness.EnsureMicroshiftConfigs()).To(Succeed())
	Expect(harness.WaitForMicroshiftReady(e2e.MicroshiftKubeconfigPath)).To(Succeed())
	waitDeviceUpToDate(harness, deviceID, "device UpToDate on the MicroShift-capable V12 OS")
}

var _ = Describe("application delta Helm", Label("delta", "microshift", "slow", "helm"), Serial, func() {
	It("When a standalone device updates a Helm application with multiple images it should apply each delta and remain healthy", Label("standalone"), func() {
		runHelmApplicationDeltaTest(false)
	})

	It("When a fleet updates a Helm application with multiple images it should apply each delta and remain healthy", Label("fleet"), func() {
		runHelmApplicationDeltaTest(true)
	})
})

func runHelmApplicationDeltaTest(fleetOwned bool) {
	harness := e2e.GetWorkerHarness()

	By("enrolling a device and preparing the V12 MicroShift environment for Helm")
	deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
	prepareHelmApplicationDeltaDevice(harness, deviceID)
	createWritableDeltaRepo(harness)

	registry := applicationRegistryEndpoint()
	fleetName := ""
	if fleetOwned {
		fleetName = "delta-helm-" + strings.TrimPrefix(applicationDeltaArtifactTag(harness), "e2e-")
	}
	sourceImages := helmDeltaImages{
		deployment:    applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		sidecar:       applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
		initContainer: applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		pod:           applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		job:           applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
		cronJob:       applicationImageReference(registry, "flightctl-tests/nginx", "v1"),
	}
	v1HelmValues, err := helmDeltaChartValues(sourceImages)
	Expect(err).NotTo(HaveOccurred())
	v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{
		HelmValues: v1HelmValues,
	}, applicationDeltaHelmAppNames)
	Expect(err).NotTo(HaveOccurred())
	if fleetOwned {
		By("applying the V1 Helm application through fleet ownership")
		v1FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v1Apps, util.DeviceTags.V12)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v1FleetSpec)).To(Succeed())
		attachDeviceToApplicationDeltaFleet(harness, deviceID, fleetName)
	} else {
		By("installing the V1 Helm application on a standalone device")
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v1Apps
		})).To(Succeed())
	}
	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 Helm application")
	requireDeltaGenerationSupport(harness, deviceID)

	eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceID)
	Expect(err).NotTo(HaveOccurred())
	before := getDeltaDevice(harness, deviceID)

	By("building a full target image for each Helm workload image reference")
	tagPrefix := applicationDeltaArtifactTag(harness)
	if fleetOwned {
		tagPrefix += "-fleet"
	}
	deploymentTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm-deployment", "flightctl-tests/alpine", sourceImages.deployment,
	)
	Expect(err).NotTo(HaveOccurred())
	sidecarTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm-sidecar", "flightctl-tests/nginx", sourceImages.sidecar,
	)
	Expect(err).NotTo(HaveOccurred())
	initContainerTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm-init", "flightctl-tests/alpine", sourceImages.initContainer,
	)
	Expect(err).NotTo(HaveOccurred())
	podTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm-pod", "flightctl-tests/alpine", sourceImages.pod,
	)
	Expect(err).NotTo(HaveOccurred())
	jobTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm-job", "flightctl-tests/alpine", sourceImages.job,
	)
	Expect(err).NotTo(HaveOccurred())
	cronJobTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm-cronjob", "flightctl-tests/nginx", sourceImages.cronJob,
	)
	Expect(err).NotTo(HaveOccurred())
	helmTargets := helmDeltaTargets{
		deployment:    deploymentTarget,
		sidecar:       sidecarTarget,
		initContainer: initContainerTarget,
		pod:           podTarget,
		job:           jobTarget,
		cronJob:       cronJobTarget,
	}
	By("checking every new workload image is absent from CRI before delta prefetch")
	for _, target := range helmTargets.all() {
		requireCRIImageAbsent(harness, target.image)
	}
	v2HelmValues, err := helmDeltaChartValues(helmTargets.images())
	Expect(err).NotTo(HaveOccurred())
	v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
		HelmValues: v2HelmValues,
	}, applicationDeltaHelmAppNames)
	Expect(err).NotTo(HaveOccurred())
	assertNoDesiredApplicationDeltaHints(v2Apps)

	if fleetOwned {
		By("updating the fleet-owned Helm application")
		v2FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v2Apps, util.DeviceTags.V12)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v2FleetSpec)).To(Succeed())
	} else {
		By("updating the standalone Helm application")
		Expect(harness.UpdateDeviceWithRetries(deviceID, func(device *v1beta1.Device) {
			device.Spec.Applications = &v2Apps
		})).To(Succeed())
	}
	waitForApplicationDeltaGenerationEvents(harness, fleetName, deviceID, eventBaseline, applicationDeltaTargets(helmTargets.all()...)...)
	helmHints := waitForRenderedNestedDeltaHints(harness, deviceID, applicationDeltaHelmName, v1beta1.AppTypeHelm, helmTargets.all())
	registerApplicationDeltaArtifactsCleanup(helmHints)

	By("checking all rendered Helm image hints and reported application delta outcome")
	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, eventBaseline)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V2 Helm application")
	for _, target := range helmTargets.podBackedWorkloads() {
		waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaHelmName, target, v1beta1.DeviceDeltaApplyOutcomeApplied)
	}
	for _, target := range helmTargets.all() {
		requireCRIImagePresent(harness, target.image)
	}
	after := getDeltaDevice(harness, deviceID)
	Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
}

func helmDeltaChartValues(images helmDeltaImages) (map[string]any, error) {
	values, err := helmImageValues(images.deployment)
	if err != nil {
		return nil, err
	}
	values["sidecar"] = map[string]any{"enabled": true, "image": images.sidecar}
	values["initContainer"] = map[string]any{"enabled": true, "image": images.initContainer}
	values["standalonePod"] = map[string]any{"enabled": true, "image": images.pod}
	values["job"] = map[string]any{"enabled": true, "image": images.job}
	values["cronJob"] = map[string]any{"enabled": true, "image": images.cronJob}
	return values, nil
}
