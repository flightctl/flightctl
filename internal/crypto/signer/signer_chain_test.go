package signer

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"testing"

	"github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/util"
	fccrypto "github.com/flightctl/flightctl/pkg/crypto"
	"github.com/google/uuid"
)

const mockSignerName = "mock"

// mockCA implements CA to intercept IssueRequestedClientCertificate calls.
type mockCA struct {
	cfg              *ca.Config
	signers          *CASigners
	clientIssueCalls int
}

func newMockCA(t *testing.T) *mockCA {
	t.Helper()
	cfg := ca.NewDefault(t.TempDir())
	m := &mockCA{cfg: cfg}
	m.signers = NewCASigners(m)
	return m
}

func (m *mockCA) Config() *ca.Config           { return m.cfg }
func (m *mockCA) GetSigner(name string) Signer { return m.signers.GetSigner(name) }
func (m *mockCA) PeerCertificateSignerFromCtx(ctx context.Context) Signer {
	peer, err := PeerCertificateFromCtx(ctx)
	if err != nil {
		return nil
	}
	if name, err := GetSignerNameExtension(peer); err == nil && name != "" {
		return m.GetSigner(name)
	}
	return nil
}

func (m *mockCA) IssueRequestedClientCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error) {
	m.clientIssueCalls++
	cert := &x509.Certificate{Subject: csr.Subject}
	for _, o := range opts {
		_ = o(cert)
	}
	return cert, nil
}

func (m *mockCA) IssueRequestedServerCertificate(ctx context.Context, csr *x509.CertificateRequest, expirySeconds int, opts ...certOption) (*x509.Certificate, error) {
	return &x509.Certificate{Subject: csr.Subject}, nil
}

// mockSigner is a minimal signer used to exercise wrapper chains only.
// It returns nil on Verify and calls through to CA IssueRequestedClientCertificate on Sign.
type mockSigner struct {
	name             string
	ca               CA
	restrictedPrefix string
}

func (m *mockSigner) Name() string                                          { return m.name }
func (m *mockSigner) Verify(ctx context.Context, request SignRequest) error { return nil }
func (m *mockSigner) Sign(ctx context.Context, request SignRequest) (*x509.Certificate, error) {
	x := request.X509()
	return m.ca.IssueRequestedClientCertificate(ctx, &x, 0)
}

// Implement RestrictedSigner when a prefix is configured.
func (m *mockSigner) RestrictedPrefix() string { return m.restrictedPrefix }

func newMockSignerFactory(name, restrictedPrefix string) func(CA) Signer {
	return func(ca CA) Signer {
		return &mockSigner{name: name, ca: ca, restrictedPrefix: restrictedPrefix}
	}
}

// Helper to register a signer into the mock CA by name.
func (m *mockCA) registerSigner(name string, s Signer) { m.signers.signers[name] = s }
func (m *mockCA) registerMockSigner(s Signer)          { m.registerSigner(mockSignerName, s) }

func makeCSR(t *testing.T, cn string, orgID uuid.UUID) *x509.CertificateRequest {
	t.Helper()
	_, priv, err := fccrypto.NewKeyPair()
	if err != nil {
		t.Fatalf("newKeyPair: %v", err)
	}
	tpl := &x509.CertificateRequest{}
	if cn != "" {
		tpl.Subject = pkix.Name{CommonName: cn}
	}
	if orgID != uuid.Nil {
		encodedOrg, err := asn1.Marshal(orgID.String())
		if err != nil {
			t.Fatalf("marshal org id: %v", err)
		}
		tpl.ExtraExtensions = append(tpl.ExtraExtensions, pkix.Extension{Id: OIDOrgID, Critical: false, Value: encodedOrg})
	}
	raw, err := x509.CreateCertificateRequest(rand.Reader, tpl, priv.(crypto.Signer))
	if err != nil {
		t.Fatalf("create csr: %v", err)
	}
	csr, err := x509.ParseCertificateRequest(raw)
	if err != nil {
		t.Fatalf("parse csr: %v", err)
	}
	return csr
}

func withOrgCtx(orgID uuid.UUID) context.Context {
	return util.WithOrganizationID(context.Background(), orgID)
}

