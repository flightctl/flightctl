package ansible_test

import (
	"github.com/flightctl/flightctl/test/e2e/ansible/ansiblewrapper"
	"github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("flightctl.core.device_info", func() {
	const (
		modelID = "test-model-id"
		fleetID = "test-fleet-id"
		ownerID = "test-owner-id"
	)

	var testDevices []map[string]interface{}

	BeforeEach(func() {
		testDevices = []map[string]interface{}{
			{
				"id":    "test-device-id",
				"name":  "test-device-name",
				"model": modelID,
				"fleet": fleetID,
				"owner": ownerID,
				"identity": map[string]interface{}{
					"mac": "aa:bb:cc:dd:ee:ff",
				},
			},
			{
				"id":    "test-device-2",
				"name":  "test-device-2-name",
				"model": modelID,
				"fleet": fleetID,
				"owner": ownerID,
			},
		}
		for _, device := range testDevices {
			_, err := ansiblewrapper.RunModule(util.Device, device)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterEach(func() {
		for _, device := range testDevices {
			_, _ = ansiblewrapper.RunModule(util.Device, map[string]interface{}{
				"id":    device["id"],
				"state": "absent",
			})
		}
	})

	It("should retrieve all devices", func() {
		result, err := ansiblewrapper.RunInfoModule(util.AnsibleDeviceInfoModule, nil)
		Expect(err).NotTo(HaveOccurred())

		devices, err := ansiblewrapper.Extract(result, "plays.0.tasks.0.result.devices")
		Expect(len(devices)).To(BeNumerically(">=", len(testDevices)))
		Expect(err).NotTo(HaveOccurred())
	})

	It("should retrieve the first device by ID and check details", func() {
		d := testDevices[0]
		result, err := ansiblewrapper.RunInfoModule(util.AnsibleDeviceInfoModule, map[string]interface{}{
			"id": d["id"],
		})
		Expect(err).NotTo(HaveOccurred())

		device, err := ansiblewrapper.ExtractOne(result, "plays.0.tasks.0.result.devices")
		Expect(err).NotTo(HaveOccurred())
		expectedFields := []string{"id", "model", "fleet", "owner"}
		for _, field := range expectedFields {
			Expect(device[field]).To(Equal(d[field]))
		}
	})

	It("should retrieve the first device by name and check details", func() {
		d := testDevices[0]
		result, err := ansiblewrapper.RunInfoModule(util.AnsibleDeviceInfoModule, map[string]interface{}{
			"name": d["name"],
		})
		Expect(err).NotTo(HaveOccurred())

		device, err := ansiblewrapper.ExtractOne(result, "plays.0.tasks.0.result.devices")
		Expect(err).NotTo(HaveOccurred())
		Expect(device["name"]).To(Equal(d["name"]))

		if identity, ok := d["identity"].(map[string]interface{}); ok {
			Expect(ansiblewrapper.NestedValue(device, "identity.mac")).To(Equal(identity["mac"]))
		}
	})
})
