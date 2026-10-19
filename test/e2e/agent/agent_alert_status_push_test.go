package agent_test

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	agentresource "github.com/flightctl/flightctl/internal/agent/device/resource"
	servicecommon "github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	"github.com/samber/lo"
)

const (
	// alertPushStatusInterval is the periodic status push interval these specs
	// configure. One minute is long enough to tell the two push paths apart —
	// an alert-driven push lands within seconds, so it is comfortably ahead of
	// the next periodic sync — while keeping the wait for that periodic sync
	// short. It stays below the five minute disconnect timeout for margin,
	// though that timeout is driven by the agent's rendered-device long poll,
	// which heartbeats far more often than the status push interval, rather
	// than by this value.
	alertPushStatusInterval = 1 * time.Minute

	// alertPushImmediateWindow bounds how long an alert-driven push may take to
	// surface on the service. The clock starts from a device already measured
	// above the threshold, so the expected cost is one sampling interval to
	// start the firing window, the samples spanning alertPushAlertDuration, and
	// the patch — under ten seconds. The allowance is three times that, and
	// still only half the periodic interval, so the observation cannot be
	// explained away by a periodic sync that happened to come due.
	alertPushImmediateWindow = "30s"

	// alertPushQuietWindow is how long a warning-level alert transition is
	// observed making no difference to the reported status. Every use of it in
	// these specs starts immediately after an observed periodic push, so the
	// window sits at the beginning of a fresh periodic interval with more than
	// half the interval still to spare.
	alertPushQuietWindow = "20s"

	// alertPushPeriodicWindow bounds the wait for a status change that only
	// the periodic sync can deliver. A wait anchored on a push the spec has
	// just observed is due the next periodic push within one
	// alertPushStatusInterval of that observation; the one wait that starts
	// from an unobserved phase — the first warning delivery in the warning
	// spec — can additionally sit a full sampling and firing latency behind.
	// Ninety seconds, one and a half intervals, covers both with margin to
	// spare while still failing a periodic sync that runs at half again its
	// configured rate. The two minute window this replaces could not tell a
	// one minute sync from a two minute one.
	alertPushPeriodicWindow = "90s"

	// alertPushPinPolling is the poll interval used while waiting for a periodic
	// status push. The harness' one second poll is short because the specs use
	// the moment that wait returns as the start of a fresh periodic interval: a
	// coarse poll would spend part of that interval before the following
	// observation even starts.
	alertPushPinPolling = e2e.POLLINGLONG

	// alertPushSamplingInterval is the resource monitor sampling interval, kept
	// short so an alert edge transition is detected promptly.
	alertPushSamplingInterval = 2 * time.Second

	// alertPushAlertDuration is how long usage must stay above the threshold
	// before the alert starts firing. The API rejects a rule whose duration is
	// not strictly greater than the monitor sampling interval, so this is the
	// smallest value the schema allows next to alertPushSamplingInterval — the
	// alert fires on the second sample above the threshold, a handful of
	// seconds into the fill and far inside every observation window below.
	alertPushAlertDuration = "3s"

	// alertPushWarningThreshold is the disk usage percentage above which the
	// warning-severity rule fires. The monitor compares the share of used
	// blocks on the filesystem holding alertPushDiskPath with a strict
	// greater-than. The thresholds are deliberately well clear of both ends of
	// the scale: a threshold near zero would fire the moment the monitor starts
	// and be indistinguishable from a periodic push, while a threshold near the
	// top would need the test to fill most of the device.
	alertPushWarningThreshold = 30

	// alertPushCriticalThreshold is the disk usage percentage above which the
	// critical-severity rule fires, one band above the warning threshold. The
	// critical spec's monitor carries both rules and its base fill parks the
	// device in the band between them: above the warning rule so it fires
	// without triggering an immediate push, below the critical one so the
	// critical alert only fires when the trigger fill crosses it.
	alertPushCriticalThreshold = 40

	// alertPushHeadroom is the margin, in usage percentage points, kept on
	// each side of every threshold. The base fills stop short of their band
	// edge and the trigger fills overshoot their threshold, so a single sample
	// cannot land back on the quiet side and reset the firing window.
	alertPushHeadroom = 5

	// alertPushDiskPath is the directory the disk monitor watches and the
	// directory the fill files are written to, so the monitored filesystem and
	// the filled filesystem are the same one by construction. It is /var/tmp
	// because the device runs a bootc/ostree image where /usr is read-only;
	// /var is the writable state partition, and /var/tmp (unlike /tmp) is a
	// real on-disk path rather than a tmpfs.
	alertPushDiskPath = "/var/tmp"

	// alertPushBaseFill holds the bulk of the garbage written to the device. It
	// is written once during preparation and stops short of the threshold the
	// spec's trigger fill later crosses, so the slow part of filling the disk
	// happens before any timed observation. The critical spec parks the device
	// in the warning band between the two thresholds; the warning spec stops
	// below the warning threshold. A device that already sits at or above that
	// target needs no base fill, in which case the file is never created and
	// nothing below depends on it.
	alertPushBaseFill = "/var/tmp/flightctl-e2e-disk-fill.base"

	// alertPushTriggerFill is the small top-up that actually crosses the
	// threshold. With the base fill already in place it is a fraction of the
	// device, so writing it costs seconds rather than minutes and the
	// observation windows start promptly after the alert becomes possible.
	alertPushTriggerFill = "/var/tmp/flightctl-e2e-disk-fill.trigger"

	// Scenario names, which name the per-spec artifact directory, and the
	// evidence file the disk layout after a trigger fill is recorded in.
	alertPushCriticalScenario = "alert-status-push-critical"
	alertPushWarningScenario  = "alert-status-push-warning"
	alertPushDiskFillEvidence = "vm_disk_fill.txt"
)

