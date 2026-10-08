package agent_test

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	agentcfg "github.com/flightctl/flightctl/internal/agent/config"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

	// alertPushPeriodicWindow bounds the wait for the next periodic status push,
	// which is due within one interval of the preceding observation.
	alertPushPeriodicWindow = "2m"

	// alertPushPinPolling is the poll interval used while waiting for a periodic
	// status push. It is short because the specs use the moment that wait
	// returns as the start of a fresh periodic interval: a coarse poll would
	// spend part of that interval before the following observation even starts.
	alertPushPinPolling = 1 * time.Second

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

	// alertPushThreshold is the disk usage percentage above which the alert
	// fires. The monitor compares the share of used blocks on the filesystem
	// holding alertPushDiskPath with a strict greater-than. Thirty percent is
	// deliberately well clear of both ends: a threshold near zero would fire
	// the moment the monitor starts and be indistinguishable from a periodic
	// push, while a threshold near the top would need the test to fill most of
	// the device. The device is expected to start below it; prepare asserts
	// that rather than assuming it.
	alertPushThreshold = 30

	// alertPushHeadroom is the margin, in usage percentage points, kept on each
	// side of the threshold. The base fill brings the device to
	// alertPushThreshold-alertPushHeadroom so the baseline sits clearly below
	// the rule, and the trigger fill takes it to
	// alertPushThreshold+alertPushHeadroom so a single sample cannot land back
	// on the quiet side and reset the firing window.
	alertPushHeadroom = 5

	// alertPushDiskPath is the directory the disk monitor watches and the
	// directory the fill files are written to, so the monitored filesystem and
	// the filled filesystem are the same one by construction. It is /var/tmp
	// because the device runs a bootc/ostree image where /usr is read-only;
	// /var is the writable state partition, and /var/tmp (unlike /tmp) is a
	// real on-disk path rather than a tmpfs.
	alertPushDiskPath = "/var/tmp"

	// alertPushBaseFill holds the bulk of the garbage written to the device. It
	// is written once during preparation and stops short of the threshold, so
	// the slow part of filling the disk happens before any timed observation.
	// A device that already sits at or above that target needs no base fill,
	// in which case the file is never created and nothing below depends on it.
	alertPushBaseFill = "/var/tmp/flightctl-e2e-disk-fill.base"

	// alertPushTriggerFill is the small top-up that actually crosses the
	// threshold. With the base fill already in place it is a fraction of the
	// device, so writing it costs seconds rather than minutes and the
	// observation windows start promptly after the alert becomes possible.
	alertPushTriggerFill = "/var/tmp/flightctl-e2e-disk-fill.trigger"

	// The service recomputes the device summary from the reported resource
	// statuses on every status write, so these are the summary messages a client
	// observes rather than the text the agent sends.
	diskCriticalSummaryInfo = "Disk utilization has reached a critical level."
	diskWarningSummaryInfo  = "Disk utilization has reached a warning level."
)

