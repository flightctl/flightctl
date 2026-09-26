package delta

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"sigs.k8s.io/yaml"
)

const (
	TIMEOUT               = "5m"
	POLLING               = "500ms"
	LONGTIMEOUT           = "10m"
	progressStall         = 90 * time.Second
	rolloutProgressStall  = 5 * time.Minute
	eventPropagationGrace = 10 * time.Second
	fleetLabelKey         = "fleet"
)

type deltaUpdateExpectation int

const (
	deltaUpdateExpectGeneratedAndUsed deltaUpdateExpectation = iota
	deltaUpdateExpectGenerationButNotUsed
	deltaUpdateExpectCachedAndUsed
)

func (e deltaUpdateExpectation) expectsGeneration() bool {
	return e != deltaUpdateExpectCachedAndUsed
}

func (e deltaUpdateExpectation) expectsGenerationSuccess() bool {
	return e == deltaUpdateExpectGeneratedAndUsed
}

func (e deltaUpdateExpectation) expectsDeltaHint() bool {
	return e != deltaUpdateExpectGenerationButNotUsed
}

var _ = Describe("OS delta hold", Label("delta"), Serial, func() {
	It("When a fleet OS image changes with a writable delta target it should hold then apply a generated OS delta", func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		fleetName := "delta-os-hold"
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, nil))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")
		requireDeltaGenerationSupport(harness, deviceId)

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceId)
		Expect(err).NotTo(HaveOccurred())

		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, nil))).To(Succeed())

		waitSettledOSDeltaUpdate(harness, fleetName, deviceId, v2Image, deltaUpdateExpectGeneratedAndUsed, eventBaseline)
	})

	It("When generateDelta is false it should write the new OS spec without FleetDeltaPreparing", func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		fleetName := "delta-os-skip-generate"
		policy := &v1beta1.RolloutPolicy{DeltaGeneration: &v1beta1.RolloutPolicyDeltaGeneration{GenerateDelta: lo.ToPtr(false)}}
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, policy))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceId)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, policy))).To(Succeed())

		waitForRolloutWithoutDeltaPreparing(harness, fleetName, deviceId, v2Image, eventBaseline,
			"device spec is V2 and UpToDate without waiting for generation", TIMEOUT)
	})

	It("When maxWaitForDelta expires it should roll out the OS without a deltaImage hint", func() {
		temporarilyUseFastDeltaPrepareDeadlinePoll()

		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		fleetName := "delta-os-deadline"
		policy := &v1beta1.RolloutPolicy{DeltaGeneration: &v1beta1.RolloutPolicyDeltaGeneration{MaxWaitForDelta: lo.ToPtr(v1beta1.Duration("1s"))}}
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, policy))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")
		requireDeltaGenerationSupport(harness, deviceId)

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceId)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, policy))).To(Succeed())

		waitSettledOSDeltaUpdate(harness, fleetName, deviceId, v2Image, deltaUpdateExpectGenerationButNotUsed, eventBaseline)
	})

	It("When a standalone device OS spec changes with a writable delta target it should delay render then hint", Label("standalone"), func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")
		requireDeltaGenerationSupport(harness, deviceId)

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		eventBaseline, err := captureDeltaEventBaseline(harness, "", deviceId)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			if device.Spec == nil {
				device.Spec = &v1beta1.DeviceSpec{}
			}
			device.Spec.Os = &v1beta1.DeviceOsSpec{Image: v2Image}
		})).To(Succeed())

		waitSettledOSDeltaUpdate(harness, "", deviceId, v2Image, deltaUpdateExpectGeneratedAndUsed, eventBaseline)
	})

	It("When there is no writable delta target it should update OS without a hint", func() {
		harness := e2e.GetWorkerHarness()

		fleetName := "delta-os-no-target"
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, nil))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		eventBaseline, err := captureDeltaEventBaseline(harness, fleetName, deviceId)
		Expect(err).NotTo(HaveOccurred())
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, nil))).To(Succeed())

		waitForRolloutWithoutDeltaPreparing(harness, fleetName, deviceId, v2Image, eventBaseline,
			"device UpToDate on V2 without deltaImage", LONGTIMEOUT)
	})
})

