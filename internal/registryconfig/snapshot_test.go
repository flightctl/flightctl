package registryconfig_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

var orgID = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func candidate(t *testing.T, name string, spec domain.OciRepoSpec) rc.Candidate {
	t.Helper()
	spec.Type = domain.OciRepoSpecTypeOci
	repo := domain.Repository{}
	repo.Metadata.Name = lo.ToPtr(name)
	require.NoError(t, repo.Spec.FromOciRepoSpec(spec))
	return rc.Candidate{OrganizationID: orgID, Repository: repo}
}

func credential(t *testing.T, name, registry, scope string) rc.Candidate {
	t.Helper()
	auth := domain.OciAuth{}
	require.NoError(t, auth.FromDockerAuth(domain.DockerAuth{Username: "account", Password: "private-token", AuthType: domain.Docker}))
	spec := domain.OciRepoSpec{Registry: registry, DevicePullAccess: lo.ToPtr(true), OciAuth: &auth}
	if scope != "" {
		spec.Namespace = lo.ToPtr(scope)
	}
	return candidate(t, name, spec)
}

func need(ref string) rc.PullNeed {
	return rc.PullNeed{Reference: ref, Consumer: rc.ConsumerContainer, Destination: "container:root"}
}

func TestSnapshot(t *testing.T) {
	local := credential(t, "local", "quay.io", "")
	foreign := credential(t, "local", "quay.io", "team")
	foreign.OrganizationID = uuid.New()
	invalid := rc.Candidate{OrganizationID: foreign.OrganizationID}
	var httpRepo domain.Repository
	require.NoError(t, httpRepo.Spec.FromHttpRepoSpec(domain.HttpRepoSpec{}))
	var gitRepo domain.Repository
	require.NoError(t, gitRepo.Spec.FromGitRepoSpec(domain.GitRepoSpec{}))
	tests := []struct {
		name       string
		candidates []rc.Candidate
		outcome    rc.Outcome
	}{
		{"When the snapshot is empty it should leave needs unmatched", nil, rc.OutcomeUnmatched},
		{"When only foreign resources exist it should leave needs unmatched", []rc.Candidate{foreign, invalid}, rc.OutcomeUnmatched},
		{"When foreign scopes and identities overlap it should select only local resources", []rc.Candidate{foreign, invalid, local}, rc.OutcomeSelected},
		{"When non OCI resources exist it should ignore them", []rc.Candidate{{OrganizationID: orgID, Repository: httpRepo}, {OrganizationID: orgID, Repository: gitRepo}, local}, rc.OutcomeSelected},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, err := rc.NewSnapshot(orgID, tt.candidates)
			require.NoError(t, err)
			got, err := s.Select([]rc.PullNeed{need("quay.io/team/app")})
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tt.outcome, got[0].Outcome)
			if tt.outcome == rc.OutcomeSelected {
				require.Equal(t, "local", *got[0].Supplier.Metadata.Name)
			} else {
				require.Nil(t, got[0].Supplier)
			}
			empty, err := s.Select(nil)
			require.NoError(t, err)
			require.Empty(t, empty)
		})
	}
}

func TestSnapshotInvalidInput(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*rc.Candidate)
	}{
		{"missing name", func(c *rc.Candidate) { c.Repository.Metadata.Name = nil }},
		{"invalid registry", func(c *rc.Candidate) { *c = credential(t, "invalid", "private-token/invalid", "") }},
		{"invalid scope", func(c *rc.Candidate) { *c = credential(t, "invalid", "quay.io", "UPPER") }},
		{"both path fields", func(c *rc.Candidate) {
			*c = candidate(t, "invalid", domain.OciRepoSpec{Registry: "quay.io", Namespace: lo.ToPtr("team"), Repository: lo.ToPtr("team/app")})
		}},
		{"invalid mode", func(c *rc.Candidate) {
			*c = candidate(t, "invalid", domain.OciRepoSpec{Registry: "quay.io", AccessMode: lo.ToPtr(domain.OciRepoSpecAccessMode("bad"))})
		}},
		{"malformed union", func(c *rc.Candidate) {
			require.NoError(t, json.Unmarshal([]byte(`{"private-token":true}`), &c.Repository.Spec))
		}},
	} {
		t.Run("When input has "+tt.name+" it should return a sanitized error", func(t *testing.T) {
			c := credential(t, "invalid", "quay.io", "")
			tt.change(&c)
			s, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
			require.Error(t, err)
			require.Nil(t, s)
			require.NotContains(t, err.Error(), "private-token")
		})
	}
	t.Run("When a resource identity repeats it should reject the snapshot", func(t *testing.T) {
		c := credential(t, "duplicate", "quay.io", "")
		_, err := rc.NewSnapshot(orgID, []rc.Candidate{c, c})
		require.Error(t, err)
	})
}

func TestSnapshotOwnership(t *testing.T) {
	c := credential(t, "original", "quay.io", "team")
	c.Repository.Metadata.Labels = lo.ToPtr(map[string]string{"key": "original"})
	s, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
	require.NoError(t, err)
	*c.Repository.Metadata.Name = "changed"
	(*c.Repository.Metadata.Labels)["key"] = "changed"
	got, err := s.Select([]rc.PullNeed{need("quay.io/team/app"), need("quay.io/team/app")})
	require.NoError(t, err)
	require.Equal(t, "original", *got[0].Supplier.Metadata.Name)
	*got[0].Supplier.Metadata.Name = "changed-again"
	(*got[0].Supplier.Metadata.Labels)["key"] = "changed-again"
	require.Equal(t, "original", *got[1].Supplier.Metadata.Name)
	again, err := s.Select([]rc.PullNeed{need("quay.io/team/app")})
	require.NoError(t, err)
	require.Equal(t, "original", *again[0].Supplier.Metadata.Name)
	require.Equal(t, "original", (*again[0].Supplier.Metadata.Labels)["key"])
}
