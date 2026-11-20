package registryconfig_test

import (
	"slices"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func rotations[T any](input []T) [][]T {
	var result [][]T
	for i := range input {
		rotated := append(slices.Clone(input[i:]), input[:i]...)
		result = append(result, rotated)
		reversed := slices.Clone(rotated)
		slices.Reverse(reversed)
		result = append(result, reversed)
	}
	return result
}

func TestSharedDestinations(t *testing.T) {
	candidates := []rc.Candidate{
		credential(t, "broad", "quay.io", ""),
		credential(t, "narrow", "quay.io", "team"),
		changed(t, credential(t, "writer", "quay.io", "team"), func(s *domain.OciRepoSpec) { s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeReadWrite) }),
		credential(t, "port", "quay.io:5000", "team"),
	}
	foreign := credential(t, "foreign", "quay.io", "team/app")
	foreign.OrganizationID = uuid.New()
	candidates = append(candidates, foreign)
	inputs := []rc.PullNeed{
		need("quay.io/team/app:one"),
		{Reference: "quay.io/team/other:two", Consumer: rc.ConsumerHelmImage, Destination: "container:root"},
		{Reference: "quay.io/team/app:three", Consumer: rc.ConsumerContainer, User: "workload", Destination: "container:workload"},
		need("quay.io/other/app"),
		need("quay.io:5000/team/app"),
		need("quay.io/team/app:one"),
	}
	t.Run("When consumers and candidates are reordered it should retain scoped shared suppliers", func(t *testing.T) {
		for _, resources := range rotations(candidates) {
			s, err := rc.NewSnapshot(orgID, resources)
			require.NoError(t, err)
			for _, needs := range rotations(inputs) {
				got, err := s.Select(needs)
				require.NoError(t, err)
				require.Len(t, got, len(needs))
				for i, result := range got {
					require.Equal(t, needs[i], result.Need)
					require.Equal(t, rc.OutcomeSelected, result.Outcome)
					expected := "narrow"
					if result.Registry == "quay.io:5000" {
						expected = "port"
					} else if result.Scope == "" {
						expected = "broad"
					}
					require.Equal(t, expected, *result.Supplier.Metadata.Name)
				}
			}
		}
	})
}

func TestSharedAmbiguities(t *testing.T) {
	candidates := []rc.Candidate{
		credential(t, "zulu", "quay.io", "team"), credential(t, "alpha", "quay.io", "team"),
		credential(t, "second", "registry.example", ""), credential(t, "first", "registry.example", ""),
	}
	inputs := []rc.PullNeed{
		need("quay.io/team/app:one"),
		{Reference: "quay.io/team/app:two", Consumer: rc.ConsumerHelmImage, Destination: "container:root"},
		need("registry.example/app"),
		{Reference: "quay.io/team/app", Consumer: rc.ConsumerContainer, User: "workload", Destination: "container:workload"},
		need("quay.io/team/app:one"),
	}
	t.Run("When shared ties are reordered it should report each credential entry once in canonical order", func(t *testing.T) {
		expectedText := ""
		for _, resources := range rotations(candidates) {
			s, err := rc.NewSnapshot(orgID, resources)
			require.NoError(t, err)
			for _, needs := range rotations(inputs) {
				got, err := s.Select(needs)
				require.Error(t, err)
				require.Len(t, got, len(needs))
				for i, result := range got {
					require.Equal(t, needs[i], result.Need)
					require.Equal(t, rc.OutcomeAmbiguous, result.Outcome)
					require.Nil(t, result.Supplier)
				}
				joined, ok := err.(interface{ Unwrap() []error })
				require.True(t, ok)
				require.Len(t, joined.Unwrap(), 3)
				var entries []*rc.AmbiguityError
				for _, entry := range joined.Unwrap() {
					a, ok := entry.(*rc.AmbiguityError)
					require.True(t, ok)
					entries = append(entries, a)
				}
				require.Equal(t, "container:root", entries[0].Destination)
				require.Equal(t, "container:workload", entries[1].Destination)
				require.Equal(t, []string{"alpha", "zulu"}, entries[0].RepositoryNames)
				require.Equal(t, []string{"alpha", "zulu"}, entries[1].RepositoryNames)
				require.Equal(t, "registry.example", entries[2].Registry)
				require.Equal(t, []string{"first", "second"}, entries[2].RepositoryNames)
				if expectedText == "" {
					expectedText = err.Error()
				} else {
					require.Equal(t, expectedText, err.Error())
				}
				require.NotContains(t, err.Error(), "private-token")
			}
		}
	})
}
