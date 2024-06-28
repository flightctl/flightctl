package status

import (
	"context"
	"fmt"
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

const podmanListResult = `
[
  {
    "AutoRemove": true,
    "Command": [
      "command"
    ],
    "CreatedAt": "40 minutes ago",
    "CIDFile": "",
    "Exited": false,
    "ExitedAt": -62135596800,
    "ExitCode": 0,
    "Id": "id1",
    "Image": "quay.io/image1:latest",
    "ImageID": "b22b91a96569c182755b01c5ab342d7194f825984fa293cebfd1b1c8c383252b",
    "IsInfra": false,
    "Labels": {
      "description": "A lovely description.",
      "version": "1"
    },
    "Mounts": [
      "/foo"
    ],
    "Names": [
      "myfirstname",
	  "myothername"
    ],
    "Namespaces": {

    },
    "Networks": [],
    "Pid": 1136940,
    "Pod": "",
    "PodName": "",
    "Ports": [
      {
        "host_ip": "127.0.0.1",
        "container_port": 1234,
        "host_port": 1234,
        "range": 1,
        "protocol": "tcp"
      }
    ],
    "Restarts": 0,
    "Size": null,
    "StartedAt": 1706178662,
    "State": "running",
    "Status": "Up 40 minutes",
    "Created": 1706178662
  },
  {
    "AutoRemove": true,
    "Command": [
      "mycommand"
    ],
    "CreatedAt": "46 minutes ago",
    "CIDFile": "",
    "Exited": false,
    "ExitedAt": -62135596800,
    "ExitCode": 0,
    "Id": "id2",
    "Image": "quay.io/image2:latest",
    "ImageID": "b22b91a96569c182755b01c5ab342d7194f825984fa293cebfd1b1c8c383252c",
    "IsInfra": false,
    "Labels": {
      "description": "This describes it perfectly.",
      "version": "2"
    },
    "Mounts": [
      "/bar"
    ],
    "Names": [
      "agreatname"
    ],
    "Namespaces": {

    },
    "Networks": [],
    "Pid": 1136940,
    "Pod": "",
    "PodName": "",
    "Ports": [
      {
        "host_ip": "127.0.0.1",
        "container_port": 7443,
        "host_port": 7443,
        "range": 1,
        "protocol": "tcp"
      }
    ],
    "Restarts": 0,
    "Size": null,
    "StartedAt": 1706178662,
    "State": "paused",
    "Status": "Paused",
    "Created": 1706178662
  }
]
`

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

	Context("containers controller", func() {
		It("list podman containers", func() {
			container.matchPatterns = []string{"myfirstname", "agreatname"}
			execMock.EXPECT().LookPath("crictl").Return("", fmt.Errorf("not found"))
			execMock.EXPECT().LookPath("podman").Return("/usr/bin/podman", nil)
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/podman", "ps", "-a", "--format", "json", "--filter", "name=myfirstname", "--filter", "name=agreatname").Return(podmanListResult, "", 0)
			err := container.Export(context.TODO(), &deviceStatus)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(deviceStatus.Applications.Data)).To(Equal(2))
		})

		It("list crio containers", func() {
			container.matchPatterns = []string{"alpine", "busybox"}
			execMock.EXPECT().LookPath("crictl").Return("/usr/bin/crictl", nil)
			execMock.EXPECT().LookPath("podman").Return("", fmt.Errorf("not found"))
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/crictl", "ps", "-a", "--output", "json", "--name", "alpine", "--name", "busybox").Return(crioListResult, "", 0)
			err := container.Export(context.TODO(), &deviceStatus)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(deviceStatus.Applications.Data)).To(Equal(2))
		})

		It("list both podman and crio containers", func() {
			container.matchPatterns = []string{"myfirstname", "alpine"}
			execMock.EXPECT().LookPath("podman").Return("/usr/bin/podman", nil)
			execMock.EXPECT().LookPath("crictl").Return("/usr/bin/crictl", nil)
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/podman", "ps", "-a", "--format", "json", "--filter", "name=myfirstname", "--filter", "name=alpine").Return(podmanListResult, "", 0)
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/crictl", "ps", "-a", "--output", "json", "--name", "myfirstname", "--name", "alpine").Return(crioListResult, "", 0)
			err := container.Export(context.TODO(), &deviceStatus)
			Expect(err).ToNot(HaveOccurred())

			Expect(len(deviceStatus.Applications.Data)).To(Equal(4))
		})
	})
})
