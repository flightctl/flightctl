package registryconfig

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
)

// AmbiguityError contains supplier identities without credential values.
type AmbiguityError struct {
	Registry        string
	Scope           string
	Destination     string
	RepositoryNames []string
}

func (e *AmbiguityError) Error() string {
	return fmt.Sprintf("ambiguous credentials for %s scope %q: Repositories %s", e.Registry, e.Scope, strings.Join(e.RepositoryNames, ", "))
}

type decision struct {
	outcome  Outcome
	supplier *repository
	names    []string
}

func choose(peers []repository) decision {
	if len(peers) == 0 {
		return decision{outcome: OutcomeUnmatched}
	}
	result := decision{outcome: OutcomeBlocked}
	var winners []repository
	readPreferred := false
	for _, peer := range peers {
		if peer.spec.DevicePullAccess == nil || !*peer.spec.DevicePullAccess {
			continue
		}
		result.outcome = OutcomeAllowed
		if peer.spec.OciAuth == nil {
			continue
		}
		read := peer.spec.AccessMode == nil || *peer.spec.AccessMode == domain.OciRepoAccessModeRead
		if read && !readPreferred {
			winners = nil
			readPreferred = true
		}
		if read == readPreferred {
			winners = append(winners, peer)
		}
	}
	if len(winners) == 1 {
		result.outcome = OutcomeSelected
		result.supplier = &winners[0]
	}
	if len(winners) > 1 {
		result.outcome = OutcomeAmbiguous
		for _, winner := range winners {
			result.names = append(result.names, winner.name)
		}
		slices.Sort(result.names)
	}
	return result
}

// Select returns one result per input need. Any error prevents publishing the batch.
func (s *Snapshot) Select(needs []PullNeed) ([]Selection, error) {
	results := make([]Selection, len(needs))
	peersByNeed := make([][]repository, len(needs))
	for i, need := range needs {
		registry, path, err := parseNeed(need)
		if err != nil {
			return nil, fmt.Errorf("invalid pull need %d: %w", i, err)
		}
		scope, peers := s.matchingScope(registry, path, need.Consumer)
		results[i] = Selection{Need: need, Registry: registry, Scope: scope}
		peersByNeed[i] = peers
	}
	type entryKey struct {
		destination string
		registry    string
		scope       string
	}
	decisions := make(map[entryKey]decision)
	var ambiguities []*AmbiguityError
	for i := range results {
		result := &results[i]
		key := entryKey{result.Need.Destination, result.Registry, result.Scope}
		selected, cached := decisions[key]
		if !cached {
			selected = choose(peersByNeed[i])
			if selected.outcome != OutcomeUnmatched {
				decisions[key] = selected
			}
			if selected.outcome == OutcomeAmbiguous {
				ambiguities = append(ambiguities, &AmbiguityError{Registry: result.Registry, Scope: result.Scope, Destination: result.Need.Destination, RepositoryNames: selected.names})
			}
		}
		result.Outcome = selected.outcome
		if selected.supplier != nil {
			supplier, err := supplierCopy(*selected.supplier)
			if err != nil {
				return nil, err
			}
			result.Supplier = supplier
		}
	}
	slices.SortFunc(ambiguities, func(a, b *AmbiguityError) int {
		return cmp.Or(cmp.Compare(a.Registry, b.Registry), cmp.Compare(a.Scope, b.Scope), cmp.Compare(a.Destination, b.Destination))
	})
	errs := make([]error, len(ambiguities))
	for i, ambiguity := range ambiguities {
		errs[i] = ambiguity
	}
	return results, errors.Join(errs...)
}
