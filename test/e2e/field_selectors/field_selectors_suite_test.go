package field_selectors

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFieldSelector(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Field selectors E2E Suite")
}