func temporarilyUseFastDeltaPrepareDeadlinePoll() {
	providers := setup.GetDefaultProviders()
	Expect(providers).NotTo(BeNil())
	Expect(providers.Infra).NotTo(BeNil())
	Expect(providers.Lifecycle).NotTo(BeNil())

	originalConfig, err := providers.Infra.GetServiceConfig(infra.ServicePeriodic)
	Expect(err).NotTo(HaveOccurred())
	restoreConfig, err := restoreDeltaPrepareDeadlineConfig(originalConfig)
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() {
		Expect(providers.Infra.SetServiceConfig(infra.ServicePeriodic, "config.yaml", restoreConfig)).To(Succeed())
		Expect(providers.Lifecycle.Restart(infra.ServicePeriodic)).To(Succeed())
		Expect(providers.Lifecycle.WaitForReady(infra.ServicePeriodic, 2*time.Minute)).To(Succeed())
	})

	updatedConfig, err := withDeltaPrepareDeadlineInterval(originalConfig, time.Second)
	Expect(err).NotTo(HaveOccurred())
	Expect(providers.Infra.SetServiceConfig(infra.ServicePeriodic, "config.yaml", updatedConfig)).To(Succeed())
	Expect(providers.Lifecycle.Restart(infra.ServicePeriodic)).To(Succeed())
	Expect(providers.Lifecycle.WaitForReady(infra.ServicePeriodic, 2*time.Minute)).To(Succeed())
}

func withDeltaPrepareDeadlineInterval(configYAML string, interval time.Duration) (string, error) {
	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(configYAML), &cfg); err != nil {
		return "", fmt.Errorf("parse periodic service config: %w", err)
	}
	if cfg == nil {
		cfg = make(map[string]interface{})
	}
	periodic := ensureConfigMap(cfg, "periodic")
	tasks := ensureConfigMap(periodic, "tasks")
	deltaPrepareDeadline := ensureConfigMap(tasks, "deltaPrepareDeadline")
	schedule := ensureConfigMap(deltaPrepareDeadline, "schedule")
	schedule["interval"] = interval.String()

	updated, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal periodic service config: %w", err)
	}
	return string(updated), nil
}

func restoreDeltaPrepareDeadlineConfig(original string) (string, error) {
	var cfg map[string]interface{}
	if err := yaml.Unmarshal([]byte(original), &cfg); err != nil {
		return "", fmt.Errorf("parse original periodic service config: %w", err)
	}
	if cfg == nil {
		cfg = make(map[string]interface{})
	}
	if _, hasPeriodicConfig := cfg["periodic"]; hasPeriodicConfig {
		return original, nil
	}

	// Quadlet maps periodic settings back to its shared service-config.yaml.
	// An explicit null tells that mapping to remove the temporary setting when
	// the original rendered config had no periodic section.
	cfg["periodic"] = nil
	restored, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("marshal original periodic service config: %w", err)
	}
	return string(restored), nil
}

func ensureConfigMap(parent map[string]interface{}, key string) map[string]interface{} {
	child, ok := parent[key].(map[string]interface{})
	if !ok {
		child = make(map[string]interface{})
		parent[key] = child
	}
	return child
}

func createWritableDeltaRepo(harness *e2e.Harness) {
	registry := auxSvcs.Registry.Host + ":" + auxSvcs.Registry.Port
	caPEM, err := os.ReadFile(filepath.Join(util.GetTopLevelDir(), "bin", "e2e-certs", "pki", "CA", "ca.crt"))
	Expect(err).ToNot(HaveOccurred())
	caCrt := base64.StdEncoding.EncodeToString(caPEM)

	spec := v1beta1.RepositorySpec{}
	oci := v1beta1.OciRepoSpec{
		Registry:           registry,
		Type:               v1beta1.OciRepoSpecTypeOci,
		AccessMode:         lo.ToPtr(v1beta1.ReadWrite),
		DeltaStorageTarget: lo.ToPtr(true),
		Scheme:             lo.ToPtr(v1beta1.Https),
		CaCrt:              lo.ToPtr(caCrt),
	}
	Expect(spec.FromOciRepoSpec(oci)).To(Succeed())
	Expect(harness.CreateRepository(spec, v1beta1.ObjectMeta{Name: lo.ToPtr("delta-storage")})).To(Succeed())
}

func osFleetSpec(harness *e2e.Harness, fleetName, imageTag string, policy *v1beta1.RolloutPolicy) v1beta1.FleetSpec {
	deviceSpec, err := harness.CreateFleetDeviceSpec(auxSvcs.Registry.Host, auxSvcs.Registry.Port, imageTag)
	Expect(err).ToNot(HaveOccurred())
	selector := v1beta1.LabelSelector{MatchLabels: &map[string]string{fleetLabelKey: fleetName}}
	return v1beta1.FleetSpec{
		Selector: &selector,
		Template: struct {
			Metadata *v1beta1.ObjectMeta `json:"metadata,omitempty"`
			Spec     v1beta1.DeviceSpec  `json:"spec"`
		}{
			Spec: deviceSpec,
		},
		RolloutPolicy: policy,
	}
}

