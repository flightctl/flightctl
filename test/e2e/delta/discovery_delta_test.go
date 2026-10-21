package delta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	discoveredDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	discoveredDeltaSourceAnnotation = "io.github.containers.delta.source"
)

type discoveredDeltaArtifact struct {
	reference string
	digest    string
}

var _ = Describe("discovered delta", Label("delta", "discovery", "slow", "vm"), Serial, func() {
	It("When a fleet OS image changes with an externally published delta it should discover and apply it without generation", Label("os"), func() {
		harness := e2e.GetWorkerHarness()
		policy := &v1beta1.RolloutPolicy{
			DeltaGeneration: &v1beta1.RolloutPolicyDeltaGeneration{GenerateDelta: lo.ToPtr(false)},
		}

		By("enrolling a fleet device with OS delta generation disabled")
		fleetName := "delta-os-discovery"
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, policy))).To(Succeed())
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceID, "device UpToDate on current OS")
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		device := getDeltaDevice(harness, deviceID)
		sourceDigest := device.Status.Os.ImageDigest
		Expect(sourceDigest).NotTo(BeEmpty(), "the device must report its booted OS image digest")
		targetDigest, err := resolveApplicationImageDigest(harness, registry, util.DeviceImageRegistryPath, util.DeviceTags.V2)
		Expect(err).NotTo(HaveOccurred())
		sourceRef := fmt.Sprintf("%s/%s@%s", registry, util.DeviceImageRegistryPath, sourceDigest)
		targetRef := fmt.Sprintf("%s/%s@%s", registry, util.DeviceImageRegistryPath, targetDigest)
		_, err = publishDiscoveredDelta(harness, registry, applicationDeltaArtifactTag(harness)+"-os", util.DeviceImageRegistryPath, sourceRef, targetRef)
		Expect(err).NotTo(HaveOccurred())
		Eventually(discoveredDeltaReferrer(harness, registry, util.DeviceImageRegistryPath, sourceDigest, targetDigest),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())

		By("updating the fleet while keeping generation disabled")
		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		baseline, err := captureDeltaEventBaseline(harness, fleetName, deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, policy))).To(Succeed())

		waitForRolloutWithoutDeltaPreparing(harness, fleetName, deviceID, v2Image, baseline,
			"device UpToDate after applying a discovered OS delta", LONGTIMEOUT)
		Eventually(discoveredOSDeltaOutcome(harness, deviceID, targetDigest), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
	})

	It("When a standalone device updates container, Compose, Quadlet, and VM applications with externally published deltas it should discover and apply them without generation", Label("application", "standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device without configuring a delta storage target")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		tagPrefix := applicationDeltaArtifactTag(harness) + "-discovery"
		quadletVolumeSource, err := copyApplicationImageToRegistry(
			harness, registry, tagPrefix+"-quadlet-volume-v1", applicationDeltaQuadletVolumeRepo,
			applicationImageReference(registry, applicationDeltaAlpineRepo, "v1"),
		)
		Expect(err).NotTo(HaveOccurred())
		vmSource, err := copyApplicationImageToRegistry(
			harness, registry, tagPrefix+"-vm-v1", "containerdisks/fedora", "quay.io/containerdisks/fedora:40",
		)
		Expect(err).NotTo(HaveOccurred())
		v1Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV1, applicationDeltaOverrides{
			QuadletVolume: &quadletVolumeSource,
			VM:            &vmSource,
		}, applicationDeltaNonHelmAppNames)
		Expect(err).NotTo(HaveOccurred())
		v1Update := deviceApplicationUpdate{applications: v1Apps}
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, v1Update.apply)).To(Succeed())
		waitForApplicationDeltaApps(harness, deviceID, applicationDeltaNonHelmAppNames)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the four non-Helm V1 applications")

		By("building or copying each full target image while capturing locally reported source digests")
		nginxSource := applicationImageReference(registry, applicationDeltaNginxRepo, "v1")
		alpineSource := applicationImageReference(registry, applicationDeltaAlpineRepo, "v1")
		nginxSourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaContainerName, nginxSource)
		Expect(err).NotTo(HaveOccurred())
		alpineSourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaComposeName, alpineSource)
		Expect(err).NotTo(HaveOccurred())
		quadletVolumeSourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaQuadletName, quadletVolumeSource.image)
		Expect(err).NotTo(HaveOccurred())
		vmSourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaVMName, vmSource.image)
		Expect(err).NotTo(HaveOccurred())

		nginxTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-nginx-v2", applicationDeltaNginxRepo, nginxSource,
		)
		Expect(err).NotTo(HaveOccurred())
		alpineTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-alpine-v2", applicationDeltaAlpineRepo, alpineSource,
		)
		Expect(err).NotTo(HaveOccurred())
		quadletVolumeTarget, err := buildApplicationDeltaTargetImage(
			harness, registry, tagPrefix+"-quadlet-volume-v2", applicationDeltaQuadletVolumeRepo,
			quadletVolumeSource.image,
		)
		Expect(err).NotTo(HaveOccurred())
		vmTarget, err := copyApplicationImageToRegistry(
			harness, registry, tagPrefix+"-vm-v2", "containerdisks/fedora", applicationDeltaVMImageV2,
		)
		Expect(err).NotTo(HaveOccurred())
		allTargets := []applicationDeltaTarget{nginxTarget, alpineTarget, quadletVolumeTarget, vmTarget}
		for _, target := range allTargets {
			requireDeviceImageAbsent(harness, target.image)
		}

		By("publishing one externally generated delta referrer for each image target")
		publishedTargets := []struct {
			tag          string
			sourceImage  string
			sourceDigest string
			target       applicationDeltaTarget
		}{
			{tag: tagPrefix + "-nginx", sourceImage: nginxSource, sourceDigest: nginxSourceDigest, target: nginxTarget},
			{tag: tagPrefix + "-alpine", sourceImage: alpineSource, sourceDigest: alpineSourceDigest, target: alpineTarget},
			{tag: tagPrefix + "-quadlet-volume", sourceImage: quadletVolumeSource.image, sourceDigest: quadletVolumeSourceDigest, target: quadletVolumeTarget},
			{tag: tagPrefix + "-vm", sourceImage: vmSource.image, sourceDigest: vmSourceDigest, target: vmTarget},
		}
		for _, published := range publishedTargets {
			_, err := publishDiscoveredDeltaForTarget(harness, registry, published.tag, published.sourceImage, published.sourceDigest, published.target)
			Expect(err).NotTo(HaveOccurred())
			Eventually(discoveredDeltaReferrer(harness, registry, published.target.repository, published.sourceDigest, published.target.targetDigest),
				util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		}

		By("updating the four non-Helm applications without rendered delta hints")
		before := getDeltaDevice(harness, deviceID)
		baseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		v2Apps, err := applicationDeltaSpecsForNames(registry, applicationDeltaVersionV2, applicationDeltaOverrides{
			Container:     &nginxTarget,
			Compose:       []applicationDeltaTarget{nginxTarget, alpineTarget},
			Quadlet:       &nginxTarget,
			QuadletImage:  &alpineTarget,
			QuadletVolume: &quadletVolumeTarget,
			VM:            &vmTarget,
		}, applicationDeltaNonHelmAppNames)
		Expect(err).NotTo(HaveOccurred())
		assertNoDesiredApplicationDeltaHints(v2Apps)
		v2Update := deviceApplicationUpdate{applications: v2Apps}
		Expect(harness.UpdateDeviceWithRetries(deviceID, v2Update.apply)).To(Succeed())

		waitForApplicationDeltaApps(harness, deviceID, applicationDeltaNonHelmAppNames)
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, baseline)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate after applying discovered application deltas")
		rendered, err := tryRenderedDevice(harness, deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Spec).NotTo(BeNil())
		Expect(rendered.Spec.Applications).NotTo(BeNil())
		Expect(assertNoRenderedApplicationDeltaHints(*rendered.Spec.Applications)).To(Succeed())
		for _, target := range allTargets {
			expectNoNewApplicationDeltaGenerationEvent(harness, deviceDeltaGenerationEventSource(deviceID, baseline), target)
		}
		Eventually(discoveredApplicationDeltaOutcome(harness, deviceID, applicationDeltaContainerName, []applicationDeltaTarget{nginxTarget}, v1beta1.DeviceDeltaApplyOutcomeApplied),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Eventually(discoveredApplicationDeltaOutcome(harness, deviceID, applicationDeltaComposeName, []applicationDeltaTarget{nginxTarget, alpineTarget}, v1beta1.DeviceDeltaApplyOutcomeApplied),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Eventually(discoveredApplicationDeltaOutcome(harness, deviceID, applicationDeltaQuadletName, []applicationDeltaTarget{nginxTarget, alpineTarget, quadletVolumeTarget}, v1beta1.DeviceDeltaApplyOutcomeApplied),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Eventually(discoveredApplicationDeltaOutcome(harness, deviceID, applicationDeltaVMName, []applicationDeltaTarget{vmTarget}, v1beta1.DeviceDeltaApplyOutcomeApplied),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Expect(getDeltaDevice(harness, deviceID).Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta))
	})

	It("When a discovered application delta is unusable it should full-pull and report the fallback", Label("application", "fallback", "standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device without a delta storage target")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		sourceImage := applicationImageReference(registry, applicationDeltaNginxRepo, "v1")
		containerV1, err := e2e.NewContainerApplicationSpec(applicationDeltaContainerName, sourceImage, nil, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		v1Apps := []v1beta1.ApplicationProviderSpec{containerV1}
		v1Update := deviceApplicationUpdate{applications: v1Apps}
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, v1Update.apply)).To(Succeed())
		waitForApplicationDeltaApps(harness, deviceID, []string{applicationDeltaContainerName})
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 container application")

		sourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaContainerName, sourceImage)
		Expect(err).NotTo(HaveOccurred())
		tagPrefix := applicationDeltaArtifactTag(harness) + "-discovery-fallback"
		target, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix, applicationDeltaNginxRepo, sourceImage)
		Expect(err).NotTo(HaveOccurred())
		requireDeviceImageAbsent(harness, target.image)

		By("publishing a delta referrer and deleting one of its layers")
		artifact, err := publishDiscoveredDeltaForTarget(harness, registry, tagPrefix, sourceImage, sourceDigest, target)
		Expect(err).NotTo(HaveOccurred())
		Eventually(discoveredDeltaReferrer(harness, registry, target.repository, sourceDigest, target.targetDigest),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Expect(deleteDiscoveredDeltaLayer(target.repository, artifact)).To(Succeed())
		Eventually(discoveredDeltaReferrer(harness, registry, target.repository, sourceDigest, target.targetDigest),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())

		By("updating the container without a rendered delta hint")
		before := getDeltaDevice(harness, deviceID)
		baseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		containerV2, err := e2e.NewContainerApplicationSpec(applicationDeltaContainerName, target.image, nil, nil, nil, nil)
		Expect(err).NotTo(HaveOccurred())
		v2Apps := []v1beta1.ApplicationProviderSpec{containerV2}
		assertNoDesiredApplicationDeltaHints(v2Apps)
		v2Update := deviceApplicationUpdate{applications: v2Apps}
		Expect(harness.UpdateDeviceWithRetries(deviceID, v2Update.apply)).To(Succeed())

		waitForApplicationDeltaApps(harness, deviceID, []string{applicationDeltaContainerName})
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, baseline)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate after a discovered delta fallback")
		rendered, err := tryRenderedDevice(harness, deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Spec).NotTo(BeNil())
		Expect(rendered.Spec.Applications).NotTo(BeNil())
		Expect(assertNoRenderedApplicationDeltaHints(*rendered.Spec.Applications)).To(Succeed())
		expectNoNewApplicationDeltaGenerationEvent(harness, deviceDeltaGenerationEventSource(deviceID, baseline), target)
		Eventually(discoveredApplicationDeltaOutcome(
			harness, deviceID, applicationDeltaContainerName, []applicationDeltaTarget{target}, v1beta1.DeviceDeltaApplyOutcomeFallback,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Expect(requireDeviceImagePresent(harness, target.image)).To(Succeed())
		Expect(getDeltaDevice(harness, deviceID).Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta))
	})

	It("When discovery finds multiple usable application deltas it should select the smallest", Label("application", "ranking", "standalone"), func() {
		harness := e2e.GetWorkerHarness()

		By("enrolling a standalone device with two locally available source images")
		deviceID, _ := harness.EnrollAndWaitForOnlineStatus()
		prepareNonHelmApplicationDeltaDevice(harness, deviceID)
		requireDeltaGenerationSupport(harness, deviceID)

		registry := applicationRegistryEndpoint()
		tagPrefix := applicationDeltaArtifactTag(harness) + "-discovery-ranking"
		sourceImage := applicationImageReference(registry, applicationDeltaNginxRepo, "v1")
		target, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix+"-target", applicationDeltaNginxRepo, sourceImage)
		Expect(err).NotTo(HaveOccurred())
		closerSource, err := buildApplicationDeltaTargetImage(harness, registry, tagPrefix+"-closer-source", applicationDeltaNginxRepo, target.image)
		Expect(err).NotTo(HaveOccurred())
		composeV1, err := applicationDeltaComposeSpec([]string{sourceImage, closerSource.image})
		Expect(err).NotTo(HaveOccurred())
		v1Apps := []v1beta1.ApplicationProviderSpec{composeV1}
		v1Update := deviceApplicationUpdate{applications: v1Apps}
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, v1Update.apply)).To(Succeed())
		waitForApplicationDeltaApps(harness, deviceID, []string{applicationDeltaComposeName})
		waitDeviceUpToDate(harness, deviceID, "device UpToDate with the V1 ranking sources")
		requireDeviceImageAbsent(harness, target.image)

		By("publishing two valid referrers for the same target")
		sourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaComposeName, sourceImage)
		Expect(err).NotTo(HaveOccurred())
		closerSourceDigest, err := reportedApplicationImageDigest(harness, deviceID, applicationDeltaComposeName, closerSource.image)
		Expect(err).NotTo(HaveOccurred())
		fartherDelta, err := publishDiscoveredDeltaForTarget(harness, registry, tagPrefix+"-farther", sourceImage, sourceDigest, target)
		Expect(err).NotTo(HaveOccurred())
		closerDelta, err := publishDiscoveredDeltaForTarget(harness, registry, tagPrefix+"-closer", closerSource.image, closerSourceDigest, target)
		Expect(err).NotTo(HaveOccurred())
		Eventually(discoveredDeltaReferrer(harness, registry, target.repository, sourceDigest, target.targetDigest),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Eventually(discoveredDeltaReferrer(harness, registry, target.repository, closerSourceDigest, target.targetDigest),
			util.LONG_TIMEOUT, util.POLLING).Should(Succeed())

		By("updating one Compose image while retaining both local sources")
		before := getDeltaDevice(harness, deviceID)
		baseline, err := captureDeltaEventBaseline(harness, "", deviceID)
		Expect(err).NotTo(HaveOccurred())
		composeV2, err := applicationDeltaComposeSpec([]string{target.image, closerSource.image})
		Expect(err).NotTo(HaveOccurred())
		v2Apps := []v1beta1.ApplicationProviderSpec{composeV2}
		assertNoDesiredApplicationDeltaHints(v2Apps)
		v2Update := deviceApplicationUpdate{applications: v2Apps}
		Expect(harness.UpdateDeviceWithRetries(deviceID, v2Update.apply)).To(Succeed())

		waitForApplicationDeltaApps(harness, deviceID, []string{applicationDeltaComposeName})
		waitForApplicationDeltaContentUpToDateEvent(harness, deviceID, baseline)
		waitDeviceUpToDate(harness, deviceID, "device UpToDate after selecting a discovered delta")
		rendered, err := tryRenderedDevice(harness, deviceID)
		Expect(err).NotTo(HaveOccurred())
		Expect(rendered.Spec).NotTo(BeNil())
		Expect(rendered.Spec.Applications).NotTo(BeNil())
		Expect(assertNoRenderedApplicationDeltaHints(*rendered.Spec.Applications)).To(Succeed())
		expectNoNewApplicationDeltaGenerationEvent(harness, deviceDeltaGenerationEventSource(deviceID, baseline), target)
		Eventually(discoveredApplicationDeltaOutcome(
			harness, deviceID, applicationDeltaComposeName, []applicationDeltaTarget{target}, v1beta1.DeviceDeltaApplyOutcomeApplied,
		), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		expectedDelta, err := expectedDiscoveredDelta(target.repository, fartherDelta, closerDelta)
		Expect(err).NotTo(HaveOccurred())
		Eventually(agentAppliedDiscoveredDelta(harness, target, expectedDelta), util.LONG_TIMEOUT, util.POLLING).Should(Succeed())
		Expect(getDeltaDevice(harness, deviceID).Status.Os.LastDelta).To(Equal(before.Status.Os.LastDelta))
	})
})

