package tasks

import (
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestUtil(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "ConfigHelpers Suite")
}

var _ = Describe("config helpers", func() {
	When("config has appropriate parameters", func() {
		It("will replace them correctly", func() {
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.labels[key2] }} blah {{ device.metadata.name }} ok"
			labels := map[string]string{"key": "val", "key2": "val2", "otherkey": "otherval"}
			meta := api.DeviceMetadata{Labels: &labels, Name: util.StrToPtr("devname")}
			new, warnings := ReplaceParameters([]byte(configItem), meta)
			Expect(warnings).To(HaveLen(0))
			Expect(string(new)).To(Equal("ignition blah blah val blah blah val2 blah devname ok"))
		})
	})

	When("the config references a label that does not exist", func() {
		It("will return an error", func() {
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.labels[key2] }} blah"
			labels := map[string]string{"key": "val", "otherkey": "otherval"}
			meta := api.DeviceMetadata{Labels: &labels, Name: util.StrToPtr("devname")}
			_, warnings := ReplaceParameters([]byte(configItem), meta)
			Expect(warnings).To(HaveLen(1))
		})
	})

	When("the config references a name that does not exist", func() {
		It("will return an error", func() {
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.name }} blah"
			labels := map[string]string{"key": "val", "otherkey": "otherval"}
			meta := api.DeviceMetadata{Labels: &labels}
			_, warnings := ReplaceParameters([]byte(configItem), meta)
			Expect(warnings).To(HaveLen(1))
		})
	})

	When("the config has an invalid parameter", func() {
		It("will return a validation error", func() {
			configItems := []string{
				"ignition blah blah {{ {{ device.metadata.name }} {{ }} blah",
				"ignition blah blah {{ {{ device.metadata.name }} }} blah",
				"ignition blah blah {{ device.metadata.owner }} blah",
				"ignition blah blah {{ device.metadata.name x }} blah",
				"ignition blah blah {{ x device.metadata.name }} blah",
				"ignition blah blah {{ device.metadata.namex }} blah",
				"ignition blah blah {{ xdevice.metadata.name }} blah",
				"ignition blah blah {{ device.metadata.labels[x] x }} blah",
				"ignition blah blah {{ x device.metadata.labels[x] }} blah",
				"ignition blah blah {{ device.metadata.labels[x]x }} blah",
				"ignition blah blah {{ xdevice.metadata.labels[x] }} blah",
				"ignition blah blah {{ hello }} blah",
			}
			for _, configItem := range configItems {
				err := ValidateParameterFormat([]byte(configItem))
				Expect(err).To(HaveOccurred())
			}
		})
	})

	When("the config has no invalid parameter", func() {
		It("will not return a validation error", func() {
			configItems := []string{
				"ignition blah blah {{ device.metadata.name  }} blah",
				"ignition blah blah {{  device.metadata.labels[x] }} blah",
			}
			for _, configItem := range configItems {
				fmt.Printf("testing: %s\n", configItem)
				err := ValidateParameterFormat([]byte(configItem))
				Expect(err).ToNot(HaveOccurred())
			}
		})
	})
})
