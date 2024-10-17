package status

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"go.uber.org/mock/gomock"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Controller Suite")
}

const crioListResult = `
{
  "containers": [
    {
      "id": "id1",
      "podSandboxId": "podId1",
      "metadata": {
        "name": "alpine",
        "attempt": 0
      },
      "image": {
        "image": "alpine",
        "annotations": {
        }
      },
      "imageRef": "docker.io/library/alpine@sha256:c4a262d530f57d1b7b68b52ba8383c2e55fd1a0cb5b4f46b11eed7a2c4e143da",
      "state": "CONTAINER_RUNNING",
      "createdAt": "1712850355546013151",
      "labels": {
      },
      "annotations": {
      }
    },
    {
      "id": "id2",
      "podSandboxId": "podId2",
      "metadata": {
        "name": "busybox",
        "attempt": 0
      },
      "image": {
        "image": "busybox",
        "annotations": {
        }
      },
      "imageRef": "docker.io/library/busybox@sha256:4be429a5fbb2e71ae7958bfa558bc637cf3a61baf40a708cb8fff532b39e52d0",
      "state": "CONTAINER_EXITED",
      "createdAt": "1712832732952358805",
      "labels": {
      },
      "annotations": {
      }
    }
  ]
}
`

var _ = Describe("containers exporter", func() {
	var (
		container    *Container
		ctrl         *gomock.Controller
		execMock     *executer.MockExecuter
		deviceStatus v1alpha1.DeviceStatus
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		deviceStatus = v1alpha1.NewDeviceStatus()
		execMock = executer.NewMockExecuter(ctrl)
		container = newContainer(execMock)
	})

	Context("containers exporter", func() {
		It("list crio containers", func() {
			container.matchPatterns = []string{"alpine", "busybox"}
			execMock.EXPECT().LookPath("crictl").Return("/usr/bin/crictl", nil)
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/crictl", "ps", "-a", "--output", "json", "--name", "alpine", "--name", "busybox").Return(crioListResult, "", 0)
			err := container.Export(context.TODO(), &deviceStatus)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(deviceStatus.Applications)).To(Equal(2))
		})
	})
})
