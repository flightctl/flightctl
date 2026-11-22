package certificatesigningrequest

import (
	"errors"
	"testing"
	"time"

	cacfg "github.com/flightctl/flightctl/internal/config/ca"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

type revocationBehaviorCase struct {
	name           string
	csrName        string
	reason         string
	createEventErr error
	revokeErr      error
	casMissWith    *domain.EnrollmentCertificateRevocation
	preRevoke      bool
	legacySigner   bool
	wantStatus     int32
	wantRevoked    bool
	wantReason     string
	wantRevokedBy  string
	wantEvents     int
	assertOriginal bool
}

func TestRevokeCertificateSigningRequest(t *testing.T) {
	cases := []revocationBehaviorCase{
		{
			name:           "When revoking an issued enrollment CSR it should return immutable server-owned metadata",
			csrName:        "issued-enrollment-csr",
			reason:         "Commissioning credential compromised",
			wantStatus:     statusSuccessCode,
			wantRevoked:    true,
			wantReason:     "Commissioning credential compromised",
			wantEvents:     1,
			assertOriginal: true,
		},
		{
			name:        "When revoking an already revoked CSR it should return the original revocation",
			csrName:     "already-revoked-enrollment-csr",
			reason:      "Different reason",
			preRevoke:   true,
			wantStatus:  statusSuccessCode,
			wantRevoked: true,
			wantReason:  "Credential compromised",
			wantEvents:  1,
		},
		{
			name:           "When the enrollment credential uses a legacy signer it should revoke it",
			csrName:        "legacy-signer-enrollment-csr",
			reason:         "Legacy credential compromised",
			legacySigner:   true,
			wantStatus:     statusSuccessCode,
			wantRevoked:    true,
			wantReason:     "Legacy credential compromised",
			wantEvents:     1,
			assertOriginal: true,
		},
		{
			name:           "When revocation event persistence fails it should roll back the revocation",
			csrName:        "event-failure-enrollment-csr",
			reason:         "Credential compromised",
			createEventErr: errors.New("event store unavailable"),
			wantStatus:     statusServerErrorCode,
			wantEvents:     0,
		},
		{
			name:    "When another caller already revoked the CSR it should return that revocation without a duplicate event",
			csrName: "concurrent-revoke-enrollment-csr",
			reason:  "Different reason",
			casMissWith: &domain.EnrollmentCertificateRevocation{
				Reason:    "Concurrent revocation",
				RevokedBy: "other-user",
				RevokedAt: time.Date(2025, 3, 5, 6, 7, 8, 0, time.UTC),
			},
			wantStatus:    statusSuccessCode,
			wantRevoked:   true,
			wantReason:    "Concurrent revocation",
			wantRevokedBy: "other-user",
		},
		{
			name:       "When the revocation loses a delete or recreate race it should return a conflict",
			csrName:    "revocation-race-enrollment-csr",
			reason:     "Credential compromised",
			revokeErr:  flterrors.ErrNoRowsUpdated,
			wantStatus: statusConflictCode,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, store, _, fakeEvents, cfg := newTestHandler(t)
			fakeEvents.createErr = tc.createEventErr
			store.revokeErr = tc.revokeErr
			store.casMissRevocation = tc.casMissWith
			ctx := revocationIdentityContext()
			orgId := uuid.New()
			var created *domain.CertificateSigningRequest
			if tc.legacySigner {
				created = insertLegacySignerCSR(t, h, store, ctx, tc.csrName)
			} else {
				created = createIssuedEnrollmentCSR(t, h, ctx, orgId, cfg, tc.csrName)
			}

			if tc.preRevoke {
				_, status := h.RevokeCertificateSigningRequest(ctx, orgId, tc.csrName, domain.EnrollmentCertificateRevocationRequest{Reason: "Credential compromised"})
				require.Equal(t, statusSuccessCode, status.Code)
			}

			revoked, status := h.RevokeCertificateSigningRequest(ctx, orgId, tc.csrName, domain.EnrollmentCertificateRevocationRequest{Reason: tc.reason})
			require.Equal(t, tc.wantStatus, status.Code)
			if tc.wantRevoked {
				wantRevokedBy := tc.wantRevokedBy
				if wantRevokedBy == "" {
					wantRevokedBy = "security-officer"
				}
				require.NotNil(t, revoked)
				require.NotNil(t, revoked.Status.Revocation)
				require.Equal(t, tc.wantReason, revoked.Status.Revocation.Reason)
				require.Equal(t, wantRevokedBy, revoked.Status.Revocation.RevokedBy)
				require.False(t, revoked.Status.Revocation.RevokedAt.IsZero())
				require.Nil(t, revoked.Status.Revocation.FlaggingCompletedAt)
			}

			persisted := assertRevocationPersisted(t, h, ctx, orgId, tc.csrName)
			if tc.wantRevoked {
				require.NotNil(t, persisted.Status.Revocation)
				require.Equal(t, tc.wantReason, persisted.Status.Revocation.Reason)
			} else {
				require.Nil(t, persisted.Status.Revocation)
			}
			revocationEvents := filterRevocationEvents(fakeEvents.events)
			require.Len(t, revocationEvents, tc.wantEvents)
			if len(revocationEvents) > 0 {
				require.Equal(t, domain.EventReasonEnrollmentCredentialRevoked, revocationEvents[0].Reason)
				require.Equal(t, "user:security-officer", revocationEvents[0].Actor)
				require.Equal(t, tc.csrName, revocationEvents[0].InvolvedObject.Name)
			}
			if tc.assertOriginal {
				require.Equal(t, created.Status.Certificate, persisted.Status.Certificate)
				require.Equal(t, created.Status.IssuedAt, persisted.Status.IssuedAt)
				require.Equal(t, created.Status.Conditions, persisted.Status.Conditions)
			}
		})
	}
}