// alertStatusSnapshot is a single coherent observation of everything these specs
// assert on. Reading it through one helper keeps each Eventually/Consistently
// poll self-consistent instead of mixing values from separate reads.
//
// RenderedVersion is the marker for a full status replace. The alert-driven
// push is a JSON Patch built by diffing the critical exporters, which only ever
// covers the resource fields, so a change to the reported rendered version can
// only have arrived on a complete status update.
type alertStatusSnapshot struct {
	Cpu             v1beta1.DeviceResourceStatusType
	Disk            v1beta1.DeviceResourceStatusType
	Memory          v1beta1.DeviceResourceStatusType
	Summary         v1beta1.DeviceSummaryStatusType
	Info            string
	RenderedVersion string
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
	})

	AfterEach(func() {
		// The worker harness keeps one enrolled device for the whole suite, so
		// the alert rules and the garbage files outlive the spec that created
		// them unless they are taken back off here.
		Expect(removeDiskFill(harness)).To(Succeed())
		Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
			if device.Spec == nil {
				return
			}
			device.Spec.Resources = nil
		})).To(Succeed())
		Expect(harness.SetAgentConfig(cfgBak)).To(Succeed())
		Expect(restartFlightctlAgentAndWait(harness)).To(Succeed())
	})

	Context("when the agent runs with a long periodic status push interval", func() {
		It("When a critical disk resource alert fires it should push the device status immediately and keep the full status sync running",
			Label("sanity", "agent", "alert-status-push", "slow"), func() {
				artifactDir, err := harness.SetupScenario(deviceID, "alert-status-push-critical")
				Expect(err).ToNot(HaveOccurred())

				prepareAlertStatusPushDevice(harness, deviceID, v1beta1.ResourceAlertSeverityTypeCritical)

				By(fmt.Sprintf("filling the disk above %d%% so the critical alert transitions to firing", alertPushThreshold))
				fillDiskAboveThreshold(harness, artifactDir)

				By("verifying the service reports the critical resource status within seconds")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushImmediateWindow, LONGPOLLING).
					Should(SatisfyAll(
						// The pushed fields carry the resource state the monitor observed.
						HaveField("Disk", Equal(v1beta1.DeviceResourceStatusCritical)),
						HaveField("Summary", Equal(v1beta1.DeviceSummaryStatusError)),
						HaveField("Info", Equal(diskCriticalSummaryInfo)),
						// Resources without an alert rule keep their healthy state.
						HaveField("Cpu", Equal(v1beta1.DeviceResourceStatusHealthy)),
						HaveField("Memory", Equal(v1beta1.DeviceResourceStatusHealthy)),
					), "critical alert should update device status well before the next periodic sync")

				By("verifying the full status sync keeps running and carries the alert state")
				// The alert-driven push is a patch over the resource fields only,
				// so it cannot carry a rendered version. Waiting for the device to
				// report a new one therefore waits for a complete status update
				// performed after the alert push.
				Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
					if device.Spec == nil {
						device.Spec = &v1beta1.DeviceSpec{}
					}
					device.Spec.Resources = &[]v1beta1.ResourceMonitor{
						diskResourceMonitor(v1beta1.ResourceAlertSeverityTypeCritical),
						idleMemoryResourceMonitor(),
					}
				})).To(Succeed())

				By("verifying the complete status carried every field, not only the alert fields")
				device, err := harness.GetDevice(deviceID)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status).ToNot(BeNil())
				Expect(device.Status.Resources.Cpu).To(Equal(v1beta1.DeviceResourceStatusHealthy))
				Expect(device.Status.Resources.Disk).To(Equal(v1beta1.DeviceResourceStatusCritical))
				Expect(device.Status.Resources.Memory).To(Equal(v1beta1.DeviceResourceStatusHealthy))
				Expect(device.Status.Summary.Status).To(Equal(v1beta1.DeviceSummaryStatusError))
				Expect(device.Status.SystemInfo.BootID).ToNot(BeEmpty())
				Expect(device.Status.Os.Image).ToNot(BeEmpty())
				Expect(device.Status.Config.RenderedVersion).ToNot(BeEmpty())
				Expect(string(device.Status.Updated.Status)).ToNot(BeEmpty())

				By("removing the garbage files and verifying the device recovers")
				Expect(removeDiskFill(harness)).To(Succeed())
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(HaveField("Disk", Equal(v1beta1.DeviceResourceStatusHealthy)),
						"clearing the critical alert should return the device to a healthy disk status")

				Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
			})

		It("When only a warning disk resource alert fires it should not push status until the next periodic sync",
			Label("sanity", "agent", "alert-status-push", "negative", "slow"), func() {
				artifactDir, err := harness.SetupScenario(deviceID, "alert-status-push-warning")
				Expect(err).ToNot(HaveOccurred())

				prepareAlertStatusPushDevice(harness, deviceID, v1beta1.ResourceAlertSeverityTypeWarning)

				// The observations below have to sit inside a single periodic
				// interval to mean anything, so the spec first establishes where
				// that interval starts. Filling the disk changes only a
				// warning-level alert, which is not a critical change, so the
				// moment the service reports it is the moment a periodic status
				// push landed. Every later observation is anchored on such a
				// moment, which leaves more than half an interval of margin
				// instead of relying on the periodic cadence falling favourably.
				By(fmt.Sprintf("filling the disk above %d%% so the warning alert transitions to firing", alertPushThreshold))
				fillDiskAboveThreshold(harness, artifactDir)

				By("waiting for the periodic status sync that carries the warning")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(SatisfyAll(
						HaveField("Disk", Equal(v1beta1.DeviceResourceStatusWarning)),
						HaveField("Summary", Equal(v1beta1.DeviceSummaryStatusDegraded)),
						HaveField("Info", Equal(diskWarningSummaryInfo)),
					), "periodic sync should report the warning resource status")

				By("verifying clearing the warning triggers no immediate status push")
				removeDiskTriggerFill(harness)
				Consistently(alertStatusSnapshotFunc(harness, deviceID), alertPushQuietWindow, LONGPOLLING).
					Should(HaveField("Disk", Equal(v1beta1.DeviceResourceStatusWarning)),
						"a warning alert clearing should not trigger an immediate status push")

				By("waiting for the periodic status sync that carries the cleared warning")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(SatisfyAll(
						HaveField("Disk", Equal(v1beta1.DeviceResourceStatusHealthy)),
						HaveField("Summary", Equal(v1beta1.DeviceSummaryStatusOnline)),
					), "periodic sync should report the cleared warning")

				By("verifying a firing warning triggers no immediate status push")
				fillDiskAboveThreshold(harness, artifactDir)
				Consistently(alertStatusSnapshotFunc(harness, deviceID), alertPushQuietWindow, LONGPOLLING).
					Should(SatisfyAll(
						HaveField("Disk", Equal(v1beta1.DeviceResourceStatusHealthy)),
						HaveField("Summary", Equal(v1beta1.DeviceSummaryStatusOnline)),
					), "warning alert should not trigger an immediate status push")

				By("verifying the warning reaches the service on the next periodic status sync")
				Eventually(alertStatusSnapshotFunc(harness, deviceID), alertPushPeriodicWindow, alertPushPinPolling).
					Should(SatisfyAll(
						HaveField("Disk", Equal(v1beta1.DeviceResourceStatusWarning)),
						HaveField("Summary", Equal(v1beta1.DeviceSummaryStatusDegraded)),
						HaveField("Info", Equal(diskWarningSummaryInfo)),
					), "periodic sync should report the warning resource status")

				Expect(harness.CaptureStandardEvidence(artifactDir, deviceID)).To(Succeed())
			})
	})
})

