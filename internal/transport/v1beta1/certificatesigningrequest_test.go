package transportv1beta1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	orgID := uuid.New()
	revokedAt := time.Date(2025, 3, 5, 6, 7, 8, 0, time.UTC)

	type testCase struct {
		name          string
		expectService func(service *certificatesigningrequestservice.MockService)
		send          func(t *testing.T, serverURL string) (int, []byte)
		wantStatus    int
		check         func(t *testing.T, body []byte)
	}

	cases := []testCase{
		{
			name: "When the generated client revokes a CSR it should return nested server-owned metadata",
			expectService: func(service *certificatesigningrequestservice.MockService) {
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
			},
			send: func(t *testing.T, serverURL string) (int, []byte) {
				client, err := apiclient.NewClientWithResponses(serverURL)
				require.NoError(t, err)
				response, err := client.RevokeCertificateSigningRequestWithResponse(context.Background(), "issued-csr", apiv1beta1.EnrollmentCertificateRevocationRequest{
					Reason: "Credential compromised",
				})
				require.NoError(t, err)
				return response.StatusCode(), response.Body
			},
			wantStatus: http.StatusOK,
			check: func(t *testing.T, body []byte) {
				var csr apiv1beta1.CertificateSigningRequest
				require.NoError(t, json.Unmarshal(body, &csr))
				require.NotNil(t, csr.Status)
				require.NotNil(t, csr.Status.Revocation)
				require.Equal(t, "Credential compromised", csr.Status.Revocation.Reason)
				require.Equal(t, "security-officer", csr.Status.Revocation.RevokedBy)
				require.Equal(t, revokedAt, csr.Status.Revocation.RevokedAt)
			},
		},
		{
			name: "When the request supplies server-owned revocation metadata it should reject the body",
			send: func(t *testing.T, serverURL string) (int, []byte) {
				response, err := http.Post(serverURL+"/certificatesigningrequests/issued-csr/revocation", "application/json",
					strings.NewReader(`{"reason":"Credential compromised","revokedBy":"attacker"}`))
				require.NoError(t, err)
				defer response.Body.Close()
				recorder := httptest.NewRecorder()
				_, err = recorder.Body.ReadFrom(response.Body)
				require.NoError(t, err)
				return response.StatusCode, recorder.Body.Bytes()
			},
			wantStatus: http.StatusBadRequest,
			check: func(t *testing.T, body []byte) {
				require.Contains(t, string(body), "unknown field")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			service := certificatesigningrequestservice.NewMockService(gomock.NewController(t))
			if tc.expectService != nil {
				tc.expectService(service)
			}
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

			status, body := tc.send(t, testServer.URL)
			require.Equal(t, tc.wantStatus, status)
			tc.check(t, body)
		})
	}
}