// alertPushFillFiles lists every garbage file these specs write, so the cleanup
// that removes them cannot drift from the set that creates them.
var alertPushFillFiles = []string{alertPushBaseFill, alertPushTriggerFill}

// alertStatusSnapshot is a single coherent observation of everything these specs
// assert on. Reading it through one probe keeps each Eventually/Consistently
// poll self-consistent instead of mixing values from separate reads.
type alertStatusSnapshot struct {
	Cpu     v1beta1.DeviceResourceStatusType
	Disk    v1beta1.DeviceResourceStatusType
	Memory  v1beta1.DeviceResourceStatusType
	Summary v1beta1.DeviceSummaryStatusType
	Info    string
}

var _ = Describe("Agent immediate alert status push", func() {
	var (
		harness  *e2e.Harness
		deviceID string
		cfgBak   *agentcfg.Config
	)

	BeforeEach(func() {
		var err error

		harness = e2e.GetWorkerHarness()
		deviceID, _ = harness.EnrollAndWaitForOnlineStatus()
		Expect(strings.TrimSpace(deviceID)).ToNot(BeEmpty())

		cfgBak, err = harness.GetAgentConfig()
		Expect(err).ToNot(HaveOccurred())
		Expect(cfgBak).ToNot(BeNil())
	})

	AfterEach(func() {
		// The suite reverts the pool VM to its pristine snapshot and deletes
		// the enrolled device after every spec, so nothing here can outlive
		// this spec. The cleanup still runs so a failure surfaces in this
		// spec's output rather than being masked by the pool revert, and every
		// step is attempted even when an earlier one fails.
		cleanupErrs := []error{
			harness.RemoveVMFiles(alertPushFillFiles...),
		}
		if cfgBak != nil {
			cleanupErrs = append(cleanupErrs,
				harness.SetAgentConfig(cfgBak),
				restartFlightctlAgentAndWait(harness),
			)
		}
		Expect(errors.Join(cleanupErrs...)).To(Succeed())
	})

	Context("when the agent runs with a long periodic status push interval", func() {
		It("When a critical disk resource alert fires it should push the device status immediately and keep the periodic sync running",
			Label("agent", "alert-status-push", "slow"), func() {
				artifactDir, err := harness.SetupScenario(deviceID, alertPushCriticalScenario)
				Expect(err).ToNot(HaveOccurred())
				Expect(artifactDir).ToNot(BeEmpty())

				Expect(prepareAlertStatusPushDevice(harness, deviceID, v1beta1.ResourceAlertSeverityTypeCritical)).To(Succeed())

				// The base fill parks the device in the warning band, and a
				// warning transition never triggers an immediate push, so the
				// warning state the service reports was delivered by the
				// periodic sync. Reaching the new rendered version during
				// prepare already required a status push from the restarted
				// agent, so this is a guard on the content of the settled
				// state rather than a wait for a push to happen.
				By("verifying the settled warning baseline before triggering the critical alert")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), TIMEOUT, LONGPOLLING).
					Should(SatisfyAll(
						haveDiskAlertStatus(v1beta1.DeviceResourceStatusWarning, v1beta1.DeviceSummaryStatusDegraded, servicecommon.DiskIsWarning),
						haveHealthyCpuAndMemory(),
					), "device should report the warning baseline before the critical alert fires")

				By(fmt.Sprintf("filling the disk above %d%% so the critical alert transitions to firing", alertPushCriticalThreshold))
				written, err := fillDiskAboveThreshold(harness, artifactDir, v1beta1.ResourceAlertSeverityTypeCritical)
				Expect(err).ToNot(HaveOccurred())
				Expect(written).To(BeNumerically(">", 0),
					"trigger fill should have written garbage to cross the critical alert threshold")

				By("verifying the service reports the critical resource status within seconds")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushImmediateWindow, LONGPOLLING).
					Should(SatisfyAll(
						// The pushed fields carry the resource state the monitor observed.
						haveDiskAlertStatus(v1beta1.DeviceResourceStatusCritical, v1beta1.DeviceSummaryStatusError, servicecommon.DiskIsCritical),
						// Resources without a rule that can fire keep their healthy state.
						haveHealthyCpuAndMemory(),
					), "critical alert should update device status well before the next periodic sync")

				By("removing the trigger fill so the critical alert resolves")
				Expect(harness.RemoveVMFiles(alertPushTriggerFill)).To(Succeed())

				// A resolving critical alert is still a critical-severity edge,
				// so it is pushed immediately like the firing edge. The state
				// it leaves behind is the warning the base fill holds in place,
				// which is also the state the next step has to observe before
				// it can remove the base fill: dropping both fills at once
				// would resolve both alerts in one step and the immediate
				// critical push would carry the healthy state, leaving nothing
				// for the periodic sync to deliver.
				By("verifying the service reports the resolved critical resource status within seconds")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushImmediateWindow, LONGPOLLING).
					Should(SatisfyAll(
						haveDiskAlertStatus(v1beta1.DeviceResourceStatusWarning, v1beta1.DeviceSummaryStatusDegraded, servicecommon.DiskIsWarning),
						haveHealthyCpuAndMemory(),
					), "a resolving critical alert should update device status well before the next periodic sync")

				By("removing the base fill so the warning alert resolves")
				// A warning transition never triggers an immediate push and no
				// spec change is pending, so the cleared warning can only
				// reach the service on the next periodic status sync. This is
				// the observation that fails if the alert-driven push path
				// breaks the periodic sync: nothing else is left that could
				// deliver the healthy state.
				Expect(harness.RemoveVMFiles(alertPushBaseFill)).To(Succeed())

				By("verifying the periodic status sync keeps running and delivers the cleared warning")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(haveDiskAlertStatus(v1beta1.DeviceResourceStatusHealthy, v1beta1.DeviceSummaryStatusOnline, ""),
						"the periodic sync should keep running after the alert-driven pushes and report the cleared warning")

				By("verifying the complete status carried every field, not only the alert fields")
				device, err := harness.GetDevice(deviceID)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status).ToNot(BeNil())
				Expect(device.Status.Resources.Cpu).To(Equal(v1beta1.DeviceResourceStatusHealthy))
				Expect(device.Status.Resources.Disk).To(Equal(v1beta1.DeviceResourceStatusHealthy))
				Expect(device.Status.Resources.Memory).To(Equal(v1beta1.DeviceResourceStatusHealthy))
				Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusOnline))
				Expect(device.Status.SystemInfo.BootID).ToNot(BeEmpty())
				Expect(device.Status.Os.Image).ToNot(BeEmpty())
				Expect(device.Status.Config.RenderedVersion).ToNot(BeEmpty())
				Expect(string(device.Status.Updated.Status)).ToNot(BeEmpty())

				Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
			})

		It("When only a warning disk resource alert fires it should not push status until the next periodic sync",
			Label("agent", "alert-status-push", "negative", "slow"), func() {
				artifactDir, err := harness.SetupScenario(deviceID, alertPushWarningScenario)
				Expect(err).ToNot(HaveOccurred())
				Expect(artifactDir).ToNot(BeEmpty())

				Expect(prepareAlertStatusPushDevice(harness, deviceID, v1beta1.ResourceAlertSeverityTypeWarning)).To(Succeed())

				By("verifying the settled baseline before triggering the alert")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), TIMEOUT, LONGPOLLING).
					Should(SatisfyAll(
						haveDiskAlertStatus(v1beta1.DeviceResourceStatusHealthy, v1beta1.DeviceSummaryStatusOnline, ""),
						haveHealthyCpuAndMemory(),
					), "device should report a healthy baseline before the alert fires")

				// The observations below have to sit inside a single periodic
				// interval to mean anything, so the spec first establishes where
				// that interval starts. Filling the disk changes only a
				// warning-level alert, which is not a critical change, so the
				// moment the service reports it is the moment a periodic status
				// push landed. Every later observation is anchored on such a
				// moment, which leaves more than half an interval of margin
				// instead of relying on the periodic cadence falling favourably.
				By(fmt.Sprintf("filling the disk above %d%% so the warning alert transitions to firing", alertPushWarningThreshold))
				written, err := fillDiskAboveThreshold(harness, artifactDir, v1beta1.ResourceAlertSeverityTypeWarning)
				Expect(err).ToNot(HaveOccurred())
				Expect(written).To(BeNumerically(">", 0),
					"trigger fill should have written garbage to cross the alert threshold")

				By("waiting for the periodic status sync that carries the warning")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(haveDiskAlertStatus(v1beta1.DeviceResourceStatusWarning, v1beta1.DeviceSummaryStatusDegraded, servicecommon.DiskIsWarning),
						"periodic sync should report the warning resource status")

				By("verifying clearing the warning triggers no immediate status push")
				Expect(harness.RemoveVMFiles(alertPushTriggerFill)).To(Succeed())
				Consistently(alertStatusSnapshotFunc(harness, deviceID), alertPushQuietWindow, LONGPOLLING).
					Should(HaveField("Disk", Equal(v1beta1.DeviceResourceStatusWarning)),
						"a warning alert clearing should not trigger an immediate status push")

				By("waiting for the periodic status sync that carries the cleared warning")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(haveDiskAlertStatus(v1beta1.DeviceResourceStatusHealthy, v1beta1.DeviceSummaryStatusOnline, ""),
						"periodic sync should report the cleared warning")

				By("verifying a firing warning triggers no immediate status push")
				written, err = fillDiskAboveThreshold(harness, artifactDir, v1beta1.ResourceAlertSeverityTypeWarning)
				Expect(err).ToNot(HaveOccurred())
				Expect(written).To(BeNumerically(">", 0),
					"trigger fill should have written garbage to cross the alert threshold")
				Consistently(alertStatusSnapshotFunc(harness, deviceID), alertPushQuietWindow, LONGPOLLING).
					Should(haveDiskAlertStatus(v1beta1.DeviceResourceStatusHealthy, v1beta1.DeviceSummaryStatusOnline, ""),
						"warning alert should not trigger an immediate status push")

				By("verifying the warning reaches the service on the next periodic status sync")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(haveDiskAlertStatus(v1beta1.DeviceResourceStatusWarning, v1beta1.DeviceSummaryStatusDegraded, servicecommon.DiskIsWarning),
						"periodic sync should report the warning resource status")

				Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
			})
	})
})

