package delta

import (
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
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

// all returns every Helm workload image reference.
func (images helmDeltaImages) all() []string {
	return []string{
		images.deployment,
		images.sidecar,
		images.initContainer,
		images.pod,
		images.job,
		images.cronJob,
	}
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

// helmDeltaSourceImages returns the V1 image fixture for every Helm workload.
func helmDeltaSourceImages(registry string) helmDeltaImages {
	return helmDeltaImages{
		deployment:    applicationImageReference(registry, applicationDeltaAlpineRepo, "v1"),
		sidecar:       applicationImageReference(registry, applicationDeltaNginxRepo, "v1"),
		initContainer: applicationImageReference(registry, applicationDeltaAlpineRepo, "v1"),
		pod:           applicationImageReference(registry, applicationDeltaAlpineRepo, "v1"),
		job:           applicationImageReference(registry, applicationDeltaAlpineRepo, "v1"),
		cronJob:       applicationImageReference(registry, applicationDeltaNginxRepo, "v1"),
	}
}

var _ = Describe("application delta Helm", Label("delta", "microshift", "slow", "helm"), Serial, func() {
	It("When a standalone device updates a Helm application with multiple images it should apply each delta and remain healthy", Label("standalone"), func() {
		runHelmApplicationDeltaTest(false)
	})

	It("When a fleet updates a Helm application with multiple images it should apply each delta and remain healthy", Label("fleet"), func() {
		runHelmApplicationDeltaTest(true)
	})

	It("When a standalone device updates a Helm application with externally published deltas it should discover and apply them without generation", Label("standalone", "discovery"), func() {
		runHelmApplicationDeltaDiscoveryTest(false)
	})

	It("When a fleet updates a Helm application with externally published deltas it should discover and apply them without generation", Label("fleet", "discovery"), func() {
		runHelmApplicationDeltaDiscoveryTest(true)
	})

	It("When a discovered Helm delta is unusable it should full-pull the digest-pinned CRI image", Label("standalone", "discovery", "fallback"), func() {
		fixture, err := prepareHelmApplicationDeltaDiscoverySelectionTest(helmDiscoverySelectionFallback)
		Expect(err).NotTo(HaveOccurred())
		rendered, err := tryRenderedDevice(fixture.harness, fixture.deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Spec).NotTo(BeNil())
		Expect(rendered.Spec.Applications).NotTo(BeNil())
		Expect(assertNoRenderedApplicationDeltaHints(*rendered.Spec.Applications)).To(Succeed())
		expectNoNewApplicationDeltaGenerationEvent(
			fixture.harness, deviceDeltaGenerationEventSource(fixture.deviceID, fixture.baseline), fixture.target,
		)
		Eventually(discoveredApplicationDeltaOutcome(
			fixture.harness, fixture.deviceID, applicationDeltaHelmName,
			[]applicationDeltaTarget{fixture.target}, fixture.expectedOutcome,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		requireCRIImagePresent(fixture.harness, fixture.pinnedTarget)
		Expect(getDeltaDevice(fixture.harness, fixture.deviceID).Status.Os.LastDelta).To(Equal(fixture.before.Status.Os.LastDelta))
	})

	It("When discovery finds multiple usable Helm deltas it should select the smallest", Label("standalone", "discovery", "ranking"), func() {
		fixture, err := prepareHelmApplicationDeltaDiscoverySelectionTest(helmDiscoverySelectionRanking)
		Expect(err).NotTo(HaveOccurred())
		rendered, err := tryRenderedDevice(fixture.harness, fixture.deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Spec).NotTo(BeNil())
		Expect(rendered.Spec.Applications).NotTo(BeNil())
		Expect(assertNoRenderedApplicationDeltaHints(*rendered.Spec.Applications)).To(Succeed())
		expectNoNewApplicationDeltaGenerationEvent(
			fixture.harness, deviceDeltaGenerationEventSource(fixture.deviceID, fixture.baseline), fixture.target,
		)
		Eventually(discoveredApplicationDeltaOutcome(
			fixture.harness, fixture.deviceID, applicationDeltaHelmName,
			[]applicationDeltaTarget{fixture.target}, fixture.expectedOutcome,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		requireCRIImagePresent(fixture.harness, fixture.pinnedTarget)
		Eventually(agentAppliedDiscoveredDelta(
			fixture.harness, fixture.target, fixture.expectedDelta,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Expect(getDeltaDevice(fixture.harness, fixture.deviceID).Status.Os.LastDelta).To(Equal(fixture.before.Status.Os.LastDelta))
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
	sourceImages := helmDeltaSourceImages(registry)
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

	tagPrefix := applicationDeltaArtifactTag(harness)
	if fleetOwned {
		tagPrefix += "-fleet"
	}
	By("building a full target image for each Helm workload image reference")
	helmTargets, err := buildHelmDeltaTargets(harness, registry, tagPrefix, sourceImages)
	Expect(err).NotTo(HaveOccurred())
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

// runHelmApplicationDeltaDiscoveryTest exercises agent-side discovery for a Helm application.
func runHelmApplicationDeltaDiscoveryTest(fleetOwned bool) {
	harness := e2e.GetWorkerHarness()

	By("enrolling a device and preparing MicroShift without a writable delta storage target")
	deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
	prepareHelmApplicationDeltaDevice(harness, deviceID)

	registry := applicationRegistryEndpoint()
	fleetName := ""
	if fleetOwned {
		fleetName = "delta-helm-discovery-" + strings.TrimPrefix(applicationDeltaArtifactTag(harness), "e2e-")
	}
	sourceImages := helmDeltaSourceImages(registry)
	v1HelmValues, err := helmDeltaChartValues(sourceImages)
	Expect(err).NotTo(HaveOccurred())
	v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{
		HelmValues: v1HelmValues,
	}, applicationDeltaHelmAppNames)
	Expect(err).NotTo(HaveOccurred())
	if fleetOwned {
		By("applying the V1 Helm application through a fleet with generation disabled")
		v1FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v1Apps, util.DeviceTags.V12)
		v1FleetSpec.RolloutPolicy.DeltaGeneration.GenerateDelta = lo.ToPtr(false)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v1FleetSpec)).To(Succeed())
		attachDeviceToApplicationDeltaFleet(harness, deviceID, fleetName)
	} else {
		By("installing the V1 Helm application on a standalone device")
		v1Update := deviceApplicationUpdate{applications: v1Apps}
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, v1Update.apply)).To(Succeed())
	}
	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 Helm application")
	requireDeltaGenerationSupport(harness, deviceID)

	baseline, err := captureDeltaEventBaseline(harness, fleetName, deviceID)
	Expect(err).NotTo(HaveOccurred())
	before := getDeltaDevice(harness, deviceID)
	tagPrefix := applicationDeltaArtifactTag(harness) + "-discovery"
	if fleetOwned {
		tagPrefix += "-fleet"
	}
	helmTargets, err := buildHelmDeltaTargets(harness, registry, tagPrefix, sourceImages)
	Expect(err).NotTo(HaveOccurred())
	v2Images := helmTargets.images()
	pinnedDeployment, err := applicationImageDigestReference(helmTargets.deployment.image, helmTargets.deployment.targetDigest)
	Expect(err).NotTo(HaveOccurred())
	v2Images.deployment = pinnedDeployment
	By("checking every new Helm workload image is absent from CRI before delta prefetch")
	for _, image := range v2Images.all() {
		requireCRIImageAbsent(harness, image)
	}

	By("publishing one externally generated delta referrer for each Helm workload image")
	discoveredTargets := []struct {
		name        string
		sourceImage string
		target      applicationDeltaTarget
	}{
		{name: "deployment", sourceImage: sourceImages.deployment, target: helmTargets.deployment},
		{name: "sidecar", sourceImage: sourceImages.sidecar, target: helmTargets.sidecar},
		{name: "init", sourceImage: sourceImages.initContainer, target: helmTargets.initContainer},
		{name: "pod", sourceImage: sourceImages.pod, target: helmTargets.pod},
		{name: "job", sourceImage: sourceImages.job, target: helmTargets.job},
		{name: "cronjob", sourceImage: sourceImages.cronJob, target: helmTargets.cronJob},
	}
	for _, discovered := range discoveredTargets {
		sourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaHelmName, discovered.sourceImage)
		Expect(err).NotTo(HaveOccurred())
		_, err = publishDiscoveredDeltaForTarget(
			harness, registry, tagPrefix+"-"+discovered.name, discovered.sourceImage, sourceDigest, discovered.target,
		)
		Expect(err).NotTo(HaveOccurred())
		Eventually(discoveredDeltaReferrer(
			harness, registry, discovered.target.repository, sourceDigest, discovered.target.targetDigest,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
	}

	v2HelmValues, err := helmDeltaChartValues(v2Images)
	Expect(err).NotTo(HaveOccurred())
	v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
		HelmValues: v2HelmValues,
	}, applicationDeltaHelmAppNames)
	Expect(err).NotTo(HaveOccurred())
	assertNoDesiredApplicationDeltaHints(v2Apps)
	if fleetOwned {
		By("updating the fleet-owned Helm application with generation disabled")
		v2FleetSpec := applicationDeltaFleetSpec(harness, fleetName, v2Apps, util.DeviceTags.V12)
		v2FleetSpec.RolloutPolicy.DeltaGeneration.GenerateDelta = lo.ToPtr(false)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, v2FleetSpec)).To(Succeed())
	} else {
		By("updating the standalone Helm application without a delta hint")
		v2Update := deviceApplicationUpdate{applications: v2Apps}
		Expect(harness.UpdateDeviceWithRetries(deviceID, v2Update.apply)).To(Succeed())
	}

	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, baseline)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V2 Helm application")
	rendered, err := tryRenderedDevice(harness, deviceID)
	Expect(err).NotTo(HaveOccurred())
	Expect(rendered.Spec).NotTo(BeNil())
	Expect(rendered.Spec.Applications).NotTo(BeNil())
	Expect(assertNoRenderedApplicationDeltaHints(*rendered.Spec.Applications)).To(Succeed())
	eventSource := deviceDeltaGenerationEventSource(deviceID, baseline)
	if fleetOwned {
		eventSource = fleetDeltaGenerationEventSource(fleetName, baseline)
	}
	for _, target := range helmTargets.all() {
		expectNoNewApplicationDeltaGenerationEvent(harness, eventSource, target)
	}
	for _, target := range helmTargets.podBackedWorkloads() {
		if target.image == helmTargets.deployment.image {
			target.image = v2Images.deployment
		}
		Eventually(discoveredApplicationDeltaOutcome(
			harness, deviceID, applicationDeltaHelmName, []applicationDeltaTarget{target}, v1beta1.DeviceDeltaApplyOutcomeApplied,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
	}
	for _, image := range v2Images.all() {
		requireCRIImagePresent(harness, image)
	}
	Expect(getDeltaDevice(harness, deviceID).Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta))
}

type helmDiscoverySelectionMode int

const (
	helmDiscoverySelectionFallback helmDiscoverySelectionMode = iota
	helmDiscoverySelectionRanking
)

type helmDiscoverySelectionFixture struct {
	harness         *e2e.Harness
	deviceID        string
	baseline        deltaEventBaseline
	before          *v1beta1.Device
	target          applicationDeltaTarget
	pinnedTarget    string
	expectedDelta   discoveredDeltaArtifact
	expectedOutcome v1beta1.DeviceDeltaApplyOutcomeType
}

// prepareHelmApplicationDeltaDiscoverySelectionTest installs a Helm application and updates one digest-pinned image.
func prepareHelmApplicationDeltaDiscoverySelectionTest(mode helmDiscoverySelectionMode) (helmDiscoverySelectionFixture, error) {
	harness := e2e.GetWorkerHarness()
	fixture := helmDiscoverySelectionFixture{
		harness:         harness,
		expectedOutcome: v1beta1.DeviceDeltaApplyOutcomeApplied,
	}

	By("enrolling a device and preparing MicroShift without a writable delta storage target")
	deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
	fixture.deviceID = deviceID
	prepareHelmApplicationDeltaDevice(harness, deviceID)

	registry := applicationRegistryEndpoint()
	tagPrefix := applicationDeltaArtifactTag(harness) + "-helm-discovery-selection"
	sourceImage := applicationImageReference(registry, applicationDeltaAlpineRepo, "v1")
	target, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix+"-target", applicationDeltaAlpineRepo, sourceImage)
	if err != nil {
		return fixture, err
	}
	fixture.target = target
	pinnedTarget, err := applicationImageDigestReference(target.image, target.targetDigest)
	if err != nil {
		return fixture, err
	}
	fixture.pinnedTarget = pinnedTarget

	v1Images := helmDeltaImages{deployment: sourceImage}
	if mode == helmDiscoverySelectionRanking {
		By("building a closer local source for the ranking fixture")
		closerSource, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix+"-closer-source", applicationDeltaAlpineRepo, target.image)
		if err != nil {
			return fixture, err
		}
		v1Images.sidecar = closerSource.image
	}
	v1HelmValues, err := helmDeltaChartValues(v1Images)
	if err != nil {
		return fixture, err
	}
	v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{
		HelmValues: v1HelmValues,
	}, applicationDeltaHelmAppNames)
	if err != nil {
		return fixture, err
	}
	v1Update := deviceApplicationUpdate{applications: v1Apps}
	if err := harness.UpdateDeviceAndWaitForVersion(deviceID, v1Update.apply); err != nil {
		return fixture, err
	}
	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 focused Helm application")
	requireDeltaGenerationSupport(harness, deviceID)

	fixture.baseline, err = captureDeltaEventBaseline(harness, "", deviceID)
	if err != nil {
		return fixture, err
	}
	fixture.before = getDeltaDevice(harness, deviceID)
	requireCRIImageAbsent(harness, pinnedTarget)

	sourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaHelmName, sourceImage)
	if err != nil {
		return fixture, err
	}
	if mode == helmDiscoverySelectionFallback {
		By("publishing and corrupting a Helm delta referrer")
		fixture.expectedOutcome = v1beta1.DeviceDeltaApplyOutcomeFallback
		artifact, err := publishDiscoveredDeltaForTarget(harness, registry, tagPrefix+"-fallback", sourceImage, sourceDigest, target)
		if err != nil {
			return fixture, err
		}
		if err := deleteDiscoveredDeltaLayer(target.repository, artifact); err != nil {
			return fixture, err
		}
	} else {
		By("publishing two usable Helm delta referrers")
		closerSourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaHelmName, v1Images.sidecar)
		if err != nil {
			return fixture, err
		}
		fartherDelta, err := publishDiscoveredDeltaForTarget(harness, registry, tagPrefix+"-farther", sourceImage, sourceDigest, target)
		if err != nil {
			return fixture, err
		}
		closerDelta, err := publishDiscoveredDeltaForTarget(harness, registry, tagPrefix+"-closer", v1Images.sidecar, closerSourceDigest, target)
		if err != nil {
			return fixture, err
		}
		fixture.expectedDelta, err = expectedDiscoveredDelta(target.repository, fartherDelta, closerDelta)
		if err != nil {
			return fixture, err
		}
	}

	v2Images := helmDeltaImages{deployment: pinnedTarget, sidecar: v1Images.sidecar}
	v2HelmValues, err := helmDeltaChartValues(v2Images)
	if err != nil {
		return fixture, err
	}
	v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
		HelmValues: v2HelmValues,
	}, applicationDeltaHelmAppNames)
	if err != nil {
		return fixture, err
	}
	assertNoDesiredApplicationDeltaHints(v2Apps)
	v2Update := deviceApplicationUpdate{applications: v2Apps}
	if err := harness.UpdateDeviceWithRetries(deviceID, v2Update.apply); err != nil {
		return fixture, err
	}
	waitForApplicationDeltaApps(harness, deviceID, applicationDeltaHelmAppNames)
	waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, fixture.baseline)
	waitDeviceUpToDate(harness, deviceID, "device UpToDate with the focused V2 Helm application")
	fixture.target.image = pinnedTarget
	return fixture, nil
}

