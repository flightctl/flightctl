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

var _ = Describe("application delta Helm", Label("delta", "microshift", "slow", "helm"), Serial, func() {
	It("When a standalone device updates a Helm application it should apply its delta and remain healthy", Label("standalone"), func() {
		runHelmApplicationDeltaTest(false)
	})

	It("When a fleet updates a Helm application it should apply its delta and remain healthy", Label("fleet"), func() {
		runHelmApplicationDeltaTest(true)
	})
})

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

func runHelmApplicationDeltaTest(fleetOwned bool) {
	harness := e2e.GetWorkerHarness()

	By("enrolling a device and preparing the V12 MicroShift environment for Helm")
	deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
	prepareHelmApplicationDeltaDevice(harness, deviceID)
	createWritableDeltaRepo(harness)
	requireDeltaGenerationSupport(harness, deviceID)

	registry := applicationRegistryEndpoint()
	fleetName := ""
	if fleetOwned {
		fleetName = "delta-helm-" + strings.TrimPrefix(applicationDeltaArtifactTag(harness), "e2e-")
	}
	v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{}, applicationDeltaHelmAppNames)
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

	eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceID)
	Expect(err).NotTo(HaveOccurred())
	before := getDeltaDevice(harness, deviceID)

	By("building a full target image for the Helm chart update and leaving delta generation to the control plane")
	tagPrefix := applicationDeltaArtifactTag(harness)
	if fleetOwned {
		tagPrefix += "-fleet"
	}
	helmTarget, err := buildApplicationDeltaTargetImage(
		harness, registry, tagPrefix+"-helm", "flightctl-tests/alpine",
		applicationImageReference(registry, "flightctl-tests/alpine", "v1"),
	)
	Expect(err).NotTo(HaveOccurred())
	requireCRIImageAbsent(harness, helmTarget.image)
	v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
		Helm: &helmTarget,
	}, applicationDeltaHelmAppNames)
	Expect(err).NotTo(HaveOccurred())
	assertNoDesiredApplicationDeltaHints(v2Apps)
	agentLogSince, err := harness.JournalSinceFromPrimaryVM()
	Expect(err).NotTo(HaveOccurred())

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
	waitForApplicationDeltaGenerationEvents(harness, fleetName, deviceID, eventBaseline, applicationDeltaTargets(helmTarget)...)
	helmHint := waitForRenderedNestedDeltaHint(harness, deviceID, applicationDeltaHelmName, v1beta1.AppTypeHelm, helmTarget)
	registerApplicationDeltaArtifactsCleanup([]v1beta1.ImageDeltaHint{helmHint})

	By("checking Helm application health and delta application before device status")
	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, eventBaseline)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V2 Helm application")
	waitForApplicationDeltaOutcome(harness, deviceID, applicationDeltaHelmName, helmTarget, false)
	waitForApplicationDeltaAppliedLogs(harness, agentLogSince, []v1beta1.ImageDeltaHint{helmHint}, false)
	after := getDeltaDevice(harness, deviceID)
	Expect(after.Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta), "application updates must not change OS delta status")
}