// publishDiscoveredDelta creates and publishes an externally generated OCI delta.
func publishDiscoveredDelta(harness *e2e.Harness, registry, tag, targetRepository, sourceRef, targetRef string) (discoveredDeltaArtifact, error) {
	targetTag := applicationDeltaTargetTag(tag)
	registerApplicationImageCleanup(targetRepository, targetTag)
	deltaImage := applicationImageReference(registry, targetRepository, targetTag)
	workDir := "/tmp/flightctl-delta-discovery-" + tag
	commands := []string{
		"set -eu",
		fmt.Sprintf("work=%s", shellQuote(workDir)),
		fmt.Sprintf("delta=%s", shellQuote(deltaImage)),
		"rm -rf \"$work\"",
		"mkdir -p \"$work/source\" \"$work/target\" \"$work/delta\"",
		"trap 'rm -rf \"$work\"' EXIT",
		fmt.Sprintf("skopeo copy --src-tls-verify=false %s oci:$work/source:img",
			shellQuote("docker://"+sourceRef)),
		fmt.Sprintf("skopeo copy --src-tls-verify=false %s oci:$work/target:img",
			shellQuote("docker://"+targetRef)),
		"oci-delta create oci:$work/source:img oci:$work/target:img oci:$work/delta:img",
		fmt.Sprintf("skopeo copy --preserve-digests --dest-tls-verify=false oci:$work/delta:img %s",
			shellQuote("docker://"+deltaImage)),
	}
	_, err := harness.RunShellAsUserOnVM("root", strings.Join(commands, "\n"))
	if err != nil {
		return discoveredDeltaArtifact{}, fmt.Errorf("publish externally generated delta %s: %w", deltaImage, err)
	}
	deltaDigest, err := resolveApplicationImageDigest(harness, registry, targetRepository, targetTag)
	if err != nil {
		return discoveredDeltaArtifact{}, fmt.Errorf("resolve externally generated delta %s: %w", deltaImage, err)
	}
	reference, err := applicationImageDigestReference(deltaImage, deltaDigest)
	if err != nil {
		return discoveredDeltaArtifact{}, err
	}
	return discoveredDeltaArtifact{reference: reference, digest: deltaDigest}, nil
}