func requireDeltaGenerationSupport(harness *e2e.Harness, deviceId string) {
	infra.RequireOciDeltaAvailable(harness.Context, setup.GetDefaultProviders())
	device, err := harness.GetDevice(deviceId)
	if err != nil {
		Fail(fmt.Sprintf("get device %s: %v", deviceId, err))
		return
	}
	if device == nil {
		Fail(fmt.Sprintf("device %s was not returned", deviceId))
		return
	}
	if device.Status == nil {
		Fail("device has no status")
		return
	}
	if device.Status.SystemInfo.DeltaEligible == nil {
		Fail("device status.systemInfo.deltaEligible is not set")
		return
	}
	Expect(*device.Status.SystemInfo.DeltaEligible).To(BeTrue())
	if device.Status.SystemInfo.BootcVersion == nil {
		Fail("device status.systemInfo.bootcVersion is not set")
		return
	}
	Expect(*device.Status.SystemInfo.BootcVersion).ToNot(BeEmpty())
}

func waitDeviceUpToDate(harness *e2e.Harness, deviceId, description string) {
	harness.WaitForDeviceContents(deviceId, description, func(device *v1beta1.Device) bool {
		return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
	}, TIMEOUT)
}

type deltaEventBaseline struct {
	fleet  map[string]struct{}
	device map[string]struct{}
}

func captureDeltaEventBaseline(harness *e2e.Harness, fleetName, deviceId string) (deltaEventBaseline, error) {
	baseline := deltaEventBaseline{}
	var err error
	if fleetName != "" {
		baseline.fleet, err = captureResourceEventNames(harness, fleetName)
		if err != nil {
			return deltaEventBaseline{}, err
		}
	}
	baseline.device, err = captureResourceEventNames(harness, deviceId)
	if err != nil {
		return deltaEventBaseline{}, err
	}
	return baseline, nil
}

func captureResourceEventNames(harness *e2e.Harness, resourceName string) (map[string]struct{}, error) {
	events, err := listResourceEvents(harness, resourceName)
	if err != nil {
		return nil, fmt.Errorf("list events for %s: %w", resourceName, err)
	}
	eventNames := make(map[string]struct{}, len(events))
	for _, event := range events {
		if event.Metadata.Name != nil {
			eventNames[*event.Metadata.Name] = struct{}{}
		}
	}
	return eventNames, nil
}

func waitForRolloutWithoutDeltaPreparing(harness *e2e.Harness, fleetName, deviceId, v2Image string, baseline deltaEventBaseline, description, timeout string) {
	loggedEvents := make(map[string]struct{})
	fleetProgress := generationProgressTracker{lastChange: time.Now()}
	deviceProgress := generationProgressTracker{lastChange: time.Now()}
	lastLifecycleProgress := time.Now()
	var settledAt time.Time
	Eventually(func() error {
		fleet, err := harness.GetFleet(fleetName)
		if err != nil {
			return fmt.Errorf("get fleet %s: %w", fleetName, err)
		}
		failIfDeltaPreparingFailed(fleet)
		if fleetConditionTrue(fleet, v1beta1.ConditionTypeFleetDeltaPreparing) {
			return StopTrying(fmt.Sprintf("fleet %s unexpectedly entered FleetDeltaPreparing", fleetName))
		}

		device, err := harness.GetDevice(deviceId)
		if err != nil {
			return fmt.Errorf("get device %s: %w", deviceId, err)
		}
		if device.Status == nil {
			return fmt.Errorf("device %s has no status", deviceId)
		}
		lifecycle, observedProgress, err := observeDeltaLifecycleEvents(harness, fleetName, deviceId, baseline, loggedEvents, &fleetProgress, &deviceProgress)
		if err != nil {
			return err
		}
		if observedProgress {
			lastLifecycleProgress = time.Now()
		}
		if lifecycle.generationProgress {
			return StopTrying(fmt.Sprintf("device %s unexpectedly observed DeltaGenerationProgress while delta generation is disabled", deviceId))
		}
		failIfDeviceDeltaPreparingFailed(device)
		if deviceConditionTrue(device, v1beta1.ConditionTypeDeviceDeltaPreparing) {
			return StopTrying(fmt.Sprintf("device %s unexpectedly entered DeviceDeltaPreparing", deviceId))
		}

		rendered, err := tryRenderedDevice(harness, deviceId)
		if err != nil {
			if time.Since(lastLifecycleProgress) > rolloutProgressStall {
				return StopTrying(fmt.Sprintf("device %s rollout made no observable progress for %s", deviceId, rolloutProgressStall))
			}
			return err
		}
		if renderedOsImage(rendered) == v2Image && renderedDeltaImage(rendered) != "" {
			return StopTrying(fmt.Sprintf("device %s rendered unexpected OS delta hint %q", deviceId, renderedDeltaImage(rendered)))
		}
		deviceSettled := deviceOsImage(device) == v2Image &&
			device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate &&
			device.Status.Os.Image == v2Image && renderedOsImage(rendered) == v2Image
		if !deviceSettled {
			settledAt = time.Time{}
			if time.Since(lastLifecycleProgress) > rolloutProgressStall {
				return StopTrying(fmt.Sprintf("device %s rollout made no observable progress for %s", deviceId, rolloutProgressStall))
			}
			return fmt.Errorf("device %s has not reached UpToDate on %q (reported image %q, rendered image %q)", deviceId, v2Image, device.Status.Os.Image, renderedOsImage(rendered))
		}
		if settledAt.IsZero() {
			settledAt = time.Now()
		}
		if !lifecycle.deviceContentUpToDate {
			return retrySettledEvent(fmt.Errorf("device %s reached the target state without a DeviceContentUpToDate event", deviceId), settledAt)
		}
		return nil
	}, timeout, POLLING).Should(BeNil(), description)
}

