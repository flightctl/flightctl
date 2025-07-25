package agent_test

import (
	"context"
	"testing"

	testutil "github.com/flightctl/flightctl/test/util"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const TIMEOUT = "5m"
const POLLING = "125ms"
const LONGTIMEOUT = "10m"

// Define a type for messages.
type Message string

const (
	UpdateRenderedVersionSuccess      Message = "Updated to desired renderedVersion: 2"
	ComposeFile                       string  = "podman-compose.yaml"
	ExpectedNumSleepAppV1Containers   string  = "3"
	ExpectedNumSleepAppV2V3Containers int     = 1
	ZeroContainers                    string  = "0"
)

// String returns the string representation of a message.
func (m Message) String() string {
	return string(m)
}

var (
	suiteCtx context.Context
)

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent E2E Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Agent E2E Suite")
})
