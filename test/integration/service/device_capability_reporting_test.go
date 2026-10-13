package service_test

import (
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// The agent reports device capabilities as structured status.systemInfo fields.
// These specs cover how the service persists and returns them, and how OS mode
// resolves from status.systemInfo.osMode with a fallback to the deprecated
// status.capabilities.osMode reported by older agents.
var _ = Describe("Device capability reporting", func() {
	var suite *ServiceTestSuite

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
	})

	AfterEach(func() {
		suite.Teardown()
	})

	// reportStatus enrolls a device and pushes one agent status report, which is
	// the only path through which capability data reaches the server.
	reportStatus := func(name string, mutate func(status *api.DeviceStatus)) {
		GinkgoHelper()
		_, status := suite.Device.CreateDevice(suite.Ctx, suite.OrgID, api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     &api.DeviceSpec{},
		})
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)

		deviceStatus := api.NewDeviceStatus()
		deviceStatus.SystemInfo.AgentVersion = "1.4.0"
		deviceStatus.SystemInfo.Architecture = "amd64"
		deviceStatus.SystemInfo.OperatingSystem = "linux"
		mutate(&deviceStatus)

		_, status = suite.Device.ReplaceDeviceStatus(suite.Ctx, suite.OrgID, name, api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &deviceStatus,
		}, false)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
	}

	getSystemInfo := func(name string) api.DeviceSystemInfo {
		GinkgoHelper()
		device, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Expect(device.Status).ToNot(BeNil())
		return device.Status.SystemInfo
	}

	listNames := func(fieldSelector string) []string {
		GinkgoHelper()
		list, status := suite.Device.ListDevices(suite.Ctx, suite.OrgID, api.ListDevicesParams{
			FieldSelector: lo.ToPtr(fieldSelector),
		}, nil)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		names := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			names = append(names, lo.FromPtr(item.Metadata.Name))
		}
		return names
	}

	Context("GPU reporting", func() {
		It("When a device reports GPUs it should persist and return the structured list", func() {
			reportStatus("gpu-reporter", func(status *api.DeviceStatus) {
				status.SystemInfo.Gpus = &[]api.DeviceGpu{
					{
						Index:       0,
						Vendor:      lo.ToPtr("NVIDIA"),
						Model:       lo.ToPtr("RTX 4090"),
						PciAddress:  lo.ToPtr("0000:01:00.0"),
						MemoryBytes: lo.ToPtr(int64(25769803776)),
					},
					{Index: 1, Vendor: lo.ToPtr("AMD"), Model: lo.ToPtr("Instinct MI300X")},
				}
			})

			gpus := getSystemInfo("gpu-reporter").Gpus
			Expect(gpus).ToNot(BeNil())
			Expect(*gpus).To(HaveLen(2))
			Expect((*gpus)[0].Index).To(BeEquivalentTo(0))
			Expect(lo.FromPtr((*gpus)[0].Vendor)).To(Equal("NVIDIA"))
			Expect(lo.FromPtr((*gpus)[0].Model)).To(Equal("RTX 4090"))
			Expect(lo.FromPtr((*gpus)[0].MemoryBytes)).To(BeEquivalentTo(int64(25769803776)))
			Expect(lo.FromPtr((*gpus)[1].Vendor)).To(Equal("AMD"))
		})

		It("When a device reports no GPU it should return an empty list rather than an absent one", func() {
			reportStatus("gpu-less-reporter", func(status *api.DeviceStatus) {
				status.SystemInfo.Gpus = &[]api.DeviceGpu{}
			})

			gpus := getSystemInfo("gpu-less-reporter").Gpus
			Expect(gpus).ToNot(BeNil(), "an empty list means the device scanned and found nothing")
			Expect(*gpus).To(BeEmpty())
		})

		It("When an older agent does not report GPUs it should leave the field absent", func() {
			reportStatus("gpu-silent-reporter", func(status *api.DeviceStatus) {})

			Expect(getSystemInfo("gpu-silent-reporter").Gpus).To(BeNil(),
				"an absent list must stay distinguishable from an empty one")
		})
	})

	Context("KVM reporting", func() {
		It("When a device reports KVM enabled it should persist and return true", func() {
			reportStatus("kvm-enabled-reporter", func(status *api.DeviceStatus) {
				status.SystemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(true)}
			})

			kvm := getSystemInfo("kvm-enabled-reporter").Kvm
			Expect(kvm).ToNot(BeNil())
			Expect(lo.FromPtr(kvm.Enabled)).To(BeTrue())
		})

		It("When a device reports KVM disabled it should persist and return false", func() {
			reportStatus("kvm-disabled-reporter", func(status *api.DeviceStatus) {
				status.SystemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(false)}
			})

			kvm := getSystemInfo("kvm-disabled-reporter").Kvm
			Expect(kvm).ToNot(BeNil())
			Expect(kvm.Enabled).ToNot(BeNil(),
				"a reported false must round-trip as a set field, not as an absent one")
			Expect(lo.FromPtr(kvm.Enabled)).To(BeFalse(),
				"a reported false must stay distinguishable from an unreported capability")
		})

		It("When an older agent does not report KVM it should leave the field absent", func() {
			reportStatus("kvm-silent-reporter", func(status *api.DeviceStatus) {})

			Expect(getSystemInfo("kvm-silent-reporter").Kvm).To(BeNil())
		})
	})

	Context("OS mode resolution", func() {
		seedOsModes := func() {
			GinkgoHelper()
			reportStatus("os-mode-systeminfo", func(status *api.DeviceStatus) {
				status.SystemInfo.OsMode = lo.ToPtr(api.OsModeImage)
			})
			reportStatus("os-mode-legacy", func(status *api.DeviceStatus) {
				status.Capabilities = &api.DeviceCapabilities{OsMode: lo.ToPtr(api.OsModePackage)}
			})
			reportStatus("os-mode-dual-write", func(status *api.DeviceStatus) {
				status.SystemInfo.OsMode = lo.ToPtr(api.OsModeImage)
				status.Capabilities = &api.DeviceCapabilities{OsMode: lo.ToPtr(api.OsModePackage)}
			})
			reportStatus("os-mode-unreported", func(status *api.DeviceStatus) {})
		}

		It("When devices report OS mode in either location it should return each device's reported value", func() {
			seedOsModes()

			Expect(lo.FromPtr(getSystemInfo("os-mode-systeminfo").OsMode)).To(Equal(api.OsModeImage))
			Expect(getSystemInfo("os-mode-legacy").OsMode).To(BeNil())

			legacy, status := suite.Device.GetDevice(suite.Ctx, suite.OrgID, "os-mode-legacy")
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(legacy.Status.Capabilities).ToNot(BeNil())
			Expect(lo.FromPtr(legacy.Status.Capabilities.OsMode)).To(Equal(api.OsModePackage))
		})

		It("When filtering by OS mode it should match each field selector independently", func() {
			seedOsModes()

			Expect(listNames("status.systemInfo.osMode=image")).To(ConsistOf("os-mode-systeminfo", "os-mode-dual-write"))
			Expect(listNames("status.capabilities.osMode=package")).To(ConsistOf("os-mode-legacy", "os-mode-dual-write"))
			Expect(listNames("status.systemInfo.osMode=package")).To(BeEmpty())
		})

		It("When summarizing devices it should prefer systemInfo and fall back to capabilities", func() {
			seedOsModes()

			list, status := suite.Device.ListDevices(suite.Ctx, suite.OrgID, api.ListDevicesParams{
				SummaryOnly: lo.ToPtr(true),
			}, nil)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(list.Summary).ToNot(BeNil())
			Expect(list.Summary.Capabilities).ToNot(BeNil())
			Expect(list.Summary.Capabilities.OsMode).ToNot(BeNil())

			counts := lo.FromPtr(list.Summary.Capabilities.OsMode)
			// os-mode-systeminfo and os-mode-dual-write resolve from systemInfo,
			// os-mode-legacy falls back to capabilities, and the device that
			// reported neither lands in the unknown bucket.
			Expect(counts).To(HaveKeyWithValue(string(api.OsModeImage), int64(2)))
			Expect(counts).To(HaveKeyWithValue(string(api.OsModePackage), int64(1)))
			Expect(counts).To(HaveKeyWithValue("unknown", int64(1)))
		})
	})
})
