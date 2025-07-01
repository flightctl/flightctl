package dependency

import (
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/client"
)

type OCIType string

const (
	OCITypeImage    OCIType = "Image"
	OCITypeArtifact OCIType = "Artifact"
)

type OCIPullTarget struct {
	Type       OCIType
	Reference  string
	PullPolicy v1alpha1.ImagePullPolicy
	PullSecret *client.PullSecret
}
