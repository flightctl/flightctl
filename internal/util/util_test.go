package util

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestUtil(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Util Suite")
}

var _ = Describe("Test util", func() {
	Context("Test util", func() {
		It("LabelsMatchLabelSelector", func() {
			Expect(LabelsMatchLabelSelector(map[string]string{}, map[string]string{})).To(BeFalse())
			Expect(LabelsMatchLabelSelector(map[string]string{}, map[string]string{"key": "val"})).To(BeFalse())
			Expect(LabelsMatchLabelSelector(map[string]string{"key": "val"}, map[string]string{})).To(BeFalse())
			Expect(LabelsMatchLabelSelector(map[string]string{"key1": "val1"}, map[string]string{"key2": "val2"})).To(BeFalse())
			Expect(LabelsMatchLabelSelector(map[string]string{"key1": "val1"}, map[string]string{"key1": "val1"})).To(BeTrue())
			Expect(LabelsMatchLabelSelector(map[string]string{"key1": "val1", "key2": "val2"}, map[string]string{"key1": "val1"})).To(BeTrue())
			Expect(LabelsMatchLabelSelector(map[string]string{"key1": "val1"}, map[string]string{"key1": "val1", "key2": "val2"})).To(BeFalse())
		})
	})
})

func TestLabelMapToArray(t *testing.T) {
	LabelMapToArray()
}