func TestSignerChains(t *testing.T) {
	ca := newMockCA(t)
	cfg := ca.Config()

	type testCase struct {
		name       string
		build      func() (context.Context, SignRequest)
		verifyOnly bool
		wantErr    bool
		assert     func(cert *x509.Certificate)
	}

	orgID := uuid.New()

	cases := []testCase{
		{
			name: "bootstrap_success_injects_orgid_signername_and_adjusts_cn",
			build: func() (context.Context, SignRequest) {
				subject := "foo"
				csr := makeCSR(t, subject, orgID)
				req, err := NewSignRequest(
					cfg.DeviceEnrollmentSignerName,
					*csr,
					WithResourceName(subject),
				)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return withOrgCtx(orgID), req
			},
			assert: func(cert *x509.Certificate) {
				if got, want := cert.Subject.CommonName, BootstrapCNFromName(cfg, "foo"); got != want {
					t.Fatalf("CN not adjusted: got %q want %q", got, want)
				}
				if _, present, err := GetOrgIDExtensionFromCert(cert); !present || err != nil {
					t.Fatalf("Failed to ensure OrgID ext. exists: present=%v err=%v", present, err)
				}
				if _, err := GetSignerNameExtension(cert); err != nil {
					t.Fatalf("SignerName extension missing: %v", err)
				}
			},
		},
		{
			name: "device_enrollment_injects_orgid_signername_and_fingerprint",
			build: func() (context.Context, SignRequest) {
				fingerprint := "abcdef0123456789"
				csr := makeCSR(t, fingerprint, orgID)
				req, err := NewSignRequest(cfg.DeviceManagementSignerName, *csr, WithResourceName(fingerprint))
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return withOrgCtx(orgID), req
			},
			assert: func(cert *x509.Certificate) {
				if _, present, err := GetOrgIDExtensionFromCert(cert); !present || err != nil {
					t.Fatalf("Failed to ensure OrgID ext. exists: present=%v err=%v", present, err)
				}
				if _, err := GetSignerNameExtension(cert); err != nil {
					t.Fatalf("SignerName extension missing: %v", err)
				}
				if _, err := GetDeviceFingerprintExtension(cert); err != nil {
					t.Fatalf("Device fingerprint extension missing: %v", err)
				}
			},
		},
		{
			name: "device_enrollment_peer_cert_gating_allows_bootstrap",
			build: func() (context.Context, SignRequest) {
				fingerprint := "abcdef0123456789"
				csr := makeCSR(t, fingerprint, orgID)
				req, err := NewSignRequest(cfg.DeviceManagementSignerName, *csr, WithResourceName(fingerprint))
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				// Simulate peer cert signed by bootstrap signer
				peer := &x509.Certificate{}
				// Inject signer name extension for bootstrap on the mock peer cert via ExtraExtensions
				peer.ExtraExtensions = append(peer.ExtraExtensions, pkix.Extension{Id: OIDSignerName, Value: mustASN1(t, cfg.DeviceEnrollmentSignerName)})
				ctx := context.WithValue(withOrgCtx(orgID), consts.TLSPeerCertificateCtxKey, peer)
				return ctx, req
			},
			verifyOnly: false,
		},
		{
			name: "device_enrollment_peer_cert_gating_rejects_other_signers",
			build: func() (context.Context, SignRequest) {
				fingerprint := "abcdef0123456789"
				csr := makeCSR(t, fingerprint, orgID)
				req, err := NewSignRequest(cfg.DeviceManagementSignerName, *csr, WithResourceName(fingerprint))
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				peer := &x509.Certificate{}
				peer.ExtraExtensions = append(peer.ExtraExtensions, pkix.Extension{Id: OIDSignerName, Value: mustASN1(t, cfg.DeviceSvcClientSignerName)})
				ctx := context.WithValue(withOrgCtx(orgID), consts.TLSPeerCertificateCtxKey, peer)
				return ctx, req
			},
			verifyOnly: true,
			wantErr:    true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req := tc.build()
			if tc.verifyOnly {
				err := Verify(ctx, ca, req)
				if tc.wantErr && err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !tc.wantErr && err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			cert, err := SignVerified(ctx, ca, req)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if err == nil && tc.assert != nil {
				tc.assert(cert)
			}
		})
	}
}

