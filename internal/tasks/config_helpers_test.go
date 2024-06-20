package tasks

import (
	"testing"

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
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.labels[key2] }} blah"
			labels := map[string]string{"key": "val", "key2": "val2", "otherkey": "otherval"}
			new, err := ReplaceParameters([]byte(configItem), &labels)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(new)).To(Equal("ignition blah blah val blah blah val2 blah"))
		})
	})

	When("the config references a label that does not exist", func() {
		It("will return an error", func() {
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.labels[key2] }} blah"
			labels := map[string]string{"key": "val", "otherkey": "otherval"}
			_, err := ReplaceParameters([]byte(configItem), &labels)
			Expect(err).To(HaveOccurred())
		})
	})
})
