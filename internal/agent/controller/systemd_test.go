package controller

import (
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const systemdUnitListResult = `
[
  {
    "unit": "crio.service",
    "load": "loaded",
    "active": "active",
    "sub": "running",
    "description": "cri-o"
  },
  {
    "unit": "microshift.service",
    "load": "loaded",
    "active": "active",
    "sub": "running",
    "description": "MicroShift"
  }
]
`

var _ = Describe("containers controller", func() {
	var (
		controller *SystemDController
		device     *api.Device
		ctrl       *gomock.Controller
		execMock   *executer.MockExecuter
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		execMock = executer.NewMockExecuter(ctrl)
		controller = NewSystemDControllerWithExecuter(execMock)
		device = &api.Device{
			Spec: api.DeviceSpec{
				Systemd: &struct {
					MatchPatterns *[]string "json:\"matchPatterns,omitempty\""
				}{MatchPatterns: &[]string{"crio.service", "microshift.service"}},
			},
			Status: &api.DeviceStatus{},
		}
	})

	Context("systemd controller", func() {
		It("list systemd units", func() {
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/systemctl", "list-units", "--all", "--output", "json", "crio.service", "microshift.service").Return(systemdUnitListResult, "", 0)
			_, err := controller.SetStatus(device)
			if err != nil {
				Expect(err).ToNot(HaveOccurred())
			}

			Expect(*device.Status.SystemdUnits).ToNot(BeNil())
			Expect(len(*device.Status.SystemdUnits)).To(Equal(2))
			Expect((*device.Status.SystemdUnits)[0].Name).To(Equal("crio.service"))
			Expect((*device.Status.SystemdUnits)[0].LoadState).To(Equal("loaded"))
			Expect((*device.Status.SystemdUnits)[0].ActiveState).To(Equal("active"))
			Expect((*device.Status.SystemdUnits)[1].Name).To(Equal("microshift.service"))
			Expect((*device.Status.SystemdUnits)[1].LoadState).To(Equal("loaded"))
			Expect((*device.Status.SystemdUnits)[1].ActiveState).To(Equal("active"))
		})
	})
})
