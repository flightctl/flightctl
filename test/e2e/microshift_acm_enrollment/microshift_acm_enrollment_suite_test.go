package microshift

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMicroshift(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "microshift ACM enrollment E2E Suite")
}