func waitSettledOSDeltaUpdate(harness *e2e.Harness, fleetName, deviceId, v2Image string, expectation deltaUpdateExpectation, baseline deltaEventBaseline) {
	fleetProgress := generationProgressTracker{lastChange: time.Now()}
	deviceProgress := generationProgressTracker{lastChange: time.Now()}
	loggedEvents := make(map[string]struct{})
	lastLifecycleProgress := time.Now()
	var settledAt time.Time
	Eventually(func() error {
		var fleetConditions []v1beta1.Condition
		var fleetGeneration *v1beta1.DeltaGenerationStatus
		if fleetName != "" {
			fleet, err := harness.GetFleet(fleetName)
			if err != nil {
				return err
			}
			failIfDeltaPreparingFailed(fleet)
			if fleet != nil && fleet.Status != nil {
				fleetConditions = fleet.Status.Conditions
				fleetGeneration = fleet.Status.DeltaGeneration
			}
		}

		device, err := harness.GetDevice(deviceId)
		if err != nil {
			return err
		}
		failIfDeviceDeltaPreparingFailed(device)
		if device.Status == nil {
			return fmt.Errorf("device %s has no status", deviceId)
		}
		lifecycle, observedProgress, err := observeDeltaLifecycleEvents(harness, fleetName, deviceId, baseline, loggedEvents, &fleetProgress, &deviceProgress)
		if err != nil {
			return err
		}
		if observedProgress {
			lastLifecycleProgress = time.Now()
		}

		if expectation.expectsDeltaHint() && device.Status.Os.LastDelta != nil && device.Status.Os.LastDelta.FallbackReason != nil {
			return StopTrying(fmt.Sprintf("device %s fell back: %s", deviceId, *device.Status.Os.LastDelta.FallbackReason))
		}

		rendered, err := tryRenderedDevice(harness, deviceId)
		if err != nil {
			if err := waitForExpectedGenerationProgress(expectation, lifecycle, fleetName, deviceId, &fleetProgress, &deviceProgress); err != nil {
				return err
			}
			if time.Since(lastLifecycleProgress) > rolloutProgressStall {
				return StopTrying(fmt.Sprintf("device %s rollout made no observable progress for %s", deviceId, rolloutProgressStall))
			}
			return err
		}

		deviceSettled := device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate &&
			device.Status.Os.Image == v2Image && renderedOsImage(rendered) == v2Image
		if !deviceSettled {
			settledAt = time.Time{}
			if fleetName != "" {
				if err := preparingStillRunning("fleet", fleetConditions, v1beta1.ConditionTypeFleetDeltaPreparing, fleetGeneration, &fleetProgress); err != nil {
					return err
				}
			}
			if err := preparingStillRunning("device", device.Status.Conditions, v1beta1.ConditionTypeDeviceDeltaPreparing, device.Status.DeltaGeneration, &deviceProgress); err != nil {
				return err
			}
			if err := waitForExpectedGenerationProgress(expectation, lifecycle, fleetName, deviceId, &fleetProgress, &deviceProgress); err != nil {
				return err
			}
			if time.Since(lastLifecycleProgress) > rolloutProgressStall {
				return StopTrying(fmt.Sprintf("device %s rollout made no observable progress for %s", deviceId, rolloutProgressStall))
			}
			if device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
				return fmt.Errorf("device %s updated status is %s", deviceId, device.Status.Updated.Status)
			}
			if device.Status.Os.Image != v2Image {
				return fmt.Errorf("device %s reports OS image %q, want %q", deviceId, device.Status.Os.Image, v2Image)
			}
			return fmt.Errorf("device %s rendered OS is %q", deviceId, renderedOsImage(rendered))
		}
		if settledAt.IsZero() {
			settledAt = time.Now()
		}

		fleetPreparing, _ := preparingTrueMessage(fleetConditions, v1beta1.ConditionTypeFleetDeltaPreparing)
		if fleetName != "" && fleetPreparing {
			return StopTrying(fmt.Sprintf("fleet %s reached the target device state while FleetDeltaPreparing is still true", fleetName))
		}
		if deviceConditionTrue(device, v1beta1.ConditionTypeDeviceDeltaPreparing) {
			return StopTrying(fmt.Sprintf("device %s reached the target state while DeviceDeltaPreparing is still true", deviceId))
		}
		if pending, message := expectedGenerationProgressPending(expectation, lifecycle, fleetName, deviceId); pending {
			return retrySettledEvent(fmt.Errorf("device %s reached the target state but %s", deviceId, message), settledAt)
		}

		delta := renderedDeltaImage(rendered)
		if expectation.expectsDeltaHint() && (delta == "" || delta == v2Image) {
			return StopTrying(fmt.Sprintf("device %s reached the target state without an OS delta hint", deviceId))
		}
		if !expectation.expectsDeltaHint() && delta != "" {
			return StopTrying(fmt.Sprintf("device %s has unexpected OS delta hint %q", deviceId, delta))
		}
		if err := validateDeltaLifecycleEvents(lifecycle, fleetName, deviceId, expectation); err != nil {
			return retrySettledEvent(fmt.Errorf("device %s reached the target state but %w", deviceId, err), settledAt)
		}
		return nil
	}, LONGTIMEOUT, POLLING).Should(BeNil())
}