// publishDiscoveredDeltaForTarget creates a delta from sourceImage to target and publishes it in target's repository.
func publishDiscoveredDeltaForTarget(
	harness *e2e.Harness, registry, tag, sourceImage, sourceDigest string, target applicationDeltaTarget,
) (discoveredDeltaArtifact, error) {
	sourceRef, err := applicationImageDigestReference(sourceImage, sourceDigest)
	if err != nil {
		return discoveredDeltaArtifact{}, err
	}
	targetRef, err := applicationImageDigestReference(target.image, target.targetDigest)
	if err != nil {
		return discoveredDeltaArtifact{}, err
	}
	return publishDiscoveredDelta(harness, registry, tag, target.repository, sourceRef, targetRef)
}

type discoveredDeltaManifest struct {
	Config struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"config"`
	Layers []struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	} `json:"layers"`
}

// fetchDiscoveredDeltaManifest fetches and parses an OCI delta manifest.
func fetchDiscoveredDeltaManifest(repository, digest string) (discoveredDeltaManifest, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := e2eRegistryHTTPClient()
	if err != nil {
		return discoveredDeltaManifest{}, 0, err
	}
	url := "https://" + auxSvcs.Registry.URL + "/v2/" + repository + "/manifests/" + digest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return discoveredDeltaManifest{}, 0, err
	}
	req.Header.Set("Accept", registryManifestAccept)
	resp, err := client.Do(req)
	if err != nil {
		return discoveredDeltaManifest{}, 0, fmt.Errorf("fetch discovered delta manifest %s: %w", digest, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return discoveredDeltaManifest{}, 0, fmt.Errorf("read discovered delta manifest %s: %w", digest, err)
	}
	if resp.StatusCode != http.StatusOK {
		return discoveredDeltaManifest{}, 0, fmt.Errorf("fetch discovered delta manifest %s: %s: %s", digest, resp.Status, strings.TrimSpace(string(body)))
	}
	var manifest discoveredDeltaManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return discoveredDeltaManifest{}, 0, fmt.Errorf("parse discovered delta manifest %s: %w", digest, err)
	}
	return manifest, int64(len(body)), nil
}

// deleteDiscoveredDeltaLayer removes the first layer of a published delta artifact.
func deleteDiscoveredDeltaLayer(repository string, artifact discoveredDeltaArtifact) error {
	manifest, _, err := fetchDiscoveredDeltaManifest(repository, artifact.digest)
	if err != nil {
		return err
	}
	if len(manifest.Layers) == 0 {
		return fmt.Errorf("discovered delta %s has no layer to corrupt", artifact.reference)
	}
	layerDigest := manifest.Layers[0].Digest

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := e2eRegistryHTTPClient()
	if err != nil {
		return err
	}
	url := "https://" + auxSvcs.Registry.URL + "/v2/" + repository + "/blobs/" + layerDigest
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create delete request for discovered delta layer %s: %w", layerDigest, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("delete discovered delta layer %s: %w", layerDigest, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return fmt.Errorf("read delete response for discovered delta layer %s: %w", layerDigest, err)
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("delete discovered delta layer %s: %s: %s", layerDigest, resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// discoveredDeltaDownloadSize returns the total manifest, config, and layer download size.
func discoveredDeltaDownloadSize(repository string, artifact discoveredDeltaArtifact) (int64, error) {
	manifest, manifestSize, err := fetchDiscoveredDeltaManifest(repository, artifact.digest)
	if err != nil {
		return 0, err
	}
	size := manifestSize + manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return size, nil
}

// expectedDiscoveredDelta selects the artifact matching discovery's size and reference ordering.
func expectedDiscoveredDelta(repository string, candidates ...discoveredDeltaArtifact) (discoveredDeltaArtifact, error) {
	if len(candidates) == 0 {
		return discoveredDeltaArtifact{}, fmt.Errorf("no discovered delta candidates were provided")
	}
	selected := candidates[0]
	selectedSize, err := discoveredDeltaDownloadSize(repository, selected)
	if err != nil {
		return discoveredDeltaArtifact{}, err
	}
	for _, candidate := range candidates[1:] {
		candidateSize, err := discoveredDeltaDownloadSize(repository, candidate)
		if err != nil {
			return discoveredDeltaArtifact{}, err
		}
		if candidateSize < selectedSize || (candidateSize == selectedSize && candidate.reference < selected.reference) {
			selected = candidate
			selectedSize = candidateSize
		}
	}
	return selected, nil
}

// agentAppliedDiscoveredDelta returns a condition that checks the agent's selected delta log.
func agentAppliedDiscoveredDelta(harness *e2e.Harness, target applicationDeltaTarget, artifact discoveredDeltaArtifact) func() error {
	return func() error {
		logs, err := harness.GetFlightctlAgentLogs()
		if err != nil {
			return err
		}
		expected := fmt.Sprintf("Applied OCI delta for application image %s from %s", target.image, artifact.reference)
		if !strings.Contains(logs, expected) {
			return fmt.Errorf("agent logs do not show selected delta %s for target %s", artifact.reference, target.image)
		}
		return nil
	}
}

// discoveredDeltaReferrer returns a condition that checks for a matching OCI referrer.
func discoveredDeltaReferrer(harness *e2e.Harness, registry, repository, sourceDigest, targetDigest string) func() error {
	return func() error {
		referrers, err := harness.FetchReferrers(registry, repository, targetDigest)
		if err != nil {
			return err
		}
		for _, referrer := range referrers {
			if referrer.ArtifactType != discoveredDeltaArtifactType {
				continue
			}
			if referrer.Annotations[discoveredDeltaSourceAnnotation] != sourceDigest {
				continue
			}
			return nil
		}
		return fmt.Errorf("repository %s has no OCI delta referrer for target %s and source %s", repository, targetDigest, sourceDigest)
	}
}

// discoveredOSDeltaOutcome returns a condition that checks a discovered OS delta outcome.
func discoveredOSDeltaOutcome(harness *e2e.Harness, deviceID, targetDigest string) func() error {
	return func() error {
		device := getDeltaDevice(harness, deviceID)
		if device.Status.Os.ImageDigest != targetDigest {
			return fmt.Errorf("device %s reports OS digest %q, want %q", deviceID, device.Status.Os.ImageDigest, targetDigest)
		}
		if device.Status.Os.DeltaSize != nil {
			return StopTrying(fmt.Sprintf("device %s unexpectedly reported a control-plane OS delta size", deviceID))
		}
		if device.Status.Os.LastDelta == nil {
			return fmt.Errorf("device %s has not reported an OS delta outcome", deviceID)
		}
		if device.Status.Os.LastDelta.Outcome != v1beta1.DeviceDeltaApplyOutcomeApplied {
			reason := ""
			if device.Status.Os.LastDelta.FallbackReason != nil {
				reason = ": " + *device.Status.Os.LastDelta.FallbackReason
			}
			return StopTrying(fmt.Sprintf("device %s reported OS delta outcome %q, expected Applied%s",
				deviceID, device.Status.Os.LastDelta.Outcome, reason))
		}
		return nil
	}
}

// reportedApplicationImageDigest returns the locally observed digest for an application image.
func reportedApplicationImageDigest(harness *e2e.Harness, deviceID, appName, image string) (string, error) {
	device := getDeltaDevice(harness, deviceID)
	for i := range device.Status.Applications {
		appStatus := device.Status.Applications[i]
		if appStatus.Name != appName || appStatus.ImageDigests == nil {
			continue
		}
		for _, imageDigest := range *appStatus.ImageDigests {
			if imageDigest.Image == image && imageDigest.Digest != "" {
				return imageDigest.Digest, nil
			}
		}
	}
	return "", fmt.Errorf("application %s has not reported image %s with a digest", appName, image)
}

// assertNoRenderedApplicationDeltaHints verifies rendered applications contain no control-plane hints.
func assertNoRenderedApplicationDeltaHints(apps []v1beta1.ApplicationProviderSpec) error {
	serialized, err := json.Marshal(apps)
	if err != nil {
		return err
	}
	if strings.Contains(string(serialized), `"deltaImage`) {
		return fmt.Errorf("rendered application specs contain control-plane delta hints")
	}
	return nil
}

