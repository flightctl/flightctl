package deviceexporter

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
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
		exporter *SystemDExporter
		ctrl     *gomock.Controller
		execMock *executer.MockExecuter
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		execMock = executer.NewMockExecuter(ctrl)
		exporter = newSystemDExporter(execMock)
		exporter.matchPatterns = []string{"crio.service", "microshift.service"}
	})

	Context("systemd controller", func() {
		It("list systemd units", func() {
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), systemdCommand, "list-units", "--all", "--output", "json", "crio.service", "microshift.service").Return(systemdUnitListResult, "", 0)
			status, err := exporter.GetStatus(context.TODO())
			if err != nil {
				Expect(err).ToNot(HaveOccurred())
			}

			systemdUnits, ok := status.([]v1alpha1.DeviceSystemdUnitStatus)
			Expect(ok).To(BeTrue())

			Expect(systemdUnits).ToNot(BeNil())
			Expect(len(systemdUnits)).To(Equal(2))
			Expect((systemdUnits)[0].Name).To(Equal("crio.service"))
			Expect((systemdUnits)[0].LoadState).To(Equal("loaded"))
			Expect((systemdUnits)[0].ActiveState).To(Equal("active"))
			Expect((systemdUnits)[1].Name).To(Equal("microshift.service"))
			Expect((systemdUnits)[1].LoadState).To(Equal("loaded"))
			Expect((systemdUnits)[1].ActiveState).To(Equal("active"))
		})
	})
})