func retrySettledEvent(err error, settledAt time.Time) error {
	if settledAt.IsZero() || time.Since(settledAt) <= eventPropagationGrace {
		return err
	}
	return StopTrying(err.Error())
}

type deltaLifecycleObservation struct {
	generationProgress           bool
	generationSucceeded          bool
	generationTemplateVersions   map[string]struct{}
	successfulTemplateVersions   map[string]struct{}
	fleetRolloutTemplateVersions map[string]struct{}
	deviceContentUpToDate        bool
}

func observeDeltaLifecycleEvents(harness *e2e.Harness, fleetName, deviceId string, baseline deltaEventBaseline, loggedEvents map[string]struct{}, fleetProgress, deviceProgress *generationProgressTracker) (deltaLifecycleObservation, bool, error) {
	observation := deltaLifecycleObservation{
		generationTemplateVersions:   make(map[string]struct{}),
		successfulTemplateVersions:   make(map[string]struct{}),
		fleetRolloutTemplateVersions: make(map[string]struct{}),
	}
	observedProgress := false
	deviceEvents, err := newResourceEvents(harness, v1beta1.DeviceKind, deviceId, baseline.device)
	if err != nil {
		return observation, false, err
	}
	observedProgress = logDeltaEventsOnce(deviceEvents, loggedEvents, deviceProgress) || observedProgress
	observation.deviceContentUpToDate = hasEventReason(deviceEvents, v1beta1.EventReasonDeviceContentUpToDate)

	if fleetName == "" {
		if err := observeDeltaGenerationProgress(deviceEvents, v1beta1.DeviceKind, deviceId, &observation); err != nil {
			return observation, observedProgress, err
		}
	} else {
		fleetEvents, err := newResourceEvents(harness, v1beta1.FleetKind, fleetName, baseline.fleet)
		if err != nil {
			return observation, observedProgress, err
		}
		observedProgress = logDeltaEventsOnce(fleetEvents, loggedEvents, fleetProgress) || observedProgress
		if err := observeDeltaGenerationProgress(fleetEvents, v1beta1.FleetKind, fleetName, &observation); err != nil {
			return observation, observedProgress, err
		}
		for _, event := range fleetEvents {
			if event.Reason != v1beta1.EventReasonFleetRolloutStarted {
				continue
			}
			if event.Details == nil {
				return observation, observedProgress, StopTrying(fmt.Sprintf("fleet %s FleetRolloutStarted event has no details", fleetName))
			}
			details, err := event.Details.AsFleetRolloutStartedDetails()
			if err != nil {
				return observation, observedProgress, StopTrying(fmt.Sprintf("fleet %s FleetRolloutStarted event has invalid details: %v", fleetName, err))
			}
			if details.TemplateVersion == "" {
				return observation, observedProgress, StopTrying(fmt.Sprintf("fleet %s FleetRolloutStarted event has no template version", fleetName))
			}
			observation.fleetRolloutTemplateVersions[details.TemplateVersion] = struct{}{}
		}
	}
	return observation, observedProgress, nil
}

