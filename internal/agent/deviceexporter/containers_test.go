package deviceexporter

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/pkg/executer"
	"github.com/golang/mock/gomock"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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
        "container_port": 4444,
        "host_port": 4444,
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

var _ = Describe("containers exporter", func() {
	var (
		exporter *ContainerExporter
		ctrl     *gomock.Controller
		execMock *executer.MockExecuter
	)

	BeforeEach(func() {
		ctrl = gomock.NewController(GinkgoT())
		execMock = executer.NewMockExecuter(ctrl)
		exporter = newContainerExporter(execMock)
	})

	Context("containers controller", func() {
		It("list podman containers", func() {
			execMock.EXPECT().ExecuteWithContext(gomock.Any(), "/usr/bin/podman", "ps", "-a", "--format", "json").Return(podmanListResult, "", 0)
			status, err := exporter.GetStatus(context.TODO())
			if err != nil {
				Expect(err).ToNot(HaveOccurred())
			}

			containerStatus, ok := status.([]v1alpha1.ContainerStatus)
			Expect(ok).To(BeTrue())

			Expect(containerStatus).ToNot(BeNil())
			Expect(len(containerStatus)).To(Equal(2))
			Expect((containerStatus)[0].Id).To(Equal("id1"))
			Expect((containerStatus)[0].Image).To(Equal("quay.io/image1:latest"))
			Expect((containerStatus)[0].Name).To(Equal("myfirstname"))
			Expect((containerStatus)[0].Status).To(Equal("running"))
			Expect((containerStatus)[1].Id).To(Equal("id2"))
			Expect((containerStatus)[1].Image).To(Equal("quay.io/image2:latest"))
			Expect((containerStatus)[1].Name).To(Equal("agreatname"))
			Expect((containerStatus)[1].Status).To(Equal("paused"))
		})
	})
})
