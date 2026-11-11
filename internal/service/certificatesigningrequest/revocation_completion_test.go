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

	t.Run("When a revoked credential has no completion time it should record it", func(t *testing.T) {
		h, store, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		name := "revoked-credential"
		store.items[name] = &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.CertificateSigningRequestStatus{
				Revocation: &domain.EnrollmentCertificateRevocation{Reason: "Compromised"},
			},
		}

		result, status := h.MarkEnrollmentCredentialFlaggingCompleted(context.Background(), orgId, name)

		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.NotNil(t, result.Status.Revocation.FlaggingCompletedAt)
		require.Equal(t, completedAt, *result.Status.Revocation.FlaggingCompletedAt)
	})

	t.Run("When completion is already recorded it should preserve the original time", func(t *testing.T) {
		h, store, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		name := "already-completed"
		existing := completedAt.Add(-time.Hour)
		store.items[name] = &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status: &domain.CertificateSigningRequestStatus{
				Revocation: &domain.EnrollmentCertificateRevocation{
					Reason:              "Compromised",
					FlaggingCompletedAt: &existing,
				},
			},
		}

		result, status := h.MarkEnrollmentCredentialFlaggingCompleted(context.Background(), orgId, name)

		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Equal(t, existing, *result.Status.Revocation.FlaggingCompletedAt)
	})

	t.Run("When the credential is not revoked it should not record completion", func(t *testing.T) {
		h, store, _, _, _ := newTestHandler(t)
		orgId := uuid.New()
		name := "unrevoked-credential"
		store.items[name] = &domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Status:   &domain.CertificateSigningRequestStatus{},
		}

		result, status := h.MarkEnrollmentCredentialFlaggingCompleted(context.Background(), orgId, name)

		require.Equal(t, statusSuccessCode, status.Code)
		require.NotNil(t, result)
		require.Nil(t, result.Status.Revocation)
	})

	t.Run("When the credential is missing it should return not found", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t)

		_, status := h.MarkEnrollmentCredentialFlaggingCompleted(context.Background(), uuid.New(), "missing")

		require.Equal(t, statusNotFoundCode, status.Code)
	})

	t.Run("When the name is empty it should return bad request", func(t *testing.T) {
		h, _, _, _, _ := newTestHandler(t)

		_, status := h.MarkEnrollmentCredentialFlaggingCompleted(context.Background(), uuid.New(), "")

		require.Equal(t, statusBadRequestCode, status.Code)
	})
}