// prepareAlertStatusPushDevice puts the device into the state both specs start
// from: a periodic status push interval long enough to tell the two push paths
// apart, the disk alert rules alertPushMonitorRules picks for the given
// severity, and a disk filled to that severity's base target. The caller
// asserts the settled baseline the service reports once this returns.
//
// The agent restart has to happen before the device spec is updated, not after.
// The restart only changes the agent's own configuration file, so the service
// still holds the status the agent pushed before it went down. Waiting on that
// status would prove nothing about the restarted agent, while the restart
// itself cannot be waited on directly: the unit is Type=notify and the agent
// signals readiness while writing its management banner, which happens early in
// bootstrap, ahead of the remaining bootstrap work, the start of the resource
// monitors, and the first reconciliation that applies the alert rules. Filling
// the disk against a device that only looks ready leaves the monitor either not
// yet sampling or still carrying its built-in defaults, which alert at 90% over
// thirty minutes and so never fire inside an observation window. The late
// bootstrap status push would also replace the full status after the fill is in
// place, which would hide an alert-driven push behind an ordinary one.
//
// Updating the spec last removes that gap without polling the agent's
// internals. The wait for the new rendered version is only satisfied once the
// agent has reported it, and the agent only reports a rendered version after a
// reconciliation that first handed the desired spec to the resource manager.
// So by the time it returns, bootstrap has finished, the monitors are sampling,
// the alert rules are installed, and the status the service holds was pushed by
// the restarted agent.
func prepareAlertStatusPushDevice(harness *e2e.Harness, deviceID string, severity v1beta1.ResourceAlertSeverityType) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(deviceID) == "" {
		return fmt.Errorf("device id must be non-empty")
	}

	By("verifying the device starts below the alert thresholds")
	// Nothing in the specs can create free space, so a device that is already
	// over the thresholds has to be reported as such rather than silently
	// producing an alert that fires before the test triggers it.
	if err := harness.RemoveVMFiles(alertPushFillFiles...); err != nil {
		return err
	}
	baseline, err := harness.VMDiskUsagePercent(alertPushDiskPath)
	if err != nil {
		return err
	}
	if baseline > alertPushWarningThreshold-alertPushHeadroom {
		return fmt.Errorf("device %s needs to start at most %d%% full on %s to leave room for the fill, but it is %d%% full",
			deviceID, alertPushWarningThreshold-alertPushHeadroom, alertPushDiskPath, baseline)
	}

	By(fmt.Sprintf("setting the agent periodic status push interval to %s", alertPushStatusInterval))
	if err := harness.UpdateAgentConfigWith(pinStatusPushInterval(alertPushStatusInterval)); err != nil {
		return fmt.Errorf("setting the agent status push interval to %s: %w", alertPushStatusInterval, err)
	}

	rules, baseFillTarget := alertPushMonitorRules(severity)
	By(fmt.Sprintf("adding the %s disk alert rules above %d%% on %s to the device spec", severity, alertPushThreshold(severity), alertPushDiskPath))
	monitor, err := diskResourceMonitor(rules...)
	if err != nil {
		return err
	}
	if err := harness.UpdateDeviceAndWaitForVersion(deviceID, e2e.SetDeviceResourceMonitors(monitor)); err != nil {
		return fmt.Errorf("adding the %s disk alert rules to device %s: %w", severity, deviceID, err)
	}

	By(fmt.Sprintf("filling the disk to %d%% so the trigger fill is a small write", baseFillTarget))
	// A zero here means the device was already at or above the base target and
	// no base file was written. That is a legitimate starting point, not a
	// failure: the baseline assertion the caller makes still has to pass, and
	// the trigger fill is sized against whatever space is actually left.
	written, err := harness.FillVMDisk(alertPushBaseFill, alertPushDiskPath, baseFillTarget)
	if err != nil {
		return err
	}
	GinkgoWriter.Printf("Base disk fill wrote %d MiB to %s\n", written, alertPushBaseFill)
	return nil
}