func observeDeltaGenerationProgress(events []v1beta1.Event, kind, name string, observation *deltaLifecycleObservation) error {
	for _, event := range events {
		if event.Reason != v1beta1.EventReasonDeltaGenerationProgress {
			continue
		}
		if event.Details == nil {
			return StopTrying(fmt.Sprintf("%s %s DeltaGenerationProgress event has no details", kind, name))
		}
		details, err := event.Details.AsDeltaGenerationProgressDetails()
		if err != nil {
			return StopTrying(fmt.Sprintf("%s %s DeltaGenerationProgress event has invalid details: %v", kind, name, err))
		}
		if details.ImageRepository == "" || details.SourceDigest == "" || details.TargetDigest == "" {
			return StopTrying(fmt.Sprintf("%s %s DeltaGenerationProgress event is missing image repository or image digests", kind, name))
		}
		if kind == v1beta1.FleetKind {
			if details.TemplateVersion == nil || *details.TemplateVersion == "" {
				return StopTrying(fmt.Sprintf("fleet %s DeltaGenerationProgress event is missing template version", name))
			}
			observation.generationTemplateVersions[*details.TemplateVersion] = struct{}{}
		} else if details.SpecHash == nil || *details.SpecHash == "" {
			return StopTrying(fmt.Sprintf("device %s DeltaGenerationProgress event is missing spec hash", name))
		}

		switch details.GenerationStatus {
		case v1beta1.DeltaGenerationProgressInProgress:
			observation.generationProgress = true
		case v1beta1.DeltaGenerationProgressSucceeded:
			observation.generationProgress = true
			observation.generationSucceeded = true
			if details.TemplateVersion != nil && *details.TemplateVersion != "" {
				observation.successfulTemplateVersions[*details.TemplateVersion] = struct{}{}
			}
		case v1beta1.DeltaGenerationProgressFailed, v1beta1.DeltaGenerationProgressRejected:
			return StopTrying(fmt.Sprintf("%s %s delta generation ended with status %q: %s", kind, name, details.GenerationStatus, event.Message))
		default:
			return StopTrying(fmt.Sprintf("%s %s DeltaGenerationProgress event has unknown status %q", kind, name, details.GenerationStatus))
		}
	}
	return nil
}

func expectedGenerationProgressPending(expectation deltaUpdateExpectation, observation deltaLifecycleObservation, fleetName, deviceId string) (bool, string) {
	if !expectation.expectsGeneration() {
		return false, ""
	}
	resource := "device " + deviceId
	if fleetName != "" {
		resource = "fleet " + fleetName
	}
	if !observation.generationProgress {
		return true, fmt.Sprintf("waiting for a new DeltaGenerationProgress event for %s", resource)
	}
	if expectation.expectsGenerationSuccess() && !observation.generationSucceeded {
		return true, fmt.Sprintf("waiting for a succeeded DeltaGenerationProgress event for %s", resource)
	}
	return false, ""
}

func waitForExpectedGenerationProgress(expectation deltaUpdateExpectation, observation deltaLifecycleObservation, fleetName, deviceId string, fleetProgress, deviceProgress *generationProgressTracker) error {
	pending, message := expectedGenerationProgressPending(expectation, observation, fleetName, deviceId)
	if !pending {
		return nil
	}
	tracker := deviceProgress
	if fleetName != "" {
		tracker = fleetProgress
	}
	if time.Since(tracker.lastChange) > progressStall {
		return StopTrying(fmt.Sprintf("%s; no delta generation progress for %s", message, progressStall))
	}
	return fmt.Errorf("%s", message)
}