// prepareAlertStatusPushDevice puts the device into the state both specs start
// from: a periodic status push interval long enough to tell the two push paths
// apart, a single disk alert rule of the given severity, a disk filled to just
// below the threshold, and a settled baseline on the service.
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
// the alert rule below is installed, and the status the service holds was
// pushed by the restarted agent.
func prepareAlertStatusPushDevice(harness *e2e.Harness, deviceID string, severity v1beta1.ResourceAlertSeverityType) {
	GinkgoHelper()

	By("verifying the device starts below the alert threshold")
	// Nothing in the specs can create free space, so a device that is already
	// over the threshold has to be reported as such rather than silently
	// producing an alert that fires before the test triggers it.
	Expect(removeDiskFill(harness)).To(Succeed())
	baseline, err := deviceDiskUsagePercent(harness)
	Expect(err).ToNot(HaveOccurred())
	Expect(baseline).To(BeNumerically("<=", alertPushThreshold-alertPushHeadroom),
		fmt.Sprintf("device %s needs to start at most %d%% full on %s to leave room for the fill, but it is %d%% full",
			deviceID, alertPushThreshold-alertPushHeadroom, alertPushDiskPath, baseline))

	By(fmt.Sprintf("setting the agent periodic status push interval to %s", alertPushStatusInterval))
	Expect(harness.UpdateAgentConfigWith(func(cfg *agentcfg.Config) {
		cfg.StatusUpdateInterval = util.Duration(alertPushStatusInterval)
		// The jitter defaults to the full interval, which would place the first
		// periodic push anywhere in [0, interval). Pinning it to zero starts the
		// periodic cadence at the agent restart and keeps it predictable.
		cfg.StatusUpdateJitter = lo.ToPtr(util.Duration(0))
	})).To(Succeed())

	By(fmt.Sprintf("adding a %s disk alert rule above %d%% on %s to the device spec", severity, alertPushThreshold, alertPushDiskPath))
	Expect(harness.UpdateDeviceAndWaitForVersion(deviceID, func(device *v1beta1.Device) {
		if device.Spec == nil {
			device.Spec = &v1beta1.DeviceSpec{}
		}
		device.Spec.Resources = &[]v1beta1.ResourceMonitor{diskResourceMonitor(severity)}
	})).To(Succeed())

	By(fmt.Sprintf("filling the disk to just below %d%% so the trigger fill is a small write", alertPushThreshold))
	// A zero here means the device was already at or above the baseline target
	// and no base file was written. That is a legitimate starting point, not a
	// failure: the baseline check below still has to pass, and the trigger fill
	// is sized against whatever space is actually left.
	written, err := fillDiskTo(harness, alertPushBaseFill, alertPushThreshold-alertPushHeadroom)
	Expect(err).ToNot(HaveOccurred())
	GinkgoWriter.Printf("Base disk fill wrote %d MiB to %s\n", written, alertPushBaseFill)

	// Reaching the new rendered version already required a status push from the
	// restarted agent, so this is a guard on the content of that push rather
	// than a wait for one to happen.
	By("verifying the settled baseline before triggering the alert")
	Eventually(alertStatusSnapshotFunc(harness, deviceID), TIMEOUT, LONGPOLLING).
		Should(SatisfyAll(
			HaveField("Disk", Equal(v1beta1.DeviceResourceStatusHealthy)),
			HaveField("Cpu", Equal(v1beta1.DeviceResourceStatusHealthy)),
			HaveField("Memory", Equal(v1beta1.DeviceResourceStatusHealthy)),
			HaveField("Summary", Equal(v1beta1.DeviceSummaryStatusOnline)),
		), "device should report a healthy baseline before the alert fires")
}