// pinStatusPushInterval returns an agent config mutator that sets the periodic
// status push interval and removes the jitter around it.
func pinStatusPushInterval(interval time.Duration) func(*agentcfg.Config) {
	return func(cfg *agentcfg.Config) {
		cfg.StatusUpdateInterval = util.Duration(interval)
		// The jitter defaults to the full interval, which would place the first
		// periodic push anywhere in [0, interval). Pinning it to zero starts the
		// periodic cadence at the agent restart and keeps it predictable.
		cfg.StatusUpdateJitter = lo.ToPtr(util.Duration(0))
	}
}

// alertPushMonitorRules returns the alert rules the monitor of the given
// severity carries, and the usage percentage the base fill targets. Both
// monitors carry the warning rule: in the warning spec it is the rule under
// test, while in the critical spec it sits below the critical rule so the base
// fill can park the device in the band between the two thresholds and the
// alert's later resolution can only be delivered by the periodic sync.
func alertPushMonitorRules(severity v1beta1.ResourceAlertSeverityType) ([]v1beta1.ResourceAlertRule, int) {
	rules := []v1beta1.ResourceAlertRule{
		alertPushRule(v1beta1.ResourceAlertSeverityTypeWarning, alertPushWarningThreshold),
	}
	baseFillTarget := alertPushWarningThreshold - alertPushHeadroom
	if severity == v1beta1.ResourceAlertSeverityTypeCritical {
		rules = append(rules, alertPushRule(severity, alertPushCriticalThreshold))
		baseFillTarget = alertPushCriticalThreshold - alertPushHeadroom
	}
	return rules, baseFillTarget
}

