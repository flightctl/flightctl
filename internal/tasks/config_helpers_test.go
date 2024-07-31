package tasks

import (
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
			meta := api.ObjectMeta{Labels: &labels, Name: util.StrToPtr("devname")}
			new, err := ReplaceParameters([]byte(configItem), meta)
			Expect(err).ToNot(HaveOccurred())
			Expect(string(new)).To(Equal("ignition blah blah val blah blah val2 blah devname ok"))
		})
	})

	When("the config references a label that does not exist", func() {
		It("will return an error", func() {
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.labels[key2] }} blah"
			labels := map[string]string{"key": "val", "otherkey": "otherval"}
			meta := api.ObjectMeta{Labels: &labels, Name: util.StrToPtr("devname")}
			_, err := ReplaceParameters([]byte(configItem), meta)
			Expect(err).To(HaveOccurred())
		})
	})

	When("the config references a name that does not exist", func() {
		It("will return an error", func() {
			configItem := "ignition blah blah {{ device.metadata.labels[key]}} blah blah {{ device.metadata.name }} blah"
			labels := map[string]string{"key": "val", "otherkey": "otherval"}
			meta := api.ObjectMeta{Labels: &labels}
			_, err := ReplaceParameters([]byte(configItem), meta)
			Expect(err).To(HaveOccurred())
		})
	})
})
