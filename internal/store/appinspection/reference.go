package appinspection

import (
	"errors"
	"strconv"
	"strings"

	"github.com/containers/image/v5/docker/reference"
	"github.com/flightctl/flightctl/internal/store/model"
)

type ReferenceInput struct {
	Reference string
	Role      model.InspectionArtifactRole
	Mechanism model.InspectionPullMechanism
}

// NormalizeReference derives endpoint and path facts from an OCI reference.
// Errors identify the invalid field without exposing potentially credential-bearing input.
func NormalizeReference(input ReferenceInput) (model.ApplicationInspectionRef, error) {
	switch input.Role {
	case model.InspectionRolePackage, model.InspectionRoleWorkload, model.InspectionRoleVolume, model.InspectionRoleOS, model.InspectionRoleDelta:
	default:
		return model.ApplicationInspectionRef{}, errors.New("invalid inspection artifact role")
	}
	if !validMechanism(input.Mechanism) {
		return model.ApplicationInspectionRef{}, errors.New("invalid inspection pull mechanism")
	}
	raw := input.Reference
	if host, rest, ok := strings.Cut(raw, "/"); ok && (strings.ContainsAny(host, ".:") || strings.EqualFold(host, "localhost")) {
		raw = strings.ToLower(host) + "/" + rest
	}
	named, err := reference.ParseNormalizedNamed(raw)
	if err != nil {
		return model.ApplicationInspectionRef{}, errors.New("invalid inspection OCI reference: expected an image name with optional tag or digest")
	}
	registry, rawPort, explicitPort := strings.Cut(reference.Domain(named), ":")
	var port uint64
	if explicitPort {
		port, err = strconv.ParseUint(rawPort, 10, 16)
		if err != nil || port == 0 {
			return model.ApplicationInspectionRef{}, errors.New("invalid inspection OCI registry port: expected a number from 1 to 65535")
		}
	}
	return model.ApplicationInspectionRef{
		Reference: named.String(),
		Registry:  registry,
		Port:      uint16(port),
		Path:      strings.Split(reference.Path(named), "/"),
		Role:      input.Role,
		Mechanism: input.Mechanism,
	}, nil
}

func validMechanism(mechanism model.InspectionPullMechanism) bool {
	switch mechanism {
	case model.InspectionPullContainer, model.InspectionPullHelmChart, model.InspectionPullHelmImage, model.InspectionPullOS:
		return true
	default:
		return false
	}
}
