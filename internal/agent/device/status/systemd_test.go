package status

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
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
		systemD      *SystemD
		ctrl         *gomock.Controller
		execMock     *executer.MockExecuter
		deviceStatus v1alpha1.DeviceStatus
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		deviceStatus = v1alpha1.DeviceStatus{}
		execMock = executer.NewMockExecuter(ctrl)
		systemD = newSystemD(execMock)
		systemD.matchPatterns = []string{"crio.service", "microshift.service"}
	})

	Context("systemd controller", func() {
		It("list systemd units", func() {
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), systemdCommand, "list-units", "--all", "--output", "json", "crio.service", "microshift.service").Return(systemdUnitListResult, "", 0)
			err := systemD.Export(context.TODO(), &deviceStatus)
			Expect(err).ToNot(HaveOccurred())

			Expect(*deviceStatus.SystemdUnits).ToNot(BeNil())
			Expect(len(*deviceStatus.SystemdUnits)).To(Equal(2))
			Expect((*deviceStatus.SystemdUnits)[0].Name).To(Equal("crio.service"))
			Expect((*deviceStatus.SystemdUnits)[0].LoadState).To(Equal("loaded"))
			Expect((*deviceStatus.SystemdUnits)[0].ActiveState).To(Equal("active"))
			Expect((*deviceStatus.SystemdUnits)[1].Name).To(Equal("microshift.service"))
			Expect((*deviceStatus.SystemdUnits)[1].LoadState).To(Equal("loaded"))
			Expect((*deviceStatus.SystemdUnits)[1].ActiveState).To(Equal("active"))
		})
	})
})
