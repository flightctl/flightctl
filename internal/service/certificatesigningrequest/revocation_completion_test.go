package certificatesigningrequest

import (
	"context"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestMarkEnrollmentCredentialFlaggingCompleted(t *testing.T) {
	completedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	originalNow := nowFunc
	nowFunc = func() time.Time { return completedAt }
	t.Cleanup(func() { nowFunc = originalNow })

	existingCompletion := completedAt.Add(-time.Hour)
	tests := []struct {
		name           string
		credentialName string
		setup          func(*fakeCertificateSigningRequestStore)
		wantCode       int32
		assert         func(*testing.T, *domain.CertificateSigningRequest)
	}{
		{
			name:           "When a revoked credential has no completion time it should record it",
			credentialName: "revoked-credential",
			setup: func(csrStore *fakeCertificateSigningRequestStore) {
				csrStore.items["revoked-credential"] = &domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("revoked-credential")},
					Status: &domain.CertificateSigningRequestStatus{
						Revocation: &domain.EnrollmentCertificateRevocation{Reason: "Compromised"},
					},
				}
			},
			wantCode: statusSuccessCode,
			assert: func(t *testing.T, result *domain.CertificateSigningRequest) {
				require.NotNil(t, result)
				require.NotNil(t, result.Status.Revocation.FlaggingCompletedAt)
				require.Equal(t, completedAt, *result.Status.Revocation.FlaggingCompletedAt)
			},
		},
		{
			name:           "When completion is already recorded it should preserve the original time",
			credentialName: "already-completed",
			setup: func(csrStore *fakeCertificateSigningRequestStore) {
				csrStore.items["already-completed"] = &domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("already-completed")},
					Status: &domain.CertificateSigningRequestStatus{
						Revocation: &domain.EnrollmentCertificateRevocation{
							Reason:              "Compromised",
							FlaggingCompletedAt: &existingCompletion,
						},
					},
				}
			},
			wantCode: statusSuccessCode,
			assert: func(t *testing.T, result *domain.CertificateSigningRequest) {
				require.NotNil(t, result)
				require.Equal(t, existingCompletion, *result.Status.Revocation.FlaggingCompletedAt)
			},
		},
		{
			name:           "When the credential is not revoked it should not record completion",
			credentialName: "unrevoked-credential",
			setup: func(csrStore *fakeCertificateSigningRequestStore) {
				csrStore.items["unrevoked-credential"] = &domain.CertificateSigningRequest{
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("unrevoked-credential")},
					Status:   &domain.CertificateSigningRequestStatus{},
				}
			},
			wantCode: statusSuccessCode,
			assert: func(t *testing.T, result *domain.CertificateSigningRequest) {
				require.NotNil(t, result)
				require.Nil(t, result.Status.Revocation)
			},
		},
		{
			name:           "When the credential is missing it should return not found",
			credentialName: "missing",
			wantCode:       statusNotFoundCode,
			assert: func(t *testing.T, result *domain.CertificateSigningRequest) {
				require.Nil(t, result)
			},
		},
		{
			name:           "When the name is empty it should return bad request",
			credentialName: "",
			wantCode:       statusBadRequestCode,
			assert: func(t *testing.T, result *domain.CertificateSigningRequest) {
				require.Nil(t, result)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, csrStore, _, _, _ := newTestHandler(t)
			if tt.setup != nil {
				tt.setup(csrStore)
			}

			result, status := h.MarkEnrollmentCredentialFlaggingCompleted(context.Background(), uuid.New(), tt.credentialName)

			require.Equal(t, tt.wantCode, status.Code)
			tt.assert(t, result)
		})
	}
}
