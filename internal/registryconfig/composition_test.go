package registryconfig_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	rc "github.com/flightctl/flightctl/internal/registryconfig"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func certificate(t *testing.T, serial int64) string {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(serial), NotBefore: time.Unix(0, 0), NotAfter: time.Unix(2000000000, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func trust(t *testing.T, name, registry, scope, bundle string) rc.Candidate {
	t.Helper()
	spec := domain.OciRepoSpec{Registry: registry, DevicePullAccess: lo.ToPtr(true)}
	if scope != "" {
		spec.Namespace = lo.ToPtr(scope)
	}
	if bundle != "" {
		spec.CaCrt = lo.ToPtr(base64.StdEncoding.EncodeToString([]byte(bundle)))
	}
	return candidate(t, name, spec)
}

func canonicalBundle(t *testing.T, certs ...string) string {
	t.Helper()
	slices.SortFunc(certs, func(a, b string) int {
		left, _ := pem.Decode([]byte(a))
		right, _ := pem.Decode([]byte(b))
		return strings.Compare(string(left.Bytes), string(right.Bytes))
	})
	return strings.Join(slices.Compact(certs), "")
}

func TestCompositionTrust(t *testing.T) {
	a, b, c, d := certificate(t, 1), certificate(t, 2), certificate(t, 5), certificate(t, 6)
	host := trust(t, "host", "quay.io", "", a+a+b)
	supplier := changed(t, credential(t, "supplier", "quay.io", "team"), func(s *domain.OciRepoSpec) { s.CaCrt = lo.ToPtr(base64.StdEncoding.EncodeToString([]byte(b + c))) })
	peer := changed(t, credential(t, "writer", "quay.io", "team"), func(s *domain.OciRepoSpec) {
		s.AccessMode = lo.ToPtr(domain.OciRepoAccessModeReadWrite)
		s.CaCrt = lo.ToPtr(base64.StdEncoding.EncodeToString([]byte(d)))
	})
	for _, tt := range []struct {
		name          string
		candidates    []rc.Candidate
		names         []string
		authenticated bool
		bundle        string
	}{
		{"anonymous CA", []rc.Candidate{host}, []string{"host"}, false, canonicalBundle(t, a, b)},
		{"independent host trust and scoped credentials", []rc.Candidate{supplier, host, peer}, []string{"host", "supplier", "writer"}, true, canonicalBundle(t, a, b, c, d)},
	} {
		t.Run("When composing "+tt.name+" it should union endpoint certificates independently of authentication", func(t *testing.T) {
			s, err := rc.NewSnapshot(orgID, tt.candidates)
			require.NoError(t, err)
			got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
			require.NoError(t, err)
			require.Equal(t, []rc.EndpointConfig{{Registry: "quay.io", CABundle: tt.bundle, Scheme: domain.OciRepoSchemeHttps, RepositoryNames: tt.names}}, got.Endpoints)
			if tt.authenticated {
				require.Equal(t, "supplier", *got.Selections[0].Supplier.Metadata.Name)
			} else {
				require.Nil(t, got.Selections[0].Supplier)
			}
		})
	}
}

func TestCompositionTransport(t *testing.T) {
	ca := certificate(t, 3)
	for _, kind := range []string{"credential", "CA", "anonymous"} {
		for _, tt := range []struct {
			name             string
			scheme           *domain.OciRepoSpecScheme
			skip             *bool
			explicitDefaults bool
			conflict         bool
		}{
			{name: "omitted defaults"},
			{name: "explicit defaults", scheme: lo.ToPtr(domain.OciRepoSchemeHttps), skip: lo.ToPtr(false)},
			{name: "omitted scheme versus HTTP", scheme: lo.ToPtr(domain.OciRepoSchemeHttp), conflict: true},
			{name: "omitted verification versus skipped", skip: lo.ToPtr(true), conflict: true},
			{name: "explicit defaults versus both nondefaults", scheme: lo.ToPtr(domain.OciRepoSchemeHttp), skip: lo.ToPtr(true), explicitDefaults: true, conflict: true},
		} {
			t.Run("When "+kind+" contributors use "+tt.name+" it should compare effective policies", func(t *testing.T) {
				first := trust(t, "first", "quay.io", "", "")
				if kind == "credential" {
					first = credential(t, "first", "quay.io", "team")
				}
				if kind == "CA" {
					first = trust(t, "first", "quay.io", "", ca)
				}
				if tt.explicitDefaults {
					first = changed(t, first, func(s *domain.OciRepoSpec) {
						s.Scheme = lo.ToPtr(domain.OciRepoSchemeHttps)
						s.SkipServerVerification = lo.ToPtr(false)
					})
				}
				second := changed(t, trust(t, "second", "quay.io", "", ""), func(s *domain.OciRepoSpec) { s.Scheme = tt.scheme; s.SkipServerVerification = tt.skip })
				s, err := rc.NewSnapshot(orgID, []rc.Candidate{first, second})
				require.NoError(t, err)
				got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
				if tt.conflict {
					require.Nil(t, got)
					var conflict *rc.TransportConflictError
					require.ErrorAs(t, err, &conflict)
					require.Equal(t, "quay.io", conflict.Registry)
					require.Equal(t, []string{"first", "second"}, conflict.RepositoryNames)
					require.Contains(t, err.Error(), "quay.io")
					require.Contains(t, err.Error(), "first")
					require.Contains(t, err.Error(), "second")
					for _, secret := range []string{"private-token", "account", ca, base64.StdEncoding.EncodeToString([]byte(ca))} {
						require.NotContains(t, err.Error(), secret)
					}
				} else {
					require.NoError(t, err)
					require.Equal(t, domain.OciRepoSchemeHttps, got.Endpoints[0].Scheme)
					require.False(t, got.Endpoints[0].SkipServerVerification)
				}
			})
		}
	}
	for _, scheme := range []domain.OciRepoSpecScheme{domain.OciRepoSchemeHttp, domain.OciRepoSchemeHttps} {
		for _, skip := range []bool{false, true} {
			t.Run("When contributors agree on "+string(scheme)+" verification "+lo.Ternary(skip, "skipped", "enabled")+" it should preserve explicit settings", func(t *testing.T) {
				inputs := []rc.Candidate{trust(t, "one", "quay.io", "", ""), trust(t, "two", "quay.io", "team", "")}
				for i := range inputs {
					inputs[i] = changed(t, inputs[i], func(s *domain.OciRepoSpec) { s.Scheme = &scheme; s.SkipServerVerification = &skip })
				}
				s, err := rc.NewSnapshot(orgID, inputs)
				require.NoError(t, err)
				got, err := s.Compose([]rc.PullNeed{need("quay.io/team/app")})
				require.NoError(t, err)
				require.Equal(t, scheme, got.Endpoints[0].Scheme)
				require.Equal(t, skip, got.Endpoints[0].SkipServerVerification)
			})
		}
	}
}

func TestCompositionCAParsing(t *testing.T) {
	cert := certificate(t, 4)
	badBlock := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("secret-invalid-der")}))
	otherBlock := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("secret-key")}))
	withHeaders, _ := pem.Decode([]byte(cert))
	withHeaders.Headers = map[string]string{"secret": "header"}
	headerBlock := string(pem.EncodeToMemory(withHeaders))
	for _, tt := range []struct {
		name, raw string
		valid     bool
	}{
		{"invalid base64", "secret-invalid-base64!", false},
		{"empty CA", "", false},
		{"plain text", base64.StdEncoding.EncodeToString([]byte("secret-not-pem")), false},
		{"invalid certificate", base64.StdEncoding.EncodeToString([]byte(badBlock)), false},
		{"unrelated PEM", base64.StdEncoding.EncodeToString([]byte(otherBlock)), false},
		{"certificate with PEM headers", base64.StdEncoding.EncodeToString([]byte(headerBlock)), false},
		{"mixed PEM", base64.StdEncoding.EncodeToString([]byte(otherBlock + badBlock + headerBlock + cert)), true},
		{"PEM whitespace", base64.StdEncoding.EncodeToString([]byte(strings.ReplaceAll(cert, "\n", "\r\n") + cert)), true},
	} {
		t.Run("When CA input contains "+tt.name+" it should retain only usable certificates", func(t *testing.T) {
			c := changed(t, trust(t, "ca", "quay.io", "", ""), func(s *domain.OciRepoSpec) { s.CaCrt = &tt.raw })
			s, err := rc.NewSnapshot(orgID, []rc.Candidate{c})
			require.NoError(t, err)
			got, err := s.Compose([]rc.PullNeed{need("quay.io/app")})
			if tt.valid {
				require.NoError(t, err)
				require.Equal(t, cert, got.Endpoints[0].CABundle)
			} else {
				require.Error(t, err)
				require.Nil(t, got)
				require.Contains(t, err.Error(), "quay.io")
				require.Contains(t, err.Error(), "ca")
				require.NotContains(t, err.Error(), "secret-")
				if tt.raw != "" {
					require.NotContains(t, err.Error(), tt.raw)
				}
			}
		})
	}
}

func TestCompositionSelectionErrors(t *testing.T) {
	t.Run("When no needs or contributors apply it should return empty settings", func(t *testing.T) {
		s, err := rc.NewSnapshot(orgID, nil)
		require.NoError(t, err)
		for _, needs := range [][]rc.PullNeed{nil, {need("quay.io/app")}} {
			got, err := s.Compose(needs)
			require.NoError(t, err)
			require.Empty(t, got.Endpoints)
			require.Len(t, got.Selections, len(needs))
		}
	})
	t.Run("When selection fails it should return no composition", func(t *testing.T) {
		s, err := rc.NewSnapshot(orgID, []rc.Candidate{credential(t, "a", "quay.io", ""), credential(t, "b", "quay.io", "")})
		require.NoError(t, err)
		got, err := s.Compose([]rc.PullNeed{need("quay.io/app")})
		require.Nil(t, got)
		var ambiguity *rc.AmbiguityError
		require.True(t, errors.As(err, &ambiguity))
		got, err = s.Compose([]rc.PullNeed{need("private-token/INVALID")})
		require.Error(t, err)
		require.Nil(t, got)
		require.NotContains(t, err.Error(), "private-token")
	})
}
