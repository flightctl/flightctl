package ansible_test

import (
	"github.com/flightctl/flightctl/test/e2e/ansible/ansiblewrapper"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("flightctl.core.device_info", Ordered, func() {

	var testDevices []map[string]interface{}

	BeforeAll(func() {
		testDevices = []map[string]interface{}{
			{
				"kind":        util.Device,
				"name":        "test-ansible-device",
				"api_version": "flightctl.io/v1alpha1",
			},
			{
				"kind":                util.Device,
				"name":                "test-ansible-device-2",
				"resource_definition": "{{ lookup('file', 'device.yml') | from_yaml }}",
			},
		}
		for _, device := range testDevices {
			out, err := ansiblewrapper.RunModule(util.AnsibleResourceModule, device)
			logrus.Info(out)

			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterAll(func() {
		for _, device := range testDevices {
			_, _ = ansiblewrapper.RunModule(util.AnsibleResourceModule, map[string]interface{}{
				"id":    device["id"],
				"state": "absent",
			})
		}
	})

	It("should retrieve all devices", func() {
		result, err := ansiblewrapper.RunInfoModule(util.AnsibleResourceInfoModule, map[string]interface{}{
			"kind": util.Device,
		})
		Expect(err).NotTo(HaveOccurred())
		logrus.Info(result)

		devices, err := ansiblewrapper.Extract(result, "plays.0.tasks.0.result.devices")
		Expect(len(devices)).To(BeNumerically(">=", len(testDevices)))
		Expect(err).NotTo(HaveOccurred())
	})

	// It("should retrieve the first device by ID and check details", func() {
	// 	d := testDevices[0]
	// 	result, err := ansiblewrapper.RunInfoModule(util.AnsibleResourceInfoModule, map[string]interface{}{
	// 		"id": d["id"],
	// 	})
	// 	Expect(err).NotTo(HaveOccurred())

	// 	device, err := ansiblewrapper.ExtractOne(result, "plays.0.tasks.0.result.devices")
	// 	Expect(err).NotTo(HaveOccurred())
	// 	expectedFields := []string{"id", "model", "fleet", "owner"}
	// 	for _, field := range expectedFields {
	// 		Expect(device[field]).To(Equal(d[field]))
	// 	}
	// })

	// It("should retrieve the first device by name and check details", func() {
	// 	d := testDevices[0]
	// 	result, err := ansiblewrapper.RunInfoModule(util.AnsibleResourceInfoModule, map[string]interface{}{
	// 		"name": d["name"],
	// 	})
	// 	Expect(err).NotTo(HaveOccurred())

	// 	device, err := ansiblewrapper.ExtractOne(result, "plays.0.tasks.0.result.devices")
	// 	Expect(err).NotTo(HaveOccurred())
	// 	Expect(device["name"]).To(Equal(d["name"]))

	// 	if identity, ok := d["identity"].(map[string]interface{}); ok {
	// 		Expect(ansiblewrapper.NestedValue(device, "identity.mac")).To(Equal(identity["mac"]))
	// 	}
	// })
})
