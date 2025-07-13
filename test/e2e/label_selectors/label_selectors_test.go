package label_selectors

import (
	"context"
	"fmt"
	"strings"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/test/e2e/resources"
	"github.com/flightctl/flightctl/test/harness/e2e"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var (
	suiteCtx context.Context
	harness  *e2e.Harness
)

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Label Selectors E2E Suite")

	// Create harness using VM pool function but without getting a VM
	var err error
	harness, err = e2e.NewTestHarnessWithVMPool(suiteCtx, 0)
	Expect(err).ToNot(HaveOccurred())

	// Remove the VM since this test doesn't need it
	harness.VM = nil
})

var _ = AfterSuite(func() {
	if harness != nil {
		harness.Cleanup(false)
		err := harness.CleanUpAllResources()
		Expect(err).ToNot(HaveOccurred())
	}
})

func TestLabelSelectors(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Label Selectors E2E Suite")
}

var _ = Describe("Label Selectors", Label("integration", "78751"), func() {
	var (
		harness         *e2e.Harness
		expectedDevices []*api.Device
	)

	const (
		uniqueLabelKey = "unique"
		deviceCount    = 10
	)

	BeforeEach(func() {
		expectedDevices = nil
	})

	AfterEach(func() {
		Expect(resources.DeleteAll(harness, expectedDevices, nil, nil)).To(Succeed())
	})

	Context("Filter devices by label value", func() {
		type Example struct {
			Labels string
			Key    string
			Index  int
			Count  int
		}
		DescribeTable("Filter a selected device from a list of devices using exact label value.",
			func(e Example) {
				By(fmt.Sprintf("creating devices with labels '%s', filtering by key '%s' at index %d, expecting %d", e.Labels, e.Key, e.Index, e.Count))
				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithAddedUniqueLabelToLabels(harness, deviceCount, uniqueLabelKey, e.Labels, &expectedDevices)).To(Succeed())

				Expect(resources.DevicesAreListed(harness, deviceCount)).To(Succeed())

				filteringDevicesResponse, err := filteringDevicesWithLabelNameAndIndex(harness, e.Key, e.Index, expectedDevices)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedDevices(filteringDevicesResponse, err, e.Count)).To(Succeed())
			},
			Entry("exact match 1", Example{Labels: "region=eu-west-1,zone=AB", Key: uniqueLabelKey, Index: 2, Count: 1}),
			Entry("exact match 2", Example{Labels: "region=eu-west-2,zone=CD", Key: uniqueLabelKey, Index: 5, Count: 1}),
			Entry("exact match 3", Example{Labels: "region=eu-west-3,zone=EF", Key: uniqueLabelKey, Index: 8, Count: 1}),
			Entry("mismatch", Example{Labels: "region=eu-west-4,zone=GH", Key: "unique1", Index: 6, Count: 0}),
		)
	})

	Context("Filter devices by various label selectors", func() {
		type Example struct {
			Labels         string
			UniqueLabelKey string
			Selector       string
			Count          int
		}

		DescribeTable("Filter selected devices from a list of devices using different selectors.",
			func(e Example) {
				By(fmt.Sprintf("creating devices with labels '%s', unique label key '%s', filtering by selector '%s', expecting %d", e.Labels, e.UniqueLabelKey, e.Selector, e.Count))
				Expect(resources.DevicesAreListed(harness, 0)).To(Succeed())

				Expect(createDevicesWithAddedUniqueLabelToLabels(harness, deviceCount, e.UniqueLabelKey, e.Labels, &expectedDevices)).To(Succeed())

				Expect(resources.DevicesAreListed(harness, deviceCount)).To(Succeed())

				filteringDevicesResponse, err := filteringDevicesWithLabelSelector(harness, e.Selector)
				Expect(err).ShouldNot(HaveOccurred())

				Expect(responseShouldContainExpectedDevices(filteringDevicesResponse, err, e.Count)).To(Succeed())
			},
			// label in a set
			Entry("label in set - match", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "region in (test, eu-west-1)", Count: deviceCount}),
			Entry("label in set - no match", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "region in (test, eu-west-2)", Count: 0}),

			// label not in a set
			Entry("label not in set - no match", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "region notin (test, eu-west-1)", Count: 0}),
			Entry("label not in set - match", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "region notin (test, eu-west-2)", Count: deviceCount}),

			// label existence
			Entry("label existence - match region", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "region", Count: deviceCount}),
			Entry("label existence - match unique", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "unique", Count: deviceCount}),
			Entry("label existence - no match", Example{Labels: "key=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "region", Count: 0}),

			// label non-existence
			Entry("label non-existence - no match region", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "!region", Count: 0}),
			Entry("label non-existence - no match unique", Example{Labels: "region=eu-west-1", UniqueLabelKey: uniqueLabelKey, Selector: "!unique", Count: 0}),
		)
	})
})

func createDevicesWithAddedUniqueLabelToLabels(harness *e2e.Harness, count int, labelKey string, csvLabels string, expectedDevices *[]*api.Device) error {
	if count <= 0 {
		return fmt.Errorf("count should be greater than 0")
	}
	if labelKey == "" {
		return fmt.Errorf("labelKey cannot be empty")
	}
	if csvLabels == "" {
		return fmt.Errorf("labels CSV input cannot be empty")
	}

	labels := parseTextWithLabels(csvLabels)

	for i := 0; i < count; i++ {
		deviceName := uuid.NewString()
		labels[labelKey] = uuid.NewString()
		device, err := resources.CreateDevice(harness, deviceName, &labels)
		if err == nil {
			*expectedDevices = append(*expectedDevices, device)
		}
	}
	return nil
}

func parseTextWithLabels(text string) map[string]string {
	labelMap := make(map[string]string)
	labels := strings.Split(text, ",")
	for _, label := range labels {
		parts := strings.SplitN(label, "=", 2)
		if len(parts) == 2 {
			labelMap[parts[0]] = parts[1]
		}
	}
	return labelMap
}

func filteringDevicesWithLabelNameAndIndex(harness *e2e.Harness, labelName string, createdDeviceIndex int, expectedDevices []*api.Device) (string, error) {
	if createdDeviceIndex < 0 {
		return "", fmt.Errorf("created device index must be positive")
	}
	if createdDeviceIndex >= len(expectedDevices) {
		return "", fmt.Errorf("created device index must be less than the number of devices")
	}
	if labelName == "" {
		return "", fmt.Errorf("label name cannot be empty")
	}
	selectedDevice := expectedDevices[createdDeviceIndex]
	selector := fmt.Sprintf("%s=%s", labelName, (*selectedDevice.Metadata.Labels)[labelName])

	return resources.FilterWithLabelSelector(harness, resources.Devices, selector)
}

func filteringDevicesWithLabelSelector(harness *e2e.Harness, selector string) (string, error) {
	if selector == "" {
		return "", fmt.Errorf("selector cannot be empty")
	}
	return resources.FilterWithLabelSelector(harness, resources.Devices, selector)
}

func responseShouldContainExpectedDevices(response string, err error, count int) error {
	return resources.SomeRowsAreListedInResponse(response, err, count)
}
