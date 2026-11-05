package transportv1beta1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	apiserver "github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/domain"
	certificatesigningrequestservice "github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestRevokeCertificateSigningRequestRoute(t *testing.T) {
	t.Run("When the generated client revokes a CSR it should return nested server-owned metadata", func(t *testing.T) {
		orgID := uuid.New()
		ctx := util.WithOrganizationID(context.Background(), orgID)
		service := certificatesigningrequestservice.NewMockService(gomock.NewController(t))
		revokedAt := time.Date(2025, 3, 5, 6, 7, 8, 0, time.UTC)
		service.EXPECT().RevokeCertificateSigningRequest(gomock.Any(), orgID, "issued-csr", apiv1beta1.EnrollmentCertificateRevocationRequest{
			Reason: "Credential compromised",
		}).Return(&domain.CertificateSigningRequest{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr("issued-csr")},
			Status: &domain.CertificateSigningRequestStatus{
				Revocation: &domain.EnrollmentCertificateRevocation{
					Reason:    "Credential compromised",
					RevokedBy: "security-officer",
					RevokedAt: revokedAt,
				},
			},
		}, domain.StatusOK())
		handler := &TransportHandler{
			certificatesigningrequest: service,
			converter:                 convertv1beta1.NewConverter(),
		}
		router := chi.NewRouter()
		router.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(util.WithOrganizationID(r.Context(), orgID)))
			})
		})
		apiserver.HandlerFromMux(handler, router)
		testServer := httptest.NewServer(router)
		defer testServer.Close()

		client, err := apiclient.NewClientWithResponses(testServer.URL)
		require.NoError(t, err)
		response, err := client.RevokeCertificateSigningRequestWithResponse(ctx, "issued-csr", apiv1beta1.EnrollmentCertificateRevocationRequest{
			Reason: "Credential compromised",
		})

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode())
		require.NotNil(t, response.JSON200)
		require.NotNil(t, response.JSON200.Status.Revocation)
		require.Equal(t, "Credential compromised", response.JSON200.Status.Revocation.Reason)
		require.Equal(t, "security-officer", response.JSON200.Status.Revocation.RevokedBy)
		require.Equal(t, revokedAt, response.JSON200.Status.Revocation.RevokedAt)
	})
}