// alertPushThreshold returns the disk usage percentage the alert rule of the
// given severity fires above.
func alertPushThreshold(severity v1beta1.ResourceAlertSeverityType) int {
	if severity == v1beta1.ResourceAlertSeverityTypeCritical {
		return alertPushCriticalThreshold
	}
	return alertPushWarningThreshold
}

// alertPushRule builds one disk alert rule with the firing duration every
// monitor in these specs shares.
func alertPushRule(severity v1beta1.ResourceAlertSeverityType, percentage int) v1beta1.ResourceAlertRule {
	return v1beta1.ResourceAlertRule{
		Severity:    severity,
		Duration:    alertPushAlertDuration,
		Percentage:  float32(percentage),
		Description: fmt.Sprintf("Disk usage on %s is above %d%%.", alertPushDiskPath, percentage),
	}
}

// diskResourceMonitor builds a disk resource monitor on alertPushDiskPath
// carrying the given alert rules.
func diskResourceMonitor(rules ...v1beta1.ResourceAlertRule) (v1beta1.ResourceMonitor, error) {
	if len(rules) == 0 {
		return v1beta1.ResourceMonitor{}, fmt.Errorf("a disk resource monitor needs at least one alert rule")
	}
	monitor := v1beta1.ResourceMonitor{}
	if err := monitor.FromDiskResourceMonitorSpec(v1beta1.DiskResourceMonitorSpec{
		MonitorType:      agentresource.DiskMonitorType,
		Path:             alertPushDiskPath,
		SamplingInterval: alertPushSamplingInterval.String(),
		AlertRules:       rules,
	}); err != nil {
		return v1beta1.ResourceMonitor{}, fmt.Errorf("building a disk resource monitor on %s: %w", alertPushDiskPath, err)
	}
	return monitor, nil
}