func validateDeltaLifecycleEvents(observation deltaLifecycleObservation, fleetName, deviceId string, expectation deltaUpdateExpectation) error {
	if pending, message := expectedGenerationProgressPending(expectation, observation, fleetName, deviceId); pending {
		return fmt.Errorf("%s", message)
	}
	if fleetName != "" {
		if len(observation.fleetRolloutTemplateVersions) == 0 {
			return fmt.Errorf("waiting for FleetRolloutStarted event for fleet %s", fleetName)
		}
		if expectation.expectsGeneration() {
			generationVersions := observation.generationTemplateVersions
			if expectation.expectsGenerationSuccess() {
				generationVersions = observation.successfulTemplateVersions
			}
			matched := false
			for templateVersion := range generationVersions {
				if _, ok := observation.fleetRolloutTemplateVersions[templateVersion]; ok {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("waiting for FleetRolloutStarted for the delta generation template version on fleet %s", fleetName)
			}
		}
	}
	if !observation.deviceContentUpToDate {
		return fmt.Errorf("waiting for DeviceContentUpToDate event for device %s", deviceId)
	}
	return nil
}

func newResourceEvents(harness *e2e.Harness, kind, name string, baseline map[string]struct{}) ([]v1beta1.Event, error) {
	events, err := listResourceEvents(harness, name)
	if err != nil {
		return nil, fmt.Errorf("list events for %s %s: %w", kind, name, err)
	}
	var current []v1beta1.Event
	for _, event := range events {
		if isNewResourceEvent(event, kind, name, baseline) {
			current = append(current, event)
		}
	}
	return current, nil
}

func listResourceEvents(harness *e2e.Harness, resourceName string) ([]v1beta1.Event, error) {
	fieldSelector := fmt.Sprintf("involvedObject.name=%s", resourceName)
	resp, err := harness.Client.ListEventsWithResponse(harness.Context, &v1beta1.ListEventsParams{FieldSelector: &fieldSelector})
	if err != nil {
		return nil, err
	}
	if resp == nil {
		return nil, fmt.Errorf("nil response")
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode())
	}
	return resp.JSON200.Items, nil
}

func isNewResourceEvent(event v1beta1.Event, kind, name string, baseline map[string]struct{}) bool {
	if event.InvolvedObject.Kind != kind || event.InvolvedObject.Name != name || event.Metadata.Name == nil || *event.Metadata.Name == "" {
		return false
	}
	_, existed := baseline[*event.Metadata.Name]
	return !existed
}

func hasEventReason(events []v1beta1.Event, reason v1beta1.EventReason) bool {
	for _, event := range events {
		if event.Reason == reason {
			return true
		}
	}
	return false
}

func logDeltaEventsOnce(events []v1beta1.Event, loggedEvents map[string]struct{}, progress *generationProgressTracker) bool {
	observedProgress := false
	for _, event := range events {
		if event.Metadata.Name == nil || *event.Metadata.Name == "" {
			continue
		}
		if _, logged := loggedEvents[*event.Metadata.Name]; logged {
			continue
		}
		loggedEvents[*event.Metadata.Name] = struct{}{}
		if event.Reason == v1beta1.EventReasonDeltaGenerationProgress && progress != nil {
			progress.lastChange = time.Now()
		}
		observedProgress = isDeltaLifecycleProgressEvent(event.Reason) || observedProgress
		GinkgoWriter.Printf("delta test observed %s for %s/%s: %s\n", event.Reason, event.InvolvedObject.Kind, event.InvolvedObject.Name, event.Message)
	}
	return observedProgress
}

func isDeltaLifecycleProgressEvent(reason v1beta1.EventReason) bool {
	switch reason {
	case v1beta1.EventReasonDeltaGenerationProgress,
		v1beta1.EventReasonDeltaGenerationCompleted,
		v1beta1.EventReasonDeviceContentOutOfDate,
		v1beta1.EventReasonDeviceContentUpdating,
		v1beta1.EventReasonDeviceIsRebooting,
		v1beta1.EventReasonDeviceConnected,
		v1beta1.EventReasonDeviceOSImageChanged,
		v1beta1.EventReasonDeviceContentUpToDate,
		v1beta1.EventReasonFleetRolloutStarted,
		v1beta1.EventReasonFleetRolloutBatchDispatched,
		v1beta1.EventReasonFleetRolloutBatchCompleted,
		v1beta1.EventReasonFleetRolloutCompleted,
		v1beta1.EventReasonResourceUpdated:
		return true
	default:
		return false
	}
}

