package registryconfig

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

type repository struct {
	name     string
	spec     domain.OciRepoSpec
	resource []byte
}

// Snapshot owns an organization-scoped copy of Repository selection inputs.
type Snapshot struct {
	registries map[string]map[string][]repository
}

func NewSnapshot(orgID uuid.UUID, candidates []Candidate) (*Snapshot, error) {
	s := &Snapshot{registries: make(map[string]map[string][]repository)}
	names := make(map[string]bool)
	for _, c := range candidates {
		if c.OrganizationID != orgID {
			continue
		}
		kind, err := c.Repository.Spec.Discriminator()
		if err != nil {
			return nil, fmt.Errorf("invalid Repository type")
		}
		if kind == string(domain.RepoSpecTypeGit) || kind == string(domain.RepoSpecTypeHttp) {
			continue
		}
		if kind != string(domain.RepoSpecTypeOci) {
			return nil, fmt.Errorf("invalid Repository type")
		}
		if c.Repository.Metadata.Name == nil || *c.Repository.Metadata.Name == "" {
			return nil, fmt.Errorf("missing Repository name")
		}
		name := *c.Repository.Metadata.Name
		if names[name] {
			return nil, fmt.Errorf("duplicate Repository %q", name)
		}
		names[name] = true
		spec, err := c.Repository.Spec.AsOciRepoSpec()
		if err != nil {
			return nil, fmt.Errorf("invalid OCI Repository %q", name)
		}
		if spec.AccessMode != nil && *spec.AccessMode != domain.OciRepoAccessModeRead && *spec.AccessMode != domain.OciRepoAccessModeReadWrite {
			return nil, fmt.Errorf("invalid access mode for Repository %q", name)
		}
		registry, scope, err := repositoryScope(spec)
		if err != nil {
			return nil, fmt.Errorf("invalid scope for Repository %q", name)
		}
		resource, err := json.Marshal(c.Repository)
		if err != nil {
			return nil, fmt.Errorf("invalid resource for Repository %q", name)
		}
		if s.registries[registry] == nil {
			s.registries[registry] = make(map[string][]repository)
		}
		s.registries[registry][scope] = append(s.registries[registry][scope], repository{name: name, spec: spec, resource: resource})
	}
	return s, nil
}

func (s *Snapshot) matchingScope(registry, path string, consumer Consumer) (string, []repository) {
	scopes := s.registries[registry]
	if consumer == ConsumerHelmChart {
		return "", scopes[""]
	}
	for {
		if peers := scopes[path]; len(peers) > 0 {
			return path, peers
		}
		if path == "" {
			return "", nil
		}
		i := strings.LastIndexByte(path, '/')
		if i < 0 {
			path = ""
		} else {
			path = path[:i]
		}
	}
}

func supplierCopy(repo repository) (*domain.Repository, error) {
	var result domain.Repository
	if err := json.Unmarshal(repo.resource, &result); err != nil {
		return nil, fmt.Errorf("invalid stored Repository %q", repo.name)
	}
	return &result, nil
}
