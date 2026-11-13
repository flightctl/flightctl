package registryconfig_test

import (
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func changed(t *testing.T, c rc.Candidate, fn func(*domain.OciRepoSpec)) rc.Candidate {
	t.Helper()
	spec, err := c.Repository.Spec.AsOciRepoSpec()
	require.NoError(t, err)
	fn(&spec)
	require.NoError(t, c.Repository.Spec.FromOciRepoSpec(spec))
	return c
}

func TestSelection(t *testing.T) {
	broad := credential(t, "broad", "quay.io", "")
	narrow := credential(t, "narrow", "quay.io", "team")
	rw := changed(t, narrow, func(s *domain.OciRepoSpec) { s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeReadWrite) })
	disabled := changed(t, narrow, func(s *domain.OciRepoSpec) { s.DevicePullAccess = lo.ToPtr(false) })
	omitted := changed(t, narrow, func(s *domain.OciRepoSpec) { s.DevicePullAccess = nil })
	anonymous := changed(t, narrow, func(s *domain.OciRepoSpec) { s.OciAuth = nil; s.CaCrt = lo.ToPtr("certificate") })
	peer := credential(t, "peer", "quay.io", "team")
	third := credential(t, "third", "quay.io", "team")
	rwPeer := changed(t, peer, func(s *domain.OciRepoSpec) { s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeReadWrite) })
	explicitRead := changed(t, peer, func(s *domain.OciRepoSpec) { s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeRead) })
	different := changed(t, peer, func(s *domain.OciRepoSpec) {
		auth := domain.OciAuth{}
		require.NoError(t, auth.FromDockerAuth(domain.DockerAuth{AuthType: domain.Docker, Username: "other-account", Password: "other-token"}))
		s.OciAuth = &auth
	})
	repoPeer := changed(t, peer, func(s *domain.OciRepoSpec) { s.Namespace = nil; s.Repository = lo.ToPtr("team") })
	tests := []struct {
		name            string
		candidates      []rc.Candidate
		consumer        rc.Consumer
		outcome         rc.Outcome
		supplier, scope string
		ties            []string
	}{
		{name: "narrow scope beats broad Read", candidates: []rc.Candidate{broad, rw}, outcome: rc.OutcomeSelected, supplier: "narrow", scope: "team"},
		{name: "enabled peers ignore disabled peers", candidates: []rc.Candidate{disabled, peer}, outcome: rc.OutcomeSelected, supplier: "peer", scope: "team"},
		{name: "enabled peers ignore omitted flags", candidates: []rc.Candidate{omitted, peer}, outcome: rc.OutcomeSelected, supplier: "peer", scope: "team"},
		{name: "disabled scope stops fallback", candidates: []rc.Candidate{broad, disabled}, outcome: rc.OutcomeBlocked, scope: "team"},
		{name: "omitted flag stops fallback", candidates: []rc.Candidate{broad, omitted}, outcome: rc.OutcomeBlocked, scope: "team"},
		{name: "Read beats ReadWrite", candidates: []rc.Candidate{rw, peer}, outcome: rc.OutcomeSelected, supplier: "peer", scope: "team"},
		{name: "Read ignores lower preference ties", candidates: []rc.Candidate{narrow, rwPeer, changed(t, third, func(s *domain.OciRepoSpec) { s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeReadWrite) })}, outcome: rc.OutcomeSelected, supplier: "narrow", scope: "team"},
		{name: "identical credential ties", candidates: []rc.Candidate{narrow, peer, third}, outcome: rc.OutcomeAmbiguous, scope: "team", ties: []string{"narrow", "peer", "third"}},
		{name: "different credential ties", candidates: []rc.Candidate{narrow, different}, outcome: rc.OutcomeAmbiguous, scope: "team", ties: []string{"narrow", "peer"}},
		{name: "ReadWrite ties", candidates: []rc.Candidate{rw, rwPeer}, outcome: rc.OutcomeAmbiguous, scope: "team", ties: []string{"narrow", "peer"}},
		{name: "omitted mode equals Read", candidates: []rc.Candidate{narrow, explicitRead}, outcome: rc.OutcomeAmbiguous, scope: "team", ties: []string{"narrow", "peer"}},
		{name: "equal namespace and repository scopes tie", candidates: []rc.Candidate{narrow, repoPeer}, outcome: rc.OutcomeAmbiguous, scope: "team", ties: []string{"narrow", "peer"}},
		{name: "anonymous scope stays allowed without broader credentials", candidates: []rc.Candidate{broad, anonymous}, outcome: rc.OutcomeAllowed, scope: "team"},
		{name: "anonymous Read does not defeat authenticated ReadWrite", candidates: []rc.Candidate{anonymous, rwPeer}, outcome: rc.OutcomeSelected, supplier: "peer", scope: "team"},
		{name: "anonymous peers do not tie", candidates: []rc.Candidate{anonymous, changed(t, peer, func(s *domain.OciRepoSpec) { s.OciAuth = nil })}, outcome: rc.OutcomeAllowed, scope: "team"},
		{name: "Helm ignores path-only scopes", candidates: []rc.Candidate{narrow}, consumer: rc.ConsumerHelmChart, outcome: rc.OutcomeUnmatched},
		{name: "Helm ignores disabled path scopes", candidates: []rc.Candidate{broad, disabled}, consumer: rc.ConsumerHelmChart, outcome: rc.OutcomeSelected, supplier: "broad"},
		{name: "Helm registry-wide ties", candidates: []rc.Candidate{broad, credential(t, "peer", "quay.io", "")}, consumer: rc.ConsumerHelmChart, outcome: rc.OutcomeAmbiguous, ties: []string{"broad", "peer"}},
		{name: "Helm registry-wide Read wins", candidates: []rc.Candidate{broad, changed(t, credential(t, "peer", "quay.io", ""), func(s *domain.OciRepoSpec) { s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeReadWrite) })}, consumer: rc.ConsumerHelmChart, outcome: rc.OutcomeSelected, supplier: "broad"},
		{name: "Helm registry-wide blocking", candidates: []rc.Candidate{changed(t, broad, func(s *domain.OciRepoSpec) { s.DevicePullAccess = nil }), narrow}, consumer: rc.ConsumerHelmChart, outcome: rc.OutcomeBlocked},
		{name: "Helm images stay path-aware", candidates: []rc.Candidate{broad, narrow}, consumer: rc.ConsumerHelmImage, outcome: rc.OutcomeSelected, supplier: "narrow", scope: "team"},
		{name: "OS stays path-aware", candidates: []rc.Candidate{broad, narrow}, consumer: rc.ConsumerOS, outcome: rc.OutcomeSelected, supplier: "narrow", scope: "team"},
	}
	for _, tt := range tests {
		t.Run("When selecting "+tt.name+" it should preserve the selection contract", func(t *testing.T) {
			s, err := rc.NewSnapshot(orgID, tt.candidates)
			require.NoError(t, err)
			input := need("quay.io/team/app")
			if tt.consumer != "" {
				input.Consumer = tt.consumer
			}
			if input.Consumer == rc.ConsumerHelmChart {
				input.Reference = "oci://quay.io/team/chart:1"
			}
			got, err := s.Select([]rc.PullNeed{input})
			require.Len(t, got, 1)
			if tt.ties != nil {
				require.Error(t, err)
				var ambiguity *rc.AmbiguityError
				require.True(t, errors.As(err, &ambiguity))
				require.Equal(t, tt.ties, ambiguity.RepositoryNames)
				require.Equal(t, "quay.io", ambiguity.Registry)
				require.Equal(t, tt.scope, ambiguity.Scope)
				require.NotContains(t, err.Error(), "private-token")
				require.NotContains(t, err.Error(), "other-token")
				require.NotContains(t, err.Error(), "other-account")
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tt.outcome, got[0].Outcome)
			require.Equal(t, tt.scope, got[0].Scope)
			require.Equal(t, input, got[0].Need)
			if tt.supplier != "" {
				require.NotNil(t, got[0].Supplier)
				require.Equal(t, tt.supplier, *got[0].Supplier.Metadata.Name)
			} else {
				require.Nil(t, got[0].Supplier)
			}
		})
	}
}

func TestDeliveryFlags(t *testing.T) {
	for _, mode := range []domain.OciRepoSpecAccessMode{domain.OciRepoAccessModeRead, domain.OciRepoAccessModeReadWrite} {
		for _, flag := range []*bool{nil, lo.ToPtr(false), lo.ToPtr(true)} {
			t.Run("When delivery eligibility varies it should require explicit true for "+string(mode)+" "+lo.Ternary(flag == nil, "omitted", lo.Ternary(lo.FromPtr(flag), "enabled", "disabled")), func(t *testing.T) {
				c := changed(t, credential(t, "supplier", "quay.io", ""), func(s *domain.OciRepoSpec) { s.DevicePullAccess = flag; s.AccessMode = &mode })
				s, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
				require.NoError(t, err)
				got, err := s.Select([]rc.PullNeed{need("quay.io/app")})
				require.NoError(t, err)
				if lo.FromPtr(flag) {
					require.Equal(t, rc.OutcomeSelected, got[0].Outcome)
				} else {
					require.Equal(t, rc.OutcomeBlocked, got[0].Outcome)
					require.Nil(t, got[0].Supplier)
				}
			})
		}
	}
}