// haveDiskAlertStatus builds a matcher over an alertStatusSnapshot asserting the
// reported disk resource status and the device summary the service derived from
// it. An empty info skips the summary message assertion, which is what the specs
// want where the summary carries no resource message.
//
// The service recomputes the device summary from the reported resource statuses
// on every status write, so the expected messages are the service's own
// constants rather than the text the agent sends.
func haveDiskAlertStatus(disk v1beta1.DeviceResourceStatusType, summary v1beta1.DeviceSummaryStatusType, info string) types.GomegaMatcher {
	fields := []types.GomegaMatcher{
		HaveField("Disk", Equal(disk)),
		HaveField("Summary", Equal(summary)),
	}
	if info != "" {
		fields = append(fields, HaveField("Info", Equal(info)))
	}
	return SatisfyAll(fields...)
}

// haveHealthyCpuAndMemory builds a matcher over an alertStatusSnapshot asserting
// the resources these specs never give a rule that can fire stay healthy, so a
// summary the service reports is attributable to the disk rule alone.
func haveHealthyCpuAndMemory() types.GomegaMatcher {
	return SatisfyAll(
		HaveField("Cpu", Equal(v1beta1.DeviceResourceStatusHealthy)),
		HaveField("Memory", Equal(v1beta1.DeviceResourceStatusHealthy)),
	)
}

