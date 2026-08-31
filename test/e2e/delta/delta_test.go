package delta

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/e2e/infra"
	"github.com/flightctl/flightctl/test/e2e/infra/setup"
	"github.com/flightctl/flightctl/test/harness/e2e"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

const (
	TIMEOUT         = "5m"
	POLLING         = "500ms"
	LONGTIMEOUT  = "10m"
	GENERATE_CAP = "30m"
	progressStall   = 90 * time.Second
	fleetLabelKey   = "fleet"
)

var _ = Describe("OS delta hold", Label("delta"), Serial, func() {
	It("When a fleet OS image changes with a writable delta target it should hold then apply a generated OS delta", func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		fleetName := "delta-os-hold"
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, nil))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")
		skipGenerateAndHold(harness, deviceId)

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)

		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, nil))).To(Succeed())

		waitSettledOSDeltaUpdate(harness, fleetName, deviceId, v2Image, true)
	})

	It("When generateDelta is false it should write the new OS spec without FleetDeltaPreparing", func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		fleetName := "delta-os-skip-generate"
		policy := &v1beta1.RolloutPolicy{GenerateDelta: lo.ToPtr(false)}
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, policy))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, policy))).To(Succeed())

		Consistently(func() bool {
			fleet, err := harness.GetFleet(fleetName)
			if err != nil {
				return true
			}
			failIfDeltaPreparingFailed(fleet)
			return fleetConditionTrue(fleet, v1beta1.ConditionTypeFleetDeltaPreparing)
		}, "30s", POLLING).Should(BeFalse())

		harness.WaitForDeviceContents(deviceId, "device spec is V2 and UpToDate without waiting for generation", func(device *v1beta1.Device) bool {
			return deviceOsImage(device) == v2Image &&
				device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
		}, TIMEOUT)
	})

	It("When maxWaitForDelta expires it should roll out the OS without a deltaImage hint", func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		fleetName := "delta-os-deadline"
		policy := &v1beta1.RolloutPolicy{MaxWaitForDelta: lo.ToPtr(v1beta1.Duration("1s"))}
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, policy))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")
		skipGenerateAndHold(harness, deviceId)

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, policy))).To(Succeed())

		waitSettledOSDeltaUpdate(harness, fleetName, deviceId, v2Image, false)
	})

	It("When a standalone device OS spec changes with a writable delta target it should delay render then hint", Label("standalone"), func() {
		harness := e2e.GetWorkerHarness()
		createWritableDeltaRepo(harness)

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus()
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")
		skipGenerateAndHold(harness, deviceId)

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		Expect(harness.UpdateDeviceWithRetries(deviceId, func(device *v1beta1.Device) {
			if device.Spec == nil {
				device.Spec = &v1beta1.DeviceSpec{}
			}
			device.Spec.Os = &v1beta1.DeviceOsSpec{Image: v2Image}
		})).To(Succeed())

		waitSettledOSDeltaUpdate(harness, "", deviceId, v2Image, true)
	})

	It("When there is no writable delta target it should update OS without a hint", func() {
		harness := e2e.GetWorkerHarness()

		fleetName := "delta-os-no-target"
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.Base, nil))).To(Succeed())

		deviceId, _ := harness.EnrollAndWaitForOnlineStatus(map[string]string{fleetLabelKey: fleetName})
		waitDeviceUpToDate(harness, deviceId, "device UpToDate on current OS")

		v2Image := harness.GetDeviceImageRefForFleet(auxSvcs.Registry.Host, auxSvcs.Registry.Port, util.DeviceTags.V2)
		Expect(harness.CreateOrUpdateTestFleet(fleetName, osFleetSpec(harness, fleetName, util.DeviceTags.V2, nil))).To(Succeed())

		Consistently(func() bool {
			fleet, err := harness.GetFleet(fleetName)
			if err != nil {
				return true
			}
			failIfDeltaPreparingFailed(fleet)
			return fleetConditionTrue(fleet, v1beta1.ConditionTypeFleetDeltaPreparing)
		}, "30s", POLLING).Should(BeFalse())

		harness.WaitForDeviceContents(deviceId, "device UpToDate on V2 without deltaImage", func(device *v1beta1.Device) bool {
			if deviceOsImage(device) != v2Image {
				return false
			}
			if device.Status == nil || device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
				return false
			}
			rendered := tryRenderedDevice(harness, deviceId)
			return renderedOsImage(rendered) == v2Image && renderedDeltaImage(rendered) == ""
		}, LONGTIMEOUT)
	})
})

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