func TestSignerWrappers(t *testing.T) {
	ca := newMockCA(t)
	factory := newMockSignerFactory(mockSignerName, "")

	type testCase struct {
		name          string
		chainedSigner func(func(CA) Signer, CA) Signer
		build         func() (context.Context, SignRequest)
		wantErrVerify bool
		assert        func(cert *x509.Certificate)
	}

	orgID := uuid.New()

	cases := []testCase{
		{
			name: "with_certificate_reuse_short_circuits",
			chainedSigner: func(baseFactory func(CA) Signer, ca CA) Signer {
				return WithCertificateReuse(baseFactory(ca))
			},
			build: func() (context.Context, SignRequest) {
				ca.clientIssueCalls = 0
				// Valid CSR (contents irrelevant due to reuse)
				subject := "foo"
				csr := makeCSR(t, subject, orgID)
				preIssued := &x509.Certificate{Subject: pkix.Name{CommonName: "preissued"}}
				req, err := NewSignRequest(
					mockSignerName,
					*csr,
					WithResourceName(subject),
					WithIssuedCertificate(preIssued),
				)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return withOrgCtx(orgID), req
			},
			assert: func(cert *x509.Certificate) {
				if cert.Subject.CommonName != "preissued" {
					t.Fatalf("expected preissued cert to be returned, got %q", cert.Subject.CommonName)
				}
				if ca.clientIssueCalls != 0 {
					t.Fatalf("expected no CA IssueRequestedClientCertificate calls, got %d", ca.clientIssueCalls)
				}
			},
		},
		{
			name: "with_csr_validation_fails_on_tamper",
			chainedSigner: func(baseFactory func(CA) Signer, ca CA) Signer {
				return WithCSRValidation(baseFactory(ca))
			},
			build: func() (context.Context, SignRequest) {
				csr := makeCSR(t, "foo", orgID)
				tampered := *csr
				tampered.Signature = []byte("bogus")
				req, err := NewSignRequest(mockSignerName, tampered, WithResourceName("foo"))
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return withOrgCtx(orgID), req
			},
			wantErrVerify: true,
		},
		{
			name: "restricted_prefix_enforced",
			chainedSigner: func(baseFactory func(CA) Signer, ca CA) Signer {
				restricted := &mockSigner{name: "restricted", ca: ca, restrictedPrefix: ca.Config().DeviceCommonNamePrefix}
				restrictedMap := map[string]Signer{restricted.RestrictedPrefix(): restricted}
				return WithSignerRestrictedPrefixes(restrictedMap, baseFactory(ca))
			},
			build: func() (context.Context, SignRequest) {
				cn := ca.Config().DeviceCommonNamePrefix + "abcdef0123456789"
				csr := makeCSR(t, cn, orgID)
				req, err := NewSignRequest(mockSignerName, *csr)
				if err != nil {
					t.Fatalf("NewSignRequest: %v", err)
				}
				return withOrgCtx(orgID), req
			},
			wantErrVerify: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := tc.chainedSigner(factory, ca)
			ca.registerMockSigner(wrapped)
			ctx, req := tc.build()
			err := Verify(ctx, ca, req)
			if tc.wantErrVerify {
				if err == nil {
					t.Fatalf("expected error from Verify(), got nil")
				} else {
					return // expected error, no need to sign
				}

			}
			if !tc.wantErrVerify && err != nil {
				t.Fatalf("unexpected error from Verify(): %v", err)
			}

			cert, err := Sign(ctx, ca, req)
			if err != nil {
				t.Fatalf("unexpected error from Sign(): %v", err)
			}
			if err == nil && tc.assert != nil {
				tc.assert(cert)
			}
		})
	}
}

func mustASN1(t *testing.T, v string) []byte {
	t.Helper()
	b, err := asn1.Marshal(v)
	if err != nil {
		t.Fatalf("asn1: %v", err)
	}
	return b
}

