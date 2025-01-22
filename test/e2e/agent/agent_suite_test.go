package agent_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const TIMEOUT = "5m"
const POLLING = "250ms"
const LONGTIMEOUT = "10m"

// Define a type for messages.
type Message string

const (
	UpdateRenderedVersionSuccess Message = "Updated to desired renderedVersion: 2"
)

// String returns the string representation of a message.
func (m Message) String() string {
	return string(m)
}

func TestAgent(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Agent E2E Suite")
}