// diskResourceMonitor builds a disk resource monitor on alertPushDiskPath
// carrying exactly one alert rule of the given severity.
func diskResourceMonitor(severity v1beta1.ResourceAlertSeverityType) v1beta1.ResourceMonitor {
	GinkgoHelper()

	monitor := v1beta1.ResourceMonitor{}
	Expect(monitor.FromDiskResourceMonitorSpec(v1beta1.DiskResourceMonitorSpec{
		MonitorType:      "Disk",
		Path:             alertPushDiskPath,
		SamplingInterval: alertPushSamplingInterval.String(),
		AlertRules: []v1beta1.ResourceAlertRule{
			{
				Severity:    severity,
				Duration:    alertPushAlertDuration,
				Percentage:  alertPushThreshold,
				Description: fmt.Sprintf("Disk usage on %s is above %d%%.", alertPushDiskPath, alertPushThreshold),
			},
		},
	})).To(Succeed())
	return monitor
}

// idleMemoryResourceMonitor builds a memory monitor that cannot fire, so adding
// it to the device spec changes the rendered version without changing any
// reported resource status.
//
// The monitor compares a usage percentage with a strict greater-than, and the
// schema caps an alert rule at a hundred percent, so a rule at the cap has no
// value that satisfies it. The disk monitor alongside it is sent unchanged, and
// the agent only touches an alert rule it finds different, so the disk alert
// carries on firing across the spec update.
func idleMemoryResourceMonitor() v1beta1.ResourceMonitor {
	GinkgoHelper()

	monitor := v1beta1.ResourceMonitor{}
	Expect(monitor.FromMemoryResourceMonitorSpec(v1beta1.MemoryResourceMonitorSpec{
		MonitorType:      "Memory",
		SamplingInterval: alertPushSamplingInterval.String(),
		AlertRules: []v1beta1.ResourceAlertRule{
			{
				Severity:    v1beta1.ResourceAlertSeverityTypeWarning,
				Duration:    alertPushAlertDuration,
				Percentage:  100,
				Description: "Memory usage is above 100%.",
			},
		},
	})).To(Succeed())
	return monitor
}