func skipGenerateAndHold(harness *e2e.Harness, deviceId string) {
	infra.SkipIfOciDeltaUnavailable(harness.Context, setup.GetDefaultProviders())
	device, err := harness.GetDevice(deviceId)
	Expect(err).ToNot(HaveOccurred())
	if device.Status == nil || device.Status.Capabilities == nil || device.Status.Capabilities.DeltaEligible == nil || !*device.Status.Capabilities.DeltaEligible {
		Skip("device status.capabilities.deltaEligible is not true")
	}
}

func waitDeviceUpToDate(harness *e2e.Harness, deviceId, description string) {
	harness.WaitForDeviceContents(deviceId, description, func(device *v1beta1.Device) bool {
		return device.Status != nil && device.Status.Updated.Status == v1beta1.DeviceUpdatedStatusUpToDate
	}, TIMEOUT)
}

func waitSettledOSDeltaUpdate(harness *e2e.Harness, fleetName, deviceId, v2Image string, wantDelta bool) {
	lastMsg := ""
	lastChange := time.Now()
	Eventually(func() error {
		if fleetName != "" {
			fleet, err := harness.GetFleet(fleetName)
			if err != nil {
				return err
			}
			failIfDeltaPreparingFailed(fleet)
			var conditions []v1beta1.Condition
			var gen *v1beta1.DeltaGenerationStatus
			if fleet != nil && fleet.Status != nil {
				conditions = fleet.Status.Conditions
				gen = fleet.Status.DeltaGeneration
			}
			if err := preparingStillRunning("fleet", conditions, v1beta1.ConditionTypeFleetDeltaPreparing, gen, &lastMsg, &lastChange); err != nil {
				return err
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
		if err := preparingStillRunning("device", device.Status.Conditions, v1beta1.ConditionTypeDeviceDeltaPreparing, device.Status.DeltaGeneration, &lastMsg, &lastChange); err != nil {
			return err
		}
		if device.Status.Updated.Status != v1beta1.DeviceUpdatedStatusUpToDate {
			return fmt.Errorf("device %s updated status is %s", deviceId, device.Status.Updated.Status)
		}
		if wantDelta && device.Status.Os.LastDelta != nil && device.Status.Os.LastDelta.FallbackReason != nil {
			return fmt.Errorf("device %s fell back: %s", deviceId, *device.Status.Os.LastDelta.FallbackReason)
		}

		rendered := tryRenderedDevice(harness, deviceId)
		if rendered == nil {
			return fmt.Errorf("device %s has no rendered spec", deviceId)
		}
		if renderedOsImage(rendered) != v2Image {
			return fmt.Errorf("device %s rendered OS is %q", deviceId, renderedOsImage(rendered))
		}
		delta := renderedDeltaImage(rendered)
		if wantDelta && (delta == "" || delta == v2Image) {
			return fmt.Errorf("device %s missing OS delta hint", deviceId)
		}
		if !wantDelta && delta != "" {
			return fmt.Errorf("device %s has unexpected OS delta hint %q", deviceId, delta)
		}
		return nil
	}, GENERATE_CAP, POLLING).Should(BeNil())
}

func preparingStillRunning(kind string, conditions []v1beta1.Condition, condType v1beta1.ConditionType, gen *v1beta1.DeltaGenerationStatus, lastMsg *string, lastChange *time.Time) error {
	preparing, _ := preparingTrueMessage(conditions, condType)
	if !preparing {
		return nil
	}
	msg := generationProgressKey(gen)
	if msg != *lastMsg {
		GinkgoWriter.Printf("%s delta generation: %s\n", kind, msg)
		*lastMsg = msg
		*lastChange = time.Now()
	}
	if time.Since(*lastChange) > progressStall {
		return fmt.Errorf("%s delta generation stalled for %s at %q", kind, progressStall, msg)
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
	var b strings.Builder
	fmt.Fprintf(&b, "%d/%d", st.Completed, st.Total)
	if st.LastUpdated != nil {
		fmt.Fprintf(&b, " t=%d", st.LastUpdated.Unix())
	}
	if st.Phase != nil {
		fmt.Fprintf(&b, " %s", *st.Phase)
	}
	if st.Percent != nil {
		fmt.Fprintf(&b, " %d%%", *st.Percent)
	}
	if st.ItemsDone != nil && st.ItemsTotal != nil {
		fmt.Fprintf(&b, " items=%d/%d", *st.ItemsDone, *st.ItemsTotal)
	}
	if st.BytesDone != nil && st.BytesTotal != nil {
		fmt.Fprintf(&b, " bytes=%d/%d", *st.BytesDone, *st.BytesTotal)
	}
	return b.String()
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

func tryRenderedDevice(harness *e2e.Harness, deviceId string) *v1beta1.Device {
	resp, err := harness.Client.GetRenderedDeviceWithResponse(harness.Context, deviceId, nil)
	if err != nil || resp == nil {
		return nil
	}
	return resp.JSON200
}