// discoveredApplicationDeltaOutcome returns a condition that checks a discovered application delta outcome.
func discoveredApplicationDeltaOutcome(
	harness *e2e.Harness, deviceID, appName string, targets []applicationDeltaTarget, expectedOutcome v1beta1.DeviceDeltaApplyOutcomeType,
) func() error {
	return func() error {
		device := getDeltaDevice(harness, deviceID)
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
		for _, target := range targets {
			if !applicationStatusHasImageDigest(appStatus, target.image, target.targetDigest) {
				return fmt.Errorf("application %s has not reported target image %s with registry digest %s", appName, target.image, target.targetDigest)
			}
		}
		if appStatus.DeltaSize != nil {
			return StopTrying(fmt.Sprintf("application %s unexpectedly reported a control-plane delta size", appName))
		}
		if appStatus.LastDelta == nil {
			return fmt.Errorf("application %s has not reported a delta apply outcome", appName)
		}
		if appStatus.LastDelta.Outcome != expectedOutcome {
			reason := ""
			if appStatus.LastDelta.FallbackReason != nil {
				reason = ": " + *appStatus.LastDelta.FallbackReason
			}
			return StopTrying(fmt.Sprintf("application %s reported delta outcome %q, expected %q%s",
				appName, appStatus.LastDelta.Outcome, expectedOutcome, reason))
		}
		if expectedOutcome == v1beta1.DeviceDeltaApplyOutcomeFallback && (appStatus.LastDelta.FallbackReason == nil || *appStatus.LastDelta.FallbackReason == "") {
			return fmt.Errorf("application %s reported delta fallback without a reason", appName)
		}
		return nil
	}
}
