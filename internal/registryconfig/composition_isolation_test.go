package registryconfig_test

import (
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestCompositionBlockedNeeds(t *testing.T) {
	a, b := certificate(t, 10), certificate(t, 11)
	host := trust(t, "host", "quay.io", "", a)
	blocked := changed(t, trust(t, "blocked", "quay.io", "blocked", b), func(s *domain.OciRepoSpec) {
		s.DevicePullAccess = lo.ToPtr(false)
		s.Scheme = lo.ToPtr(domain.OciRepoSchemeHttp)
	})
	allowed := trust(t, "allowed", "quay.io", "allowed", b)
	independent := trust(t, "independent", "other.example", "", a)
	inputs := []rc.Candidate{host, blocked, allowed, independent}
	s, err := rc.NewSnapshot(orgID, inputs)
	require.NoError(t, err)
	for _, tt := range []struct {
		name   string
		needs  []rc.PullNeed
		bundle string
	}{
		{"blocked need alone", []rc.PullNeed{need("quay.io/blocked/app")}, ""},
		{"allowed need sharing the host", []rc.PullNeed{need("quay.io/blocked/app"), need("quay.io/allowed/app")}, canonicalBundle(t, a, b)},
		{"last allowed need removed", []rc.PullNeed{need("quay.io/blocked/app")}, ""},
	} {
		t.Run("When composing "+tt.name+" it should retain only trust needed by allowed pulls", func(t *testing.T) {
			needs := append(tt.needs, need("other.example/app"))
			got, err := s.Compose(needs)
			require.NoError(t, err)
			require.Equal(t, rc.OutcomeBlocked, got.Selections[0].Outcome)
			require.Equal(t, "other.example", got.Endpoints[0].Registry)
			if tt.bundle == "" {
				require.Len(t, got.Endpoints, 1)
			} else {
				require.Len(t, got.Endpoints, 2)
				require.Equal(t, tt.bundle, got.Endpoints[1].CABundle)
				require.Equal(t, []string{"allowed", "host"}, got.Endpoints[1].RepositoryNames)
			}
		})
	}
	t.Run("When delivery is reenabled it should restore contributions", func(t *testing.T) {
		inputs[1] = changed(t, blocked, func(s *domain.OciRepoSpec) { s.DevicePullAccess = lo.ToPtr(true); s.Scheme = nil })
		enabled, err := rc.NewSnapshot(orgID, inputs)
		require.NoError(t, err)
		got, err := enabled.Compose([]rc.PullNeed{need("quay.io/blocked/app")})
		require.NoError(t, err)
		require.Equal(t, rc.OutcomeAllowed, got.Selections[0].Outcome)
		require.Equal(t, canonicalBundle(t, a, b), got.Endpoints[0].CABundle)
	})
}

func TestCompositionEligibility(t *testing.T) {
	cert := certificate(t, 12)
	for _, mode := range []domain.OciRepoSpecAccessMode{domain.OciRepoAccessModeRead, domain.OciRepoAccessModeReadWrite} {
		for _, flag := range []*bool{nil, lo.ToPtr(false), lo.ToPtr(true)} {
			t.Run("When composing "+string(mode)+" eligibility "+lo.Ternary(flag == nil, "omitted", lo.Ternary(lo.FromPtr(flag), "enabled", "disabled"))+" it should require explicit delivery", func(t *testing.T) {
				input := changed(t, trust(t, "ca", "quay.io", "team", cert), func(s *domain.OciRepoSpec) { s.DevicePullAccess = flag; s.AccessMode = &mode })
				s, err := rc.NewSnapshot(orgID, []rc.Candidate{input})
				require.NoError(t, err)
				got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
				require.NoError(t, err)
				if lo.FromPtr(flag) {
					require.Len(t, got.Endpoints, 1)
					require.Equal(t, cert, got.Endpoints[0].CABundle)
				} else {
					require.Empty(t, got.Endpoints)
					require.Equal(t, rc.OutcomeBlocked, got.Selections[0].Outcome)
				}
			})
		}
	}
}

func TestCompositionExclusions(t *testing.T) {
	cert := certificate(t, 13)
	valid := trust(t, "valid", "quay.io", "team", cert)
	invalidCA := func(name, registry, scope string) rc.Candidate {
		return changed(t, trust(t, name, registry, scope, ""), func(s *domain.OciRepoSpec) {
			s.CaCrt = lo.ToPtr("secret-invalid-base64!")
			s.SkipServerVerification = lo.ToPtr(true)
		})
	}
	foreign := invalidCA("foreign", "quay.io", "team")
	foreign.OrganizationID = uuid.New()
	disabled := changed(t, invalidCA("disabled", "quay.io", "team"), func(s *domain.OciRepoSpec) { s.DevicePullAccess = lo.ToPtr(false) })
	omitted := changed(t, invalidCA("omitted", "quay.io", "team"), func(s *domain.OciRepoSpec) { s.DevicePullAccess = nil })
	var gitRepo, httpRepo domain.Repository
	require.NoError(t, gitRepo.Spec.FromGitRepoSpec(domain.GitRepoSpec{}))
	require.NoError(t, httpRepo.Spec.FromHttpRepoSpec(domain.HttpRepoSpec{}))
	for _, tt := range []struct {
		name  string
		input rc.Candidate
	}{
		{"foreign organization", foreign}, {"different host", invalidCA("host", "other.example", "team")},
		{"different port", invalidCA("port", "quay.io:5000", "team")}, {"sibling scope", invalidCA("sibling", "quay.io", "other")},
		{"path string prefix", invalidCA("prefix", "quay.io", "tea")}, {"unused deeper path", invalidCA("deeper", "quay.io", "team/app/child")},
		{"disabled peer", disabled}, {"omitted peer", omitted},
		{"Git resource", rc.Candidate{OrganizationID: orgID, Repository: gitRepo}}, {"HTTP resource", rc.Candidate{OrganizationID: orgID, Repository: httpRepo}},
	} {
		t.Run("When an excluded contributor is a "+tt.name+" it should not supply trust or transport", func(t *testing.T) {
			s, err := rc.NewSnapshot(orgID, []rc.Candidate{valid, tt.input})
			require.NoError(t, err)
			got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
			require.NoError(t, err)
			require.Equal(t, []rc.EndpointConfig{{Registry: "quay.io", CABundle: cert, Scheme: domain.OciRepoSchemeHttps, RepositoryNames: []string{"valid"}}}, got.Endpoints)
		})
	}
	t.Run("When malformed trust is needed only by blocked pulls it should not be parsed", func(t *testing.T) {
		blocked := changed(t, invalidCA("blocked", "quay.io", "team"), func(s *domain.OciRepoSpec) { s.DevicePullAccess = lo.ToPtr(false) })
		s, err := rc.NewSnapshot(orgID, []rc.Candidate{blocked, invalidCA("broad", "quay.io", "")})
		require.NoError(t, err)
		got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
		require.NoError(t, err)
		require.Empty(t, got.Endpoints)
	})
}

func TestCompositionConsumers(t *testing.T) {
	cert := certificate(t, 14)
	for _, consumer := range []rc.Consumer{rc.ConsumerContainer, rc.ConsumerOS, rc.ConsumerHelmImage, rc.ConsumerHelmChart} {
		for _, hostAuth := range []bool{false, true} {
			t.Run("When "+string(consumer)+" needs path trust with host auth "+lo.Ternary(hostAuth, "present", "absent")+" it should preserve credential scoping", func(t *testing.T) {
				inputs := []rc.Candidate{trust(t, "path", "quay.io", "team", cert)}
				if hostAuth {
					inputs = append(inputs, credential(t, "host", "quay.io", ""))
				}
				input := need("quay.io/team/app")
				input.Consumer = consumer
				if consumer == rc.ConsumerHelmChart {
					input.Reference = "oci://quay.io/team/chart:1"
				}
				s, err := rc.NewSnapshot(orgID, inputs)
				require.NoError(t, err)
				selected, err := s.Select([]rc.PullNeed{input})
				require.NoError(t, err)
				got, err := s.Compose([]rc.PullNeed{input})
				require.NoError(t, err)
				require.Equal(t, selected, got.Selections)
				require.Equal(t, cert, got.Endpoints[0].CABundle)
				if hostAuth && consumer == rc.ConsumerHelmChart {
					require.Equal(t, "host", *got.Selections[0].Supplier.Metadata.Name)
				} else {
					require.Nil(t, got.Selections[0].Supplier)
				}
			})
		}
	}
	t.Run("When Helm host scope is blocked it should suppress path trust", func(t *testing.T) {
		host := changed(t, credential(t, "host", "quay.io", ""), func(s *domain.OciRepoSpec) { s.DevicePullAccess = lo.ToPtr(false) })
		s, err := rc.NewSnapshot(orgID, []rc.Candidate{host, trust(t, "path", "quay.io", "team", cert)})
		require.NoError(t, err)
		got, err := s.Compose([]rc.PullNeed{{Reference: "oci://quay.io/team/chart:1", Consumer: rc.ConsumerHelmChart, Destination: "helm"}})
		require.NoError(t, err)
		require.Equal(t, rc.OutcomeBlocked, got.Selections[0].Outcome)
		require.Empty(t, got.Endpoints)
	})
}

func TestCompositionOrdering(t *testing.T) {
	a, b := certificate(t, 15), certificate(t, 16)
	inputs := []rc.Candidate{trust(t, "host", "quay.io", "", a), trust(t, "path", "quay.io", "team", b), trust(t, "port", "quay.io:5000", "", b), trust(t, "hub", "docker.io", "library", a)}
	needs := []rc.PullNeed{need("quay.io/team/app"), need("quay.io:5000/team/app"), need("alpine:latest"), need("quay.io/team/app"), {Reference: "quay.io/team/app", Consumer: rc.ConsumerOS, Destination: "os", User: "other"}}
	expected := []rc.EndpointConfig{
		{Registry: "docker.io", CABundle: a, Scheme: domain.OciRepoSchemeHttps, RepositoryNames: []string{"hub"}},
		{Registry: "quay.io", CABundle: canonicalBundle(t, a, b), Scheme: domain.OciRepoSchemeHttps, RepositoryNames: []string{"host", "path"}},
		{Registry: "quay.io:5000", CABundle: b, Scheme: domain.OciRepoSchemeHttps, RepositoryNames: []string{"port"}},
	}
	t.Run("When candidates and needs are reordered it should preserve endpoint composition", func(t *testing.T) {
		for _, resources := range rotations(inputs) {
			s, err := rc.NewSnapshot(orgID, resources)
			require.NoError(t, err)
			for _, pulls := range rotations(needs) {
				got, err := s.Compose(pulls)
				require.NoError(t, err)
				require.Equal(t, expected, got.Endpoints)
				for i, selection := range got.Selections {
					require.Equal(t, pulls[i], selection.Need)
				}
			}
		}
	})
	t.Run("When normalized endpoints have different spelling it should merge their trust", func(t *testing.T) {
		s, err := rc.NewSnapshot(orgID, []rc.Candidate{trust(t, "upper", "QUAY.IO", "", a), trust(t, "lower", "quay.io", "team", b)})
		require.NoError(t, err)
		got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
		require.NoError(t, err)
		require.Len(t, got.Endpoints, 1)
		require.Equal(t, canonicalBundle(t, a, b), got.Endpoints[0].CABundle)
	})
}

func TestCompositionConflictOrdering(t *testing.T) {
	one := trust(t, "one", "a.example", "left", "")
	two := changed(t, trust(t, "two", "a.example", "right", ""), func(s *domain.OciRepoSpec) { s.Scheme = lo.ToPtr(domain.OciRepoSchemeHttp) })
	three := trust(t, "three", "z.example", "", "")
	four := changed(t, trust(t, "four", "z.example", "team", ""), func(s *domain.OciRepoSpec) { s.SkipServerVerification = lo.ToPtr(true) })
	inputs := []rc.Candidate{one, two, three, four, trust(t, "compatible", "ok.example", "", "")}
	needs := []rc.PullNeed{need("a.example/left/app"), need("a.example/right/app"), need("z.example/team/app"), need("ok.example/app")}
	t.Run("When several endpoints conflict it should report deterministic errors without partial output", func(t *testing.T) {
		expected := ""
		for _, resources := range rotations(inputs) {
			s, err := rc.NewSnapshot(orgID, resources)
			require.NoError(t, err)
			for _, pulls := range rotations(needs) {
				got, err := s.Compose(pulls)
				require.Nil(t, got)
				require.Error(t, err)
				joined, ok := err.(interface{ Unwrap() []error })
				require.True(t, ok)
				require.Len(t, joined.Unwrap(), 2)
				for i, registry := range []string{"a.example", "z.example"} {
					var conflict *rc.TransportConflictError
					require.ErrorAs(t, joined.Unwrap()[i], &conflict)
					require.Equal(t, registry, conflict.Registry)
				}
				if expected == "" {
					expected = err.Error()
				} else {
					require.Equal(t, expected, err.Error())
				}
			}
		}
	})
}

func TestCompositionOwnership(t *testing.T) {
	cert := certificate(t, 17)
	c := trust(t, "original", "quay.io", "", cert)
	s, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
	require.NoError(t, err)
	*c.Repository.Metadata.Name = "changed"
	got, err := s.Compose([]rc.PullNeed{need("quay.io/app")})
	require.NoError(t, err)
	got.Endpoints[0].RepositoryNames[0] = "changed-again"
	got.Endpoints[0].CABundle = "changed"
	again, err := s.Compose([]rc.PullNeed{need("quay.io/app")})
	require.NoError(t, err)
	require.Equal(t, []string{"original"}, again.Endpoints[0].RepositoryNames)
	require.Equal(t, cert, again.Endpoints[0].CABundle)
}
