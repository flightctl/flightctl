package certificatesigningrequest

import (
	"context"
	"strings"
	"testing"
	"time"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type revocationValidationCase struct {
	name         string
	reason       string
	withIdentity bool
	actor        *string
	prepare      func(t *testing.T, store *fakeCertificateSigningRequestStore, cfg *cacfg.Config, ctx context.Context, orgId uuid.UUID) string
	wantStatus   int32
	wantMessage  string
	wantRevoked  bool
	wantEvents   int
	check        func(t *testing.T, persisted *domain.CertificateSigningRequest)
}

func TestRevokeCertificateSigningRequestValidation(t *testing.T) {
	cases := []revocationValidationCase{
		{
			name:         "When the reason is blank it should reject the request without revoking",
			reason:       "",
			withIdentity: true,
			wantStatus:   statusBadRequestCode,
			wantMessage:  "reason is required and cannot be blank",
		},
		{
			name:         "When the reason is only whitespace it should reject the request without revoking",
			reason:       "   ",
			withIdentity: true,
			wantStatus:   statusBadRequestCode,
			wantMessage:  "reason is required and cannot be blank",
		},
		{
			name:         "When the reason exceeds the character limit it should reject the request without revoking",
			reason:       strings.Repeat("x", 1025),
			withIdentity: true,
			wantStatus:   statusBadRequestCode,
			wantMessage:  "reason must be at most 1024 characters",
		},
		{
			name:         "When the raw reason exceeds the character limit before trimming it should reject the request",
			reason:       strings.Repeat(" ", 1500) + "x",
			withIdentity: true,
			wantStatus:   statusBadRequestCode,
			wantMessage:  "reason must be at most 1024 characters",
		},
		{
			name:         "When the reason uses multi-byte characters it should count characters instead of bytes",
			reason:       strings.Repeat("é", 600),
			withIdentity: true,
			wantStatus:   statusSuccessCode,
			wantRevoked:  true,
			wantEvents:   1,
		},
		{
			name:         "When the target is a service-client CSR it should reject it without mutation",
			reason:       "Credential compromised",
			withIdentity: true,
			prepare: func(t *testing.T, store *fakeCertificateSigningRequestStore, cfg *cacfg.Config, ctx context.Context, orgId uuid.UUID) string {
				return insertIssuedCSR(t, store, "service-client-csr", cfg.DeviceSvcClientSignerName, "issued-service-client-certificate")
			},
			wantStatus:  statusBadRequestCode,
			wantMessage: "only enrollment credentials may be revoked",
		},
		{
			name:         "When the target is a management-renewal CSR it should reject it without mutation",
			reason:       "Credential compromised",
			withIdentity: true,
			prepare: func(t *testing.T, store *fakeCertificateSigningRequestStore, cfg *cacfg.Config, ctx context.Context, orgId uuid.UUID) string {
				return insertIssuedCSR(t, store, "management-renewal-csr", cfg.DeviceManagementRenewalSignerName, "issued-management-certificate")
			},
			wantStatus:  statusBadRequestCode,
			wantMessage: "only enrollment credentials may be revoked",
		},
		{
			name:         "When a non-enrollment CSR already has revocation state it should still reject it",
			reason:       "Credential compromised",
			withIdentity: true,
			prepare: func(t *testing.T, store *fakeCertificateSigningRequestStore, cfg *cacfg.Config, ctx context.Context, orgId uuid.UUID) string {
				name := insertIssuedCSR(t, store, "revoked-service-client-csr", cfg.DeviceSvcClientSignerName, "issued-service-client-certificate")
				store.items[name].Status.Revocation = &domain.EnrollmentCertificateRevocation{
					Reason:    "prior revocation",
					RevokedBy: "someone",
					RevokedAt: time.Now().UTC(),
				}
				return name
			},
			wantStatus:  statusBadRequestCode,
			wantMessage: "only enrollment credentials may be revoked",
			check: func(t *testing.T, persisted *domain.CertificateSigningRequest) {
				require.NotNil(t, persisted.Status.Revocation)
				require.Equal(t, "prior revocation", persisted.Status.Revocation.Reason)
			},
		},
		{
			name:         "When the enrollment CSR has no issued certificate it should reject it without mutation",
			reason:       "Credential compromised",
			withIdentity: true,
			prepare: func(t *testing.T, store *fakeCertificateSigningRequestStore, cfg *cacfg.Config, ctx context.Context, orgId uuid.UUID) string {
				name := "unissued-enrollment-csr"
				store.items[name] = &domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
					Spec: domain.CertificateSigningRequestSpec{
						SignerName: cfg.DeviceEnrollmentSignerName,
						Request:    csrPEM(t, name),
						Usages:     validUsages(),
					},
					Status: &domain.CertificateSigningRequestStatus{},
				}
				return name
			},
			wantStatus:  statusConflictCode,
			wantMessage: "only issued enrollment credentials may be revoked",
		},
		{
			name:         "When no authenticated actor is present it should reject the request without revoking",
			reason:       "Credential compromised",
			withIdentity: false,
			prepare: func(t *testing.T, store *fakeCertificateSigningRequestStore, cfg *cacfg.Config, ctx context.Context, orgId uuid.UUID) string {
				return insertIssuedCSR(t, store, "missing-actor-enrollment-csr", cfg.DeviceEnrollmentSignerName, "issued-enrollment-certificate")
			},
			wantStatus:  statusForbiddenCode,
			wantMessage: "an authenticated identity is required to revoke an enrollment credential",
		},
		{
			name:         "When the authenticated identity has an empty username it should reject the request without revoking",
			reason:       "Credential compromised",
			withIdentity: true,
			actor:        lo.ToPtr(""),
			wantStatus:   statusForbiddenCode,
			wantMessage:  "an authenticated identity with a username is required to revoke an enrollment credential",
		},
		{
			name:         "When the authenticated identity has a whitespace-only username it should reject the request without revoking",
			reason:       "Credential compromised",
			withIdentity: true,
			actor:        lo.ToPtr("   "),
			wantStatus:   statusForbiddenCode,
			wantMessage:  "an authenticated identity with a username is required to revoke an enrollment credential",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _, fakeEvents, cfg := newTestHandler(t)
			ctx := context.Background()
			if tc.withIdentity {
				if tc.actor != nil {
					ctx = context.WithValue(ctx, consts.MappedIdentityCtxKey,
						identity.NewMappedIdentity(*tc.actor, "uid", nil, nil, true, nil))
				} else {
					ctx = revocationIdentityContext()
				}
			}
			orgId := uuid.New()
			name := "validation-enrollment-csr"
			if tc.prepare != nil {
				name = tc.prepare(t, store, cfg, ctx, orgId)
			} else {
				createIssuedEnrollmentCSR(t, h, ctx, orgId, cfg, name)
			}

			_, status := h.RevokeCertificateSigningRequest(ctx, orgId, name, domain.EnrollmentCertificateRevocationRequest{
				Reason: tc.reason,
			})
			require.Equal(t, tc.wantStatus, status.Code)
			if tc.wantMessage != "" {
				require.Equal(t, tc.wantMessage, status.Message)
			}

			persisted := assertRevocationPersisted(t, h, ctx, orgId, name)
			switch {
			case tc.check != nil:
				tc.check(t, persisted)
			case tc.wantRevoked:
				require.NotNil(t, persisted.Status.Revocation)
				require.Equal(t, tc.reason, persisted.Status.Revocation.Reason)
			default:
				require.Nil(t, persisted.Status.Revocation)
			}
			require.Len(t, filterRevocationEvents(fakeEvents.events), tc.wantEvents)
		})
	}
}