func TestWithOrgIDExtension(t *testing.T) {
	ca := newMockCA(t)
	factory := newMockSignerFactory(mockSignerName, "")

	type orgOpt struct {
		has bool
		id  uuid.UUID
	}

	ctxFrom := func(o orgOpt) context.Context {
		if !o.has {
			return context.Background()
		}
		return util.WithOrganizationID(context.Background(), o.id)
	}

	csrFrom := func(o orgOpt) *x509.CertificateRequest {
		if !o.has {
			return makeCSR(t, "foo", uuid.Nil)
		}
		return makeCSR(t, "foo", o.id)
	}

	type testCase struct {
		name            string
		ctxOrg          orgOpt
		csrOrg          orgOpt
		wantVerifyError bool
		wantSignError   bool
		wantSignedOrg   orgOpt
	}

	nonDefault := uuid.New()

	cases := []testCase{
		{
			name:            "csr_absent_ctx_absent_valid_no_ext",
			ctxOrg:          orgOpt{has: false},
			csrOrg:          orgOpt{has: false},
			wantVerifyError: false,
			wantSignError:   false,
			wantSignedOrg:   orgOpt{has: false},
		},
		{
			name:            "csr_absent_ctx_default_valid_inject_context",
			ctxOrg:          orgOpt{has: true, id: NullOrgID},
			csrOrg:          orgOpt{has: false},
			wantVerifyError: false,
			wantSignError:   false,
			wantSignedOrg:   orgOpt{has: true, id: NullOrgID},
		},
		{
			name:            "csr_absent_ctx_non_default_valid_inject_context",
			ctxOrg:          orgOpt{has: true, id: nonDefault},
			csrOrg:          orgOpt{has: false},
			wantVerifyError: false,
			wantSignError:   false,
			wantSignedOrg:   orgOpt{has: true, id: nonDefault},
		},
		{
			name:            "csr_present_ctx_absent_verify_allows",
			ctxOrg:          orgOpt{has: false},
			csrOrg:          orgOpt{has: true, id: nonDefault},
			wantVerifyError: false,
			wantSignError:   false,
			wantSignedOrg:   orgOpt{has: true, id: nonDefault},
		},
		{
			name:            "csr_present_ctx_present_match_valid_inject_csr",
			ctxOrg:          orgOpt{has: true, id: nonDefault},
			csrOrg:          orgOpt{has: true, id: nonDefault},
			wantVerifyError: false,
			wantSignedOrg:   orgOpt{has: true, id: nonDefault},
		},
		{
			name:            "csr_present_ctx_present_mismatch_verify_fails",
			ctxOrg:          orgOpt{has: true, id: uuid.New()},
			csrOrg:          orgOpt{has: true, id: nonDefault},
			wantVerifyError: true,
			wantSignError:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wrapped := WithOrgIDExtension(factory)(ca)
			ca.registerMockSigner(wrapped)

			csr := csrFrom(tc.csrOrg)
			ctx := ctxFrom(tc.ctxOrg)

			req, err := NewSignRequest(mockSignerName, *csr, WithResourceName("foo"))
			if err != nil {
				t.Fatalf("NewSignRequest: %v", err)
			}

			err = Verify(ctx, ca, req)
			switch {
			case tc.wantVerifyError && err == nil:
				t.Fatalf("expected verify error, got nil")
			case !tc.wantVerifyError && err != nil:
				t.Fatalf("unexpected verify error: %v", err)
			}

			cert, err := Sign(ctx, ca, req)
			switch {
			case tc.wantSignError && err == nil:
				t.Fatalf("expected sign error, got nil")
			case tc.wantSignError && err != nil:
				return // expected error, no need to check cert
			case !tc.wantSignError && err != nil:
				t.Fatalf("unexpected sign error: %v", err)
			}

			got, extExists, err := GetOrgIDExtensionFromCert(cert)
			if err != nil {
				t.Fatalf("GetOrgIDExtensionFromCert: %v", err)
			}

			switch {
			case !tc.wantSignedOrg.has && extExists:
				t.Fatalf("expected no OrgID extension, got %s", got)
			case tc.wantSignedOrg.has && !extExists:
				t.Fatalf("expected OrgID extension, got none")
			case tc.wantSignedOrg.has && extExists && got != tc.wantSignedOrg.id:
				t.Fatalf("expected OrgID %s, but got %s", tc.wantSignedOrg.id, got)
			}

		})
	}
}