// alertStatusSnapshotFunc adapts readAlertStatusSnapshot for Eventually and
// Consistently, which retry on the returned error.
func alertStatusSnapshotFunc(harness *e2e.Harness, deviceID string) func() (alertStatusSnapshot, error) {
	return func() (alertStatusSnapshot, error) {
		return readAlertStatusSnapshot(harness, deviceID)
	}
}

// readAlertStatusSnapshot reads the resource status, summary and reported
// rendered version the service currently holds for the device.
func readAlertStatusSnapshot(harness *e2e.Harness, deviceID string) (alertStatusSnapshot, error) {
	device, err := harness.GetDevice(deviceID)
	if err != nil {
		return alertStatusSnapshot{}, fmt.Errorf("reading device %s: %w", deviceID, err)
	}
	if device.Status == nil {
		return alertStatusSnapshot{}, fmt.Errorf("device %s has no status", deviceID)
	}

	snapshot := alertStatusSnapshot{
		Cpu:             device.Status.Resources.Cpu,
		Disk:            device.Status.Resources.Disk,
		Memory:          device.Status.Resources.Memory,
		Summary:         device.Status.Summary.Status,
		RenderedVersion: device.Status.Config.RenderedVersion,
	}
	if device.Status.Summary.Info != nil {
		snapshot.Info = *device.Status.Summary.Info
	}
	return snapshot, nil
}

// fillDiskAboveThreshold writes the trigger file that takes the device over the
// alert threshold, and confirms the usage the monitor will sample is actually
// over it.
//
// A completed write only proves the bytes were requested, not that the agent
// can see them. Confirming the usage on the device, with the arithmetic the
// monitor itself uses, keeps a fill regression from surfacing as an unexplained
// device status timeout further down the spec, and means the observation
// windows below start from a device that is already over the threshold.
func fillDiskAboveThreshold(harness *e2e.Harness, artifactDir string) {
	GinkgoHelper()

	written, err := fillDiskTo(harness, alertPushTriggerFill, alertPushThreshold+alertPushHeadroom)
	Expect(err).ToNot(HaveOccurred())
	Expect(written).To(BeNumerically(">", 0),
		"trigger fill should have written garbage to cross the alert threshold")

	// The listing is evidence, not an assertion. The base fill is skipped
	// entirely when the device already sits above the baseline target, so its
	// file need not exist, and ls exits non-zero on a missing operand. Folding
	// stderr into the output keeps the "No such file" line in the evidence,
	// and the trailing true keeps a missing file from failing the spec.
	_, err = harness.RunVMCommandWithEvidence(artifactDir, "vm_disk_fill.txt",
		fmt.Sprintf("df -h %s; ls -l %s %s 2>&1 || true", alertPushDiskPath, alertPushBaseFill, alertPushTriggerFill))
	Expect(err).ToNot(HaveOccurred())

	usage, err := deviceDiskUsagePercent(harness)
	Expect(err).ToNot(HaveOccurred())
	Expect(usage).To(BeNumerically(">", alertPushThreshold),
		"garbage files should drive the device disk usage above the alert threshold")
}