// alertStatusSnapshotFunc returns a probe reading the resource status and
// summary the service currently holds for the device, shaped for Eventually and
// Consistently, which retry on the returned error.
func alertStatusSnapshotFunc(harness *e2e.Harness, deviceID string) func() (alertStatusSnapshot, error) {
	return func() (alertStatusSnapshot, error) {
		if harness == nil {
			return alertStatusSnapshot{}, fmt.Errorf("harness is nil")
		}
		if strings.TrimSpace(deviceID) == "" {
			return alertStatusSnapshot{}, fmt.Errorf("device id must be non-empty")
		}

		device, err := harness.GetDevice(deviceID)
		if err != nil {
			return alertStatusSnapshot{}, fmt.Errorf("reading device %s: %w", deviceID, err)
		}
		if device == nil || device.Status == nil {
			return alertStatusSnapshot{}, fmt.Errorf("device %s has no status", deviceID)
		}

		snapshot := alertStatusSnapshot{
			Cpu:     device.Status.Resources.Cpu,
			Disk:    device.Status.Resources.Disk,
			Memory:  device.Status.Resources.Memory,
			Summary: device.Status.Summary.Status,
		}
		if device.Status.Summary.Info != nil {
			snapshot.Info = *device.Status.Summary.Info
		}
		return snapshot, nil
	}
}

// fillDiskAboveThreshold writes the trigger file that takes the device over
// the alert threshold of the given severity, records the resulting disk layout
// as evidence, and confirms the usage the monitor will sample is actually over
// it. It returns the number of mebibytes written.
//
// A completed write only proves the bytes were requested, not that the agent
// can see them. Confirming the usage on the device, with the arithmetic the
// monitor itself uses, keeps a fill regression from surfacing as an unexplained
// device status timeout further down the spec, and means the observation
// windows start from a device that is already over the threshold.
func fillDiskAboveThreshold(harness *e2e.Harness, artifactDir string, severity v1beta1.ResourceAlertSeverityType) (int, error) {
	if harness == nil {
		return 0, fmt.Errorf("harness is nil")
	}
	if strings.TrimSpace(artifactDir) == "" {
		return 0, fmt.Errorf("artifact dir must be non-empty")
	}

	threshold := alertPushThreshold(severity)
	written, err := harness.FillVMDisk(alertPushTriggerFill, alertPushDiskPath, threshold+alertPushHeadroom)
	if err != nil {
		return 0, err
	}

	// The listing is evidence, not an assertion. The base fill is skipped
	// entirely when the device already sits above the baseline target, so its
	// file need not exist, and ls exits non-zero on a missing operand. Folding
	// stderr into the output keeps the "No such file" line in the evidence,
	// and the trailing true keeps a missing file from failing the spec.
	if _, err := harness.RunVMCommandWithEvidence(artifactDir, alertPushDiskFillEvidence,
		fmt.Sprintf("df -h %s; ls -l %s 2>&1 || true", alertPushDiskPath, strings.Join(alertPushFillFiles, " "))); err != nil {
		return written, fmt.Errorf("capturing the disk layout after filling %s: %w", alertPushTriggerFill, err)
	}

	usage, err := harness.VMDiskUsagePercent(alertPushDiskPath)
	if err != nil {
		return written, err
	}
	if usage <= threshold {
		return written, fmt.Errorf("garbage files should drive the device disk usage above the %s alert threshold of %d%%, but %s is %d%% full",
			severity, threshold, alertPushDiskPath, usage)
	}

	GinkgoWriter.Printf("Trigger disk fill wrote %d MiB to %s, taking %s to %d%%\n",
		written, alertPushTriggerFill, alertPushDiskPath, usage)
	return written, nil
}
