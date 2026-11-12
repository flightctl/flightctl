package registryconfig

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
)

// TransportConflictError identifies Repositories with incompatible endpoint policies.
type TransportConflictError struct {
	Registry        string
	RepositoryNames []string
}

func (e *TransportConflictError) Error() string {
	return fmt.Sprintf("conflicting transport settings for %s: Repositories %s", e.Registry, strings.Join(e.RepositoryNames, ", "))
}

// Compose selects credentials and composes endpoint settings from the snapshot.
// Any error returns no composition.
func (s *Snapshot) Compose(needs []PullNeed) (*Composition, error) {
	selections, err := s.Select(needs)
	if err != nil {
		return nil, err
	}
	contributors := make(map[string]map[string]repository)
	for _, selection := range selections {
		if selection.Outcome == OutcomeBlocked {
			continue
		}
		_, path, err := parseNeed(selection.Need)
		if err != nil {
			return nil, err
		}
		for scope, peers := range s.registries[selection.Registry] {
			if scope != "" && path != scope && !strings.HasPrefix(path, scope+"/") {
				continue
			}
			for _, peer := range peers {
				if peer.spec.DevicePullAccess == nil || !*peer.spec.DevicePullAccess {
					continue
				}
				if contributors[selection.Registry] == nil {
					contributors[selection.Registry] = make(map[string]repository)
				}
				contributors[selection.Registry][peer.name] = peer
			}
		}
	}
	registries := make([]string, 0, len(contributors))
	for registry := range contributors {
		registries = append(registries, registry)
	}
	slices.Sort(registries)
	result := &Composition{Selections: selections}
	var errs []error
	for _, registry := range registries {
		endpoint, err := composeEndpoint(registry, contributors[registry])
		if err != nil {
			errs = append(errs, err)
			continue
		}
		result.Endpoints = append(result.Endpoints, endpoint)
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return result, nil
}

func composeEndpoint(registry string, contributors map[string]repository) (EndpointConfig, error) {
	endpoint := EndpointConfig{Registry: registry}
	for name := range contributors {
		endpoint.RepositoryNames = append(endpoint.RepositoryNames, name)
	}
	slices.Sort(endpoint.RepositoryNames)
	certificates := make(map[string]struct{})
	var errs []error
	conflict := false
	for i, name := range endpoint.RepositoryNames {
		spec := contributors[name].spec
		scheme, skip := domain.OciRepoSchemeHttps, false
		if spec.Scheme != nil {
			scheme = *spec.Scheme
		}
		if spec.SkipServerVerification != nil {
			skip = *spec.SkipServerVerification
		}
		if i == 0 {
			endpoint.Scheme, endpoint.SkipServerVerification = scheme, skip
		} else if scheme != endpoint.Scheme || skip != endpoint.SkipServerVerification {
			conflict = true
		}
		if spec.CaCrt != nil {
			certs, err := decodeCertificates(*spec.CaCrt)
			if err != nil {
				errs = append(errs, fmt.Errorf("invalid CA certificates for %s: Repository %q", registry, name))
				continue
			}
			for _, cert := range certs {
				certificates[cert] = struct{}{}
			}
		}
	}
	if conflict {
		errs = append(errs, &TransportConflictError{Registry: registry, RepositoryNames: endpoint.RepositoryNames})
	}
	if err := errors.Join(errs...); err != nil {
		return EndpointConfig{}, err
	}
	ordered := make([]string, 0, len(certificates))
	for cert := range certificates {
		ordered = append(ordered, cert)
	}
	slices.Sort(ordered)
	var bundle strings.Builder
	for _, cert := range ordered {
		bundle.Write(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte(cert)}))
	}
	endpoint.CABundle = bundle.String()
	return endpoint, nil
}

func decodeCertificates(encoded string) ([]string, error) {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	var certificates []string
	for len(data) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			break
		}
		data = rest
		if block.Type != "CERTIFICATE" || len(block.Headers) != 0 {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			continue
		}
		certificates = append(certificates, string(block.Bytes))
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("no usable certificates")
	}
	return certificates, nil
}