func TestRevokeCertificateSigningRequestKeepsOtherIssuanceIndependent(t *testing.T) {
	signers := []struct {
		name       string
		signerName func(*cacfg.Config) string
	}{
		{name: "management renewal", signerName: func(cfg *cacfg.Config) string { return cfg.DeviceManagementRenewalSignerName }},
		{name: "service client", signerName: func(cfg *cacfg.Config) string { return cfg.DeviceSvcClientSignerName }},
	}

	for _, signer := range signers {
		t.Run("When the enrollment credential is revoked it should still issue "+signer.name+" certificates", func(t *testing.T) {
			h, store, _, _, cfg := newTestHandler(t)
			ctx := revocationIdentityContext()
			orgId := uuid.New()
			enrollment := createIssuedEnrollmentCSR(t, h, ctx, orgId, cfg, "revoked-enrollment-csr")
			_, status := h.RevokeCertificateSigningRequest(ctx, orgId, *enrollment.Metadata.Name, domain.EnrollmentCertificateRevocationRequest{
				Reason: "Credential compromised",
			})
			require.Equal(t, statusSuccessCode, status.Code)

			name := "independent-issuance-csr"
			csr := &domain.CertificateSigningRequest{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), CreationTimestamp: lo.ToPtr(time.Now().UTC())},
				Spec: domain.CertificateSigningRequestSpec{
					SignerName: signer.signerName(cfg),
					Request:    csrPEM(t, name),
					Usages:     validUsages(),
				},
				Status: &domain.CertificateSigningRequestStatus{Conditions: []domain.Condition{}},
			}
			store.items[name] = csr

			approval := *csr
			approval.Status = &domain.CertificateSigningRequestStatus{
				Conditions: []domain.Condition{{
					Type:   domain.ConditionTypeCertificateSigningRequestApproved,
					Status: domain.ConditionStatusTrue,
				}},
			}
			signed, approvalStatus := h.UpdateCertificateSigningRequestApproval(ctx, orgId, name, approval)
			require.Equal(t, statusSuccessCode, approvalStatus.Code)
			require.NotNil(t, signed.Status.Certificate)
			require.NotNil(t, store.items[*enrollment.Metadata.Name].Status.Revocation)
		})
	}
}
