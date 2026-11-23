package registryconfig_test

import (
	"strings"
	"testing"

	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestReferenceMatching(t *testing.T) {
	for _, tt := range []struct {
		name, registry, namespace, repository, ref, wantRegistry, wantScope string
		matched                                                             bool
	}{
		{"registry", "quay.io", "", "", "quay.io/team/app:tag", "quay.io", "", true},
		{"namespace", "quay.io", "team", "", "quay.io/team/app:tag", "quay.io", "team", true},
		{"exact namespace", "quay.io", "team", "", "quay.io/team", "quay.io", "team", true},
		{"repository", "quay.io", "", "team/app", "quay.io/team/app:tag", "quay.io", "team/app", true},
		{"repository descendants", "quay.io", "", "team/app", "quay.io/team/app/child", "quay.io", "team/app", true},
		{"component boundary", "quay.io", "team", "", "quay.io/team-other/app", "quay.io", "", false},
		{"explicit port", "quay.io:5000", "team", "", "quay.io:5000/team/app", "quay.io:5000", "team", true},
		{"different port", "quay.io:5000", "team", "", "quay.io:5001/team/app", "quay.io:5001", "", false},
		{"other endpoint", "quay.io", "team", "", "registry.example/team/app", "registry.example", "", false},
		{"short Docker Hub", "index.docker.io", "", "nginx", "nginx:latest", "docker.io", "library/nginx", true},
		{"Docker Hub alias", "docker.io", "library", "", "index.docker.io/library/nginx", "docker.io", "library", true},
		{"Docker Hub namespace", "index.docker.io", "team", "", "docker.io/team/app", "docker.io", "team", true},
		{"Docker Hub registry wide", "docker.io", "", "", "nginx", "docker.io", "", true},
		{"Docker Hub registry alias need", "docker.io", "team", "", "registry-1.docker.io/team/app", "docker.io", "team", true},
		{"Docker Hub registry alias candidate", "registry-1.docker.io", "team", "", "docker.io/team/app", "docker.io", "team", true},
		{"Docker Hub aliases together", "index.docker.io", "team", "", "registry-1.docker.io/team/app", "docker.io", "team", true},
		{"Docker Hub registry alias short image", "registry-1.docker.io", "", "nginx", "nginx", "docker.io", "library/nginx", true},
		{"Docker Hub registry alias library need", "docker.io", "", "nginx", "registry-1.docker.io/nginx", "docker.io", "library/nginx", true},
		{"Docker Hub registry alias registry wide", "registry-1.docker.io", "", "", "docker.io/team/app", "docker.io", "", true},
		{"Docker Hub registry alias candidate port isolation", "registry-1.docker.io:443", "", "", "docker.io/team/app", "docker.io", "", false},
		{"Docker Hub registry alias need port isolation", "docker.io", "", "", "registry-1.docker.io:443/team/app", "registry-1.docker.io:443", "", false},
		{"Docker Hub registry alias explicit port", "registry-1.docker.io:443", "team", "", "registry-1.docker.io:443/team/app", "registry-1.docker.io:443", "team", true},
		{"image transport", "quay.io", "team", "", "docker://quay.io/team/app", "quay.io", "team", true},
		{"digest", "quay.io", "team", "", "quay.io/team/app@sha256:" + strings.Repeat("a", 64), "quay.io", "team", true},
		{"tag and digest", "quay.io", "team", "", "quay.io/team/app:tag@sha256:" + strings.Repeat("b", 64), "quay.io", "team", true},
		{"localhost", "localhost:5000", "team", "", "localhost:5000/team/app", "localhost:5000", "team", true},
		{"endpoint case", "QUAY.IO", "team", "", "QUAY.IO/team/app", "quay.io", "team", true},
		{"single label registry isolation", "registry", "", "", "nginx", "docker.io", "", false},
		{"IPv6 port", "[::1]:5000", "team", "", "[::1]:5000/team/app", "[::1]:5000", "team", true},
	} {
		t.Run("When matching "+tt.name+" it should use normalized component scopes", func(t *testing.T) {
			c := credential(t, "supplier", tt.registry, tt.namespace)
			if tt.repository != "" {
				spec, err := c.Repository.Spec.AsOciRepoSpec()
				require.NoError(t, err)
				spec.Repository = lo.ToPtr(tt.repository)
				require.NoError(t, c.Repository.Spec.FromOciRepoSpec(spec))
			}
			s, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
			require.NoError(t, err)
			got, err := s.Select([]rc.PullNeed{need(tt.ref)})
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tt.wantRegistry, got[0].Registry)
			require.Equal(t, tt.wantScope, got[0].Scope)
			if tt.matched {
				require.Equal(t, rc.OutcomeSelected, got[0].Outcome)
				require.Equal(t, "supplier", *got[0].Supplier.Metadata.Name)
			} else {
				require.Equal(t, rc.OutcomeUnmatched, got[0].Outcome)
				require.Nil(t, got[0].Supplier)
			}
		})
	}
}

