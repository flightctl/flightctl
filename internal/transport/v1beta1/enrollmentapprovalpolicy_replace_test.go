package transportv1beta1

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	convert "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	apiserver "github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/domain"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type replacePolicyRaceStore struct {
	*routePolicyStore
	advance bool
}

func (memory *replacePolicyRaceStore) Get(ctx context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, error) {
	result, err := memory.routePolicyStore.Get(ctx, orgID, name)
	if memory.advance {
		memory.advance = false
		memory.policy.Metadata.ResourceVersion = lo.ToPtr("2")
		memory.policy.Spec.ApprovalLimit = lo.ToPtr(int32(20))
	}
	return result, err
}

func TestApprovalPolicyReplaceVersionAndQuota(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	for _, testCase := range []struct {
		name         string
		version      string
		limit        string
		initialLimit *int32
		race         bool
		quotaCheck   bool
		allowQuota   bool
		unavailable  bool
		wantStatus   int
	}{
		{"When the submitted version was not authorized it should reject", `"2"`, "10", lo.ToPtr(int32(10)), true, false, false, false, 409},
		{"When the authorized version changes it should conflict under lock", `"1"`, "10", lo.ToPtr(int32(10)), true, false, false, false, 409},
		{"When the version changes it should conflict before the quota check", `"1"`, "30", lo.ToPtr(int32(10)), true, false, false, false, 409},
		{"When quota is unchanged it should not require extra permission", `"1"`, "10", lo.ToPtr(int32(10)), false, false, false, false, 200},
		{"When quota is changed without permission it should reject", `"1"`, "20", lo.ToPtr(int32(10)), false, true, false, false, 403},
		{"When quota is changed with permission it should replace", `"1"`, "20", lo.ToPtr(int32(10)), false, true, true, false, 200},
		{"When nil becomes zero without permission it should reject", `"1"`, "0", nil, false, true, false, false, 403},
		{"When nil becomes zero with permission it should replace", `"1"`, "0", nil, false, true, true, false, 200},
		{"When zero becomes nil without permission it should reject", `"1"`, "null", lo.ToPtr(int32(0)), false, true, false, false, 403},
		{"When nil remains nil it should not require permission", `"1"`, "null", nil, false, false, false, false, 200},
		{"When version is missing it should conflict", "null", "10", lo.ToPtr(int32(10)), false, false, false, false, 409},
		{"When version is stale it should conflict", `"0"`, "10", lo.ToPtr(int32(10)), false, false, false, false, 409},
		{"When version is not a string it should reject input", "1", "10", lo.ToPtr(int32(10)), false, false, false, false, 400},
		{"When validation is unavailable it should allow a disabled policy limit change", `"1"`, "20", lo.ToPtr(int32(10)), false, true, true, true, 200},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			memory := &replacePolicyRaceStore{routePolicyStore: &routePolicyStore{
				policy:     &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west"), ResourceVersion: lo.ToPtr("1")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}, ApprovalLimit: testCase.initialLimit}},
				credential: &domain.CertificateSigningRequest{Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment"}, Status: &domain.CertificateSigningRequestStatus{Certificate: &certificate}},
			}, advance: testCase.race}
			permissions := auth.NewMockAuthZMiddleware(gomock.NewController(t))
			if testCase.quotaCheck {
				permissions.EXPECT().CheckPermission(gomock.Any(), "enrollmentapprovalpolicies/approvallimit", "update").Return(testCase.allowQuota, nil)
			}
			var validator policyservice.ExpressionValidator = acceptingRoutePolicyValidator{}
			if testCase.unavailable {
				validator = policyservice.UnavailableExpressionValidator{}
			}
			service, err := policyservice.NewServiceHandler(memory, validator, "enrollment", nil, logrus.New())
			require.NoError(t, err)
			handler := &TransportHandler{converter: convert.NewConverter(), authZ: permissions, enrollmentapprovalpolicy: service}
			spec, err := apiv1beta1.GetSpec()
			require.NoError(t, err)
			router := chi.NewRouter()
			router.Use(oapimiddleware.OapiRequestValidatorWithOptions(spec, &oapimiddleware.Options{SilenceServersWarning: true}))
			apiserver.HandlerWithOptions(handler, apiserver.ChiServerOptions{BaseRouter: router, BaseURL: "/api/v1"})
			versionField := `,"resourceVersion":` + testCase.version
			if testCase.version == "null" {
				versionField = ""
			}
			limitField := `,"approvalLimit":` + testCase.limit
			if testCase.limit == "null" {
				limitField = ""
			}
			body := fmt.Sprintf(`{"apiVersion":"flightctl.io/v1beta1","kind":"EnrollmentApprovalPolicy","metadata":{"name":"west"%s},"spec":{"certificateName":"credential","matchExpression":"true","deviceLabels":{}%s}}`, versionField, limitField)
			request := httptest.NewRequest(http.MethodPut, "/api/v1/enrollmentapprovalpolicies/west", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, testCase.wantStatus, response.Code, response.Body.String())
			if testCase.wantStatus == http.StatusOK {
				require.Equal(t, 1, memory.writes)
			} else {
				require.Zero(t, memory.writes)
			}
			if testCase.race {
				require.Equal(t, int32(20), *memory.policy.Spec.ApprovalLimit)
			}
		})
	}
}
