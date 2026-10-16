package agent_test

import (
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/test/harness/e2e"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	statusFixtureDir    = "/var/lib/flightctl/e2e-system-info"
	statusFixtureValue  = statusFixtureDir + "/value"
	statusFixtureDropIn = "/etc/flightctl/conf.d/99-e2e-status-value.yaml"
	statusFixtureKey    = "e2eStatusValue"
)

var _ = Describe("Agent System Info periodic values", Label("sanity"), func() {
	It("updates a file-backed custom value and recovers after an input error", Label("agent"), func() {
		harness := e2e.GetWorkerHarness()
		deviceID := harness.StartVMAndEnroll()

		By("Preparing a test-local custom value and selecting its image-baked collector")
		_, err := harness.RunScriptOnVM("install -d -m 0755 " + statusFixtureDir + " /etc/flightctl/conf.d")
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(cleanupStatusFixture, harness)
		Expect(harness.WriteAgentFile(statusFixtureValue, "A")).To(Succeed())
		Expect(harness.WriteAgentFile(statusFixtureDropIn, "system-info-custom: [siteName, emptyValue, "+statusFixtureKey+"]\n")).To(Succeed())
		Expect(harness.ReloadFlightCtlAgent()).To(Succeed())
		Eventually(readCustomInfoSnapshot, TIMEOUT, POLLING).WithArguments(harness, deviceID).Should(SatisfyAll(
			HaveField("Value", "A"),
			HaveField("Status", v1beta1.SystemInfoSourceStatusHealthy),
		))

		By("Recollecting a changed backing value without reloading the agent")
		Expect(harness.WriteAgentFile(statusFixtureValue, "B")).To(Succeed())
		var lastHealthyB customInfoSnapshot
		Eventually(func() (*customInfoSnapshot, error) {
			snapshot, err := readCustomInfoSnapshot(harness, deviceID)
			if snapshot != nil {
				lastHealthyB = *snapshot
			}
			return snapshot, err
		}, TIMEOUT, POLLING).Should(SatisfyAll(
			HaveField("Value", "B"),
			HaveField("Status", v1beta1.SystemInfoSourceStatusHealthy),
			HaveField("Message", BeEmpty()),
			HaveField("LastTransitionTime", Not(BeZero())),
		))

		By("Reporting an input error while retaining the last successful value")
		Eventually(func() (time.Time, error) {
			output, err := harness.RunScriptOnVM("date -u +%FT%TZ")
			if err != nil {
				return time.Time{}, err
			}
			return time.Parse(time.RFC3339, strings.TrimSpace(output.String()))
		}, "5s", POLLING).Should(BeTemporally(">", lastHealthyB.LastTransitionTime))
		Expect(harness.RemoveAgentFile(statusFixtureValue)).To(Succeed())
		Eventually(readCustomInfoSnapshot, TIMEOUT, POLLING).WithArguments(harness, deviceID).Should(SatisfyAll(
			HaveField("Value", lastHealthyB.Value),
			HaveField("Status", v1beta1.SystemInfoSourceStatusError),
			HaveField("Message", Not(BeEmpty())),
			HaveField("LastTransitionTime", SatisfyAll(Not(BeZero()), Not(BeTemporally("==", lastHealthyB.LastTransitionTime)))),
		))

		By("Recovering when the input is restored")
		Expect(harness.WriteAgentFile(statusFixtureValue, "C")).To(Succeed())
		Eventually(readCustomInfoSnapshot, TIMEOUT, POLLING).WithArguments(harness, deviceID).Should(SatisfyAll(
			HaveField("Value", "C"),
			HaveField("Status", v1beta1.SystemInfoSourceStatusHealthy),
			HaveField("Message", BeEmpty()),
		))
	})

	It("reports an updated VM hostname on the next collection", Label("agent"), func() {
		harness := e2e.GetWorkerHarness()
		deviceID := harness.StartVMAndEnroll()

		originalHostname, err := harness.GetVMHostname()
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(harness.SetVMHostname, originalHostname)

		readHostname := func() string {
			info := harness.GetDeviceSystemInfo(deviceID)
			if info == nil {
				return ""
			}
			return info.AdditionalProperties["hostname"]
		}
		By("Waiting for the enrolled device to report its original hostname")
		Eventually(readHostname, TIMEOUT, POLLING).Should(Equal(originalHostname))

		By("Changing the VM hostname without reloading the agent")
		updatedHostname := fmt.Sprintf("e2e-status-%d", GinkgoParallelProcess())
		if updatedHostname == originalHostname {
			updatedHostname = fmt.Sprintf("e2e-changed-%d", GinkgoParallelProcess())
		}
		Expect(harness.SetVMHostname(updatedHostname)).To(Succeed())
		Eventually(readHostname, TIMEOUT, POLLING).Should(Equal(updatedHostname))
	})
})

type customInfoSnapshot struct {
	Value              string
	Status             v1beta1.SystemInfoSourceStatusType
	Message            string
	LastTransitionTime time.Time
}

// readCustomInfoSnapshot returns a value and its per-source health from the same device response.
func readCustomInfoSnapshot(harness *e2e.Harness, deviceID string) (*customInfoSnapshot, error) {
	response, err := harness.GetDeviceWithStatusSystem(deviceID)
	if err != nil || response == nil || response.JSON200 == nil || response.JSON200.Status == nil {
		return nil, err
	}
	deviceStatus := response.JSON200.Status
	if deviceStatus.SystemInfo.CustomInfo == nil || deviceStatus.SystemInfoStatus == nil {
		return nil, nil
	}
	value, found := (*deviceStatus.SystemInfo.CustomInfo)[statusFixtureKey]
	if !found {
		return nil, nil
	}
	source, found := deviceStatus.SystemInfoStatus.Statuses.CustomInfo[statusFixtureKey]
	if !found {
		return nil, nil
	}
	snapshot := &customInfoSnapshot{Value: value, Status: source.Status, LastTransitionTime: source.LastTransitionTime}
	if source.Message != nil {
		snapshot.Message = *source.Message
	}
	return snapshot, nil
}

// cleanupStatusFixture restores the prepared selection and removes only the per-spec data.
func cleanupStatusFixture(harness *e2e.Harness) error {
	if err := harness.RemoveAgentFile(statusFixtureDropIn); err != nil {
		return fmt.Errorf("remove status fixture selection: %w", err)
	}
	if err := harness.RemoveAgentFile(statusFixtureValue); err != nil {
		return fmt.Errorf("remove status fixture input: %w", err)
	}
	if _, err := harness.RunScriptOnVM("rmdir " + statusFixtureDir); err != nil {
		return fmt.Errorf("remove status fixture directory: %w", err)
	}
	return nil
}