func TestHelmChartRegistryMatching(t *testing.T) {
	registry := credential(t, "single-label", "registry", "")
	hub := credential(t, "docker-hub", "docker.io", "")
	both := []rc.Candidate{registry, hub}
	for _, tt := range []struct {
		name, ref, wantRegistry, wantSupplier string
		consumer                              rc.Consumer
		candidates                            []rc.Candidate
	}{
		{"Helm chart with transport", "oci://registry/team/chart:1", "registry", "single-label", rc.ConsumerHelmChart, both},
		{"Helm chart without transport", "registry/team/chart:1", "registry", "single-label", rc.ConsumerHelmChart, both},
		{"Helm chart without a matching registry", "oci://registry/team/chart:1", "registry", "", rc.ConsumerHelmChart, []rc.Candidate{hub}},
		{"Helm chart registry alias", "oci://registry-1.docker.io/team/chart:1", "docker.io", "docker-hub", rc.ConsumerHelmChart, both},
		{"Helm chart registry alias candidate", "oci://docker.io/team/chart:1", "docker.io", "alias", rc.ConsumerHelmChart, []rc.Candidate{credential(t, "alias", "registry-1.docker.io", "")}},
		{"container image", "docker://registry/team/chart:1", "docker.io", "docker-hub", rc.ConsumerContainer, both},
		{"Helm workload image", "docker://registry/team/chart:1", "docker.io", "docker-hub", rc.ConsumerHelmImage, both},
		{"OS image", "docker://registry/team/chart:1", "docker.io", "docker-hub", rc.ConsumerOS, both},
	} {
		t.Run("When selecting a "+tt.name+" it should use the consumer's registry semantics", func(t *testing.T) {
			s, err := rc.NewSnapshot(orgID, tt.candidates)
			require.NoError(t, err)
			input := need(tt.ref)
			input.Consumer = tt.consumer
			got, err := s.Select([]rc.PullNeed{input})
			require.NoError(t, err)
			require.Len(t, got, 1)
			require.Equal(t, tt.wantRegistry, got[0].Registry)
			require.Empty(t, got[0].Scope)
			if tt.wantSupplier == "" {
				require.Equal(t, rc.OutcomeUnmatched, got[0].Outcome)
				require.Nil(t, got[0].Supplier)
			} else {
				require.Equal(t, rc.OutcomeSelected, got[0].Outcome)
				require.Equal(t, tt.wantSupplier, *got[0].Supplier.Metadata.Name)
			}
		})
	}
}

func TestNeedValidation(t *testing.T) {
	s, err := rc.NewSnapshot(orgID, []rc.Candidate{credential(t, "registry", "registry", ""), credential(t, "port", "registry:5000", "")})
	require.NoError(t, err)
	for _, tt := range []struct {
		name  string
		input rc.PullNeed
	}{
		{"bad reference", need("https://private-token@quay.io/team/app")},
		{"missing reference", need("")},
		{"missing destination", rc.PullNeed{Reference: "quay.io/team/app", Consumer: rc.ConsumerContainer}},
		{"unknown consumer", rc.PullNeed{Reference: "quay.io/team/app", Consumer: "bad", Destination: "root"}},
		{"chart prefix on image", need("oci://quay.io/team/app")},
		{"pathless chart", rc.PullNeed{Reference: "registry", Consumer: rc.ConsumerHelmChart, Destination: "helm"}},
		{"pathless OCI chart", rc.PullNeed{Reference: "oci://registry", Consumer: rc.ConsumerHelmChart, Destination: "helm"}},
		{"pathless chart with port", rc.PullNeed{Reference: "registry:5000", Consumer: rc.ConsumerHelmChart, Destination: "helm"}},
		{"pathless OCI chart with port", rc.PullNeed{Reference: "oci://registry:5000", Consumer: rc.ConsumerHelmChart, Destination: "helm"}},
		{"empty chart path", rc.PullNeed{Reference: "oci://registry/", Consumer: rc.ConsumerHelmChart, Destination: "helm"}},
	} {
		t.Run("When a need has "+tt.name+" it should reject the entire batch safely", func(t *testing.T) {
			got, err := s.Select([]rc.PullNeed{need("quay.io/team/app"), tt.input})
			require.Error(t, err)
			require.Nil(t, got)
			require.NotContains(t, err.Error(), "private-token")
		})
	}
}