// fillDiskTo writes a garbage file so that the usage of the filesystem holding
// alertPushDiskPath reaches at least targetPercent, and returns the number of
// mebibytes written. Any previous content of the file is removed first so the
// amount to write is computed against the space the rest of the test is using.
//
// The arithmetic mirrors the agent's disk monitor exactly: it statfs(2)s the
// monitored path and reports 100 * (1 - available/total) rounded to the nearest
// integer, where available is the count of blocks available to unprivileged
// users. Sizing the write off the same two numbers means the percentage this
// targets is the one the alert rule is evaluated against.
func fillDiskTo(harness *e2e.Harness, path string, targetPercent int) (int, error) {
	if harness == nil {
		return 0, fmt.Errorf("harness is nil")
	}

	script := fmt.Sprintf(`
set -eu
rm -f %[1]s
sync
set -- $(stat -f -c '%%S %%b %%a' %[2]s)
frag=$1
blocks=$2
avail=$3
total_bytes=$((blocks * frag))
avail_bytes=$((avail * frag))
# Blocks still available once the filesystem is targetPercent full.
target_avail=$((total_bytes / 100 * (100 - %[3]d)))
needed=$((avail_bytes - target_avail))
if [ "$needed" -le 0 ]; then
  echo 0
  exit 0
fi
count=$((needed / 1048576 + 1))
dd if=/dev/zero of=%[1]s bs=1M count="$count" status=none
sync
echo "$count"
`, path, alertPushDiskPath, targetPercent)

	out, err := harness.RunScriptOnVM(script)
	if err != nil {
		return 0, fmt.Errorf("filling %s to %d%%: %w", path, targetPercent, err)
	}
	return parseTrailingInt(out.String(), fmt.Sprintf("disk fill of %s", path))
}

// deviceDiskUsagePercent reports the used share of the filesystem holding
// alertPushDiskPath, computed the way the agent's disk monitor computes it.
func deviceDiskUsagePercent(harness *e2e.Harness) (int, error) {
	if harness == nil {
		return 0, fmt.Errorf("harness is nil")
	}

	script := fmt.Sprintf(`
set -eu
set -- $(stat -f -c '%%b %%a' %[1]s)
blocks=$1
avail=$2
if [ "$blocks" -le 0 ]; then
  echo 0
  exit 0
fi
echo $(( ((blocks - avail) * 100 + blocks / 2) / blocks ))
`, alertPushDiskPath)

	out, err := harness.RunScriptOnVM(script)
	if err != nil {
		return 0, fmt.Errorf("sampling disk usage on the device: %w", err)
	}
	return parseTrailingInt(out.String(), "disk usage probe")
}

// removeDiskTriggerFill deletes only the garbage that crosses the threshold,
// leaving the device just below it so the alert clears without the base fill
// having to be written again.
func removeDiskTriggerFill(harness *e2e.Harness) {
	GinkgoHelper()

	Expect(removeDiskFillFiles(harness, alertPushTriggerFill)).To(Succeed())
}

// removeDiskFill deletes every garbage file these specs write. It is safe to
// call when nothing was ever written.
func removeDiskFill(harness *e2e.Harness) error {
	return removeDiskFillFiles(harness, alertPushBaseFill, alertPushTriggerFill)
}

func removeDiskFillFiles(harness *e2e.Harness, paths ...string) error {
	if harness == nil {
		return fmt.Errorf("harness is nil")
	}
	if len(paths) == 0 {
		return nil
	}

	script := fmt.Sprintf("rm -f %s\nsync\n", strings.Join(paths, " "))
	if _, err := harness.RunScriptOnVM(script); err != nil {
		return fmt.Errorf("removing disk fill files %s: %w", strings.Join(paths, " "), err)
	}
	return nil
}

// parseTrailingInt reads the integer a device probe script prints as its last
// word, tolerating any banner the SSH session prepends to it.
func parseTrailingInt(output, what string) (int, error) {
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return 0, fmt.Errorf("%s returned no output", what)
	}
	value := fields[len(fields)-1]
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parsing %s result %q from the device: %w", what, value, err)
	}
	return parsed, nil
}
