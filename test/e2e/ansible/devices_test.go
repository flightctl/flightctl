package ansible_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("flightctl.core.device_info", func() {
	const (
		modelID = "test-model-id"
		fleetID = "test-fleet-id"
		ownerID = "test-owner-id"
	)

	var testDevices = []map[string]interface{}{
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

	BeforeSuite(func() {
		for _, device := range testDevices {
			_, err := wrapper.RunModule("flightctl.core.device", device)
			Expect(err).NotTo(HaveOccurred())
		}
	})

	AfterSuite(func() {
		for _, device := range testDevices {
			_, _ = wrapper.RunModule("flightctl.core.device", map[string]interface{}{
				"id":    device["id"],
				"state": "absent",
			})
		}
	})

	It("should retrieve all devices", func() {
		result, err := wrapper.RunInfoModule("flightctl.core.device_info", nil)
		Expect(err).NotTo(HaveOccurred())

		devices := wrapper.Extract(result, "plays.0.tasks.0.result.devices")
		Expect(len(devices)).To(BeNumerically(">=", len(testDevices)))
	})

	It("should retrieve the first device by ID and check details", func() {
		d := testDevices[0]
		result, err := wrapper.RunInfoModule("flightctl.core.device_info", map[string]interface{}{
			"id": d["id"],
		})
		Expect(err).NotTo(HaveOccurred())

		device := wrapper.ExtractOne(result, "plays.0.tasks.0.result.devices")
		Expect(device["id"]).To(Equal(d["id"]))
		Expect(device["model"]).To(Equal(d["model"]))
		Expect(device["fleet"]).To(Equal(d["fleet"]))
		Expect(device["owner"]).To(Equal(d["owner"]))
	})

	It("should retrieve the first device by name and check details", func() {
		d := testDevices[0]
		result, err := wrapper.RunInfoModule("flightctl.core.device_info", map[string]interface{}{
			"name": d["name"],
		})
		Expect(err).NotTo(HaveOccurred())

		device := wrapper.ExtractOne(result, "plays.0.tasks.0.result.devices")
		Expect(device["name"]).To(Equal(d["name"]))

		if identity, ok := d["identity"].(map[string]interface{}); ok {
			Expect(wrapper.NestedValue(device, "identity.mac")).To(Equal(identity["mac"]))
		}
	})
})
