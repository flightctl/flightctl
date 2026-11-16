package firmware

import (
	"encoding/json"
	"errors"

	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
)

type signaturePolicy struct {
	Default    []policyRequirement                       `json:"default"`
	Transports map[string]map[string][]policyRequirement `json:"transports"`
}

type policyRequirement struct {
	Type           string          `json:"type"`
	KeyData        string          `json:"keyData,omitempty"`
	SignedIdentity *policyIdentity `json:"signedIdentity,omitempty"`
}

type policyIdentity struct {
	Type             string `json:"type"`
	DockerRepository string `json:"dockerRepository"`
}

// SignaturePolicy renders a containers signature policy that rejects every
// image except those signed by d's publisher key for d's exact repository.
func SignaturePolicy(d *policy.ReleaseDescriptor) ([]byte, error) {
	if d == nil {
		return nil, errors.New("no approved release descriptor")
	}
	publisher := d.Publisher.Policy
	repo := publisher.SignedIdentity.DockerRepository
	if publisher.Type != "sigstoreSigned" || publisher.SignedIdentity.Type != "exactRepository" || publisher.KeyData == "" || repo == "" {
		return nil, errors.New("approved publisher policy is not a keyed sigstoreSigned requirement for an exact repository")
	}
	return json.Marshal(signaturePolicy{
		Default: []policyRequirement{{Type: "reject"}},
		Transports: map[string]map[string][]policyRequirement{
			"docker": {
				repo: {{
					Type:           "sigstoreSigned",
					KeyData:        publisher.KeyData,
					SignedIdentity: &policyIdentity{Type: "exactRepository", DockerRepository: repo},
				}},
			},
		},
	})
}