// buildHelmDeltaTargets builds one full target image for each Helm workload image.
func buildHelmDeltaTargets(harness *e2e.Harness, registry, tagPrefix string, sourceImages helmDeltaImages) (helmDeltaTargets, error) {
	targets := helmDeltaTargets{}
	definitions := []struct {
		name        string
		repository  string
		sourceImage string
		target      *applicationDeltaTarget
	}{
		{name: "deployment", repository: applicationDeltaAlpineRepo, sourceImage: sourceImages.deployment, target: &targets.deployment},
		{name: "sidecar", repository: applicationDeltaNginxRepo, sourceImage: sourceImages.sidecar, target: &targets.sidecar},
		{name: "init", repository: applicationDeltaAlpineRepo, sourceImage: sourceImages.initContainer, target: &targets.initContainer},
		{name: "pod", repository: applicationDeltaAlpineRepo, sourceImage: sourceImages.pod, target: &targets.pod},
		{name: "job", repository: applicationDeltaAlpineRepo, sourceImage: sourceImages.job, target: &targets.job},
		{name: "cronjob", repository: applicationDeltaNginxRepo, sourceImage: sourceImages.cronJob, target: &targets.cronJob},
	}
	for _, definition := range definitions {
		target, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-helm-"+definition.name, definition.repository, definition.sourceImage,
		)
		if err != nil {
			return helmDeltaTargets{}, err
		}
		*definition.target = target
	}
	return targets, nil
}

// helmDeltaChartValues renders chart values for the supplied Helm workload images.
func helmDeltaChartValues(images helmDeltaImages) (map[string]any, error) {
	values, err := helmImageValues(images.deployment)
	if err != nil {
		return nil, err
	}
	if images.sidecar != "" {
		values["sidecar"] = map[string]any{"enabled": true, "image": images.sidecar}
	}
	if images.initContainer != "" {
		values["initContainer"] = map[string]any{"enabled": true, "image": images.initContainer}
	}
	if images.pod != "" {
		values["standalonePod"] = map[string]any{"enabled": true, "image": images.pod}
	}
	if images.job != "" {
		values["job"] = map[string]any{"enabled": true, "image": images.job}
	}
	if images.cronJob != "" {
		values["cronJob"] = map[string]any{"enabled": true, "image": images.cronJob}
	}
	return values, nil
}