type generationProgressTracker struct {
	lastMsg    string
	lastChange time.Time
}

func preparingStillRunning(kind string, conditions []v1beta1.Condition, condType v1beta1.ConditionType, gen *v1beta1.DeltaGenerationStatus, tracker *generationProgressTracker) error {
	preparing, _ := preparingTrueMessage(conditions, condType)
	if !preparing {
		return nil
	}
	msg := generationProgressKey(gen)
	if msg != tracker.lastMsg {
		GinkgoWriter.Printf("%s delta generation progress (still preparing): %s\n", kind, msg)
		tracker.lastMsg = msg
		tracker.lastChange = time.Now()
	}
	if time.Since(tracker.lastChange) > progressStall {
		return StopTrying(fmt.Sprintf("%s delta generation stalled for %s at %q", kind, progressStall, msg))
	}
	return fmt.Errorf("%s delta generation still running: %s", kind, msg)
}

func preparingTrueMessage(conditions []v1beta1.Condition, condType v1beta1.ConditionType) (bool, string) {
	cond := v1beta1.FindStatusCondition(conditions, condType)
	if cond == nil || cond.Status != v1beta1.ConditionStatusTrue {
		return false, ""
	}
	return true, cond.Message
}

func generationProgressKey(st *v1beta1.DeltaGenerationStatus) string {
	if st == nil {
		return ""
	}
	return fmt.Sprintf("%d/%d", st.Completed, st.Total)
}

func fleetConditionTrue(fleet *v1beta1.Fleet, condType v1beta1.ConditionType) bool {
	if fleet == nil || fleet.Status == nil {
		return false
	}
	cond := v1beta1.FindStatusCondition(fleet.Status.Conditions, condType)
	return cond != nil && cond.Status == v1beta1.ConditionStatusTrue
}

func failIfDeltaPreparingFailed(fleet *v1beta1.Fleet) {
	if fleet == nil || fleet.Status == nil {
		return
	}
	failIfPreparingFailed(fleet.Status.Conditions, v1beta1.ConditionTypeFleetDeltaPreparing)
}

func failIfDeviceDeltaPreparingFailed(device *v1beta1.Device) {
	if device == nil || device.Status == nil {
		return
	}
	failIfPreparingFailed(device.Status.Conditions, v1beta1.ConditionTypeDeviceDeltaPreparing)
}

func deviceConditionTrue(device *v1beta1.Device, condType v1beta1.ConditionType) bool {
	if device == nil || device.Status == nil {
		return false
	}
	cond := v1beta1.FindStatusCondition(device.Status.Conditions, condType)
	return cond != nil && cond.Status == v1beta1.ConditionStatusTrue
}

func failIfPreparingFailed(conditions []v1beta1.Condition, condType v1beta1.ConditionType) {
	cond := v1beta1.FindStatusCondition(conditions, condType)
	if cond == nil || cond.Status != v1beta1.ConditionStatusFalse || cond.Reason != "Failed" {
		return
	}
	msg := string(condType) + " failed"
	if cond.Message != "" {
		msg = msg + ": " + cond.Message
	}
	Fail(msg)
}

func deviceOsImage(device *v1beta1.Device) string {
	if device == nil || device.Spec == nil || device.Spec.Os == nil {
		return ""
	}
	return device.Spec.Os.Image
}

func renderedOsImage(device *v1beta1.Device) string {
	return deviceOsImage(device)
}

func renderedDeltaImage(device *v1beta1.Device) string {
	if device == nil || device.Spec == nil || device.Spec.Os == nil || device.Spec.Os.DeltaImage == nil {
		return ""
	}
	return *device.Spec.Os.DeltaImage
}

func tryRenderedDevice(harness *e2e.Harness, deviceId string) (*v1beta1.Device, error) {
	resp, err := harness.Client.GetRenderedDeviceWithResponse(harness.Context, deviceId, nil)
	if err != nil {
		return nil, fmt.Errorf("get rendered device %s: %w", deviceId, err)
	}
	if resp == nil {
		return nil, fmt.Errorf("get rendered device %s: nil response", deviceId)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("get rendered device %s: HTTP %d (%s)", deviceId, resp.StatusCode(), resp.Status())
	}
	return resp.JSON200, nil
}
