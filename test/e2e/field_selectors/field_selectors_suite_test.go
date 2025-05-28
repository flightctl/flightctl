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

const (
	templateImage    = "quay.io/redhat/rhde:9.2"
	repositoryUrl    = "https://github.com/flightctl/flightctl.git"
	devicePrefix     = "device-"
	fleetPrefix      = "fleet-"
	repositoryPrefix = "repository-"
	fleetName        = "fleet-1"
	resourceCount    = 10
)
