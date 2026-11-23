package transportv1beta1

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
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
	"github.com/flightctl/flightctl/internal/store"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type routePolicyStore struct {
	policystore.Store
	policy     *domain.EnrollmentApprovalPolicy
	credential *domain.CertificateSigningRequest
	writes     int
}

func (memory *routePolicyStore) Get(context.Context, uuid.UUID, string) (*domain.EnrollmentApprovalPolicy, error) {
	return store.CloneJSON(memory.policy)
}

func (memory *routePolicyStore) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}
func (memory *routePolicyStore) GetCredential(context.Context, uuid.UUID, string) (*domain.CertificateSigningRequest, error) {
	return memory.credential, nil
}
func (memory *routePolicyStore) Mutate(_ context.Context, _ uuid.UUID, _ string, apply policystore.ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, bool, error) {
	current, err := store.CloneJSON(memory.policy)
	if err != nil {
		return nil, nil, false, err
	}
	mutation := &store.Mutation[domain.EnrollmentApprovalPolicy]{}
	mutation.SetResource(current)
	if err := apply(mutation); err != nil {
		return nil, nil, false, err
	}
	previous := memory.policy
	memory.policy = mutation.Resource()
	memory.writes++
	return memory.policy, previous, previous == nil, nil
}

type acceptingRoutePolicyValidator struct{}

func (acceptingRoutePolicyValidator) ValidateExpression(context.Context, string) error {
	return nil
}

func TestApprovalPolicyPatchRouteQuotaAuthorization(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})

	for _, testCase := range []struct {
		name        string
		operation   string
		quotaCheck  bool
		allowQuota  bool
		unavailable bool
		wantStatus  int
		wantLimit   *int32
		wantLabels  map[string]string
	}{
		{"When canonical quota add is denied it should not write", `{"op":"add","path":"/spec/approvalLimit","value":999}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When lowercase quota alias is denied it should not write", `{"op":"add","path":"/spec/approvallimit","value":999}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When an uppercase quota field is denied it should not write", `{"op":"replace","path":"/spec","value":{"certificateName":"credential","matchExpression":"true","deviceLabels":{},"APPROVALLIMIT":999}}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When a mixed case quota field is denied it should not write", `{"op":"replace","path":"/spec","value":{"certificateName":"credential","matchExpression":"true","deviceLabels":{},"ApprovalLimit":999}}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When quota replacement is denied it should not write", `{"op":"replace","path":"/spec/approvalLimit","value":999}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When quota removal is denied it should not write", `{"op":"remove","path":"/spec/approvalLimit"}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When whole spec replacement is denied it should not write", `{"op":"replace","path":"/spec","value":{"certificateName":"credential","matchExpression":"true","deviceLabels":{},"enabled":false,"approvallimit":999}}`, true, false, false, 403, lo.ToPtr(int32(10)), nil},
		{"When a whole spec retains the limit it should update without quota permission", `{"op":"replace","path":"/spec","value":{"certificateName":"credential","matchExpression":"true","deviceLabels":{"region":"west"},"ApprovalLimit":10}}`, false, false, false, 200, lo.ToPtr(int32(10)), map[string]string{"region": "west"}},
		{"When removing a missing spec member it should reject the patch", `{"op":"replace","path":"/spec","value":{"certificateName":"credential","matchExpression":"true","deviceLabels":{},"ApprovalLimit":10}},{"op":"remove","path":"/spec/approvallimit"}`, false, false, false, 400, lo.ToPtr(int32(10)), nil},
		{"When multiple operations restore the limit it should update without quota permission", `{"op":"remove","path":"/spec/approvalLimit"},{"op":"add","path":"/spec/approvalLimit","value":10}`, false, false, false, 200, lo.ToPtr(int32(10)), nil},
		{"When canonical quota change is authorized it should write", `{"op":"add","path":"/spec/approvalLimit","value":999}`, true, true, false, 200, lo.ToPtr(int32(999)), nil},
		{"When lowercase alias is authorized it should write", `{"op":"add","path":"/spec/approvallimit","value":999}`, true, true, false, 200, lo.ToPtr(int32(999)), nil},
		{"When an uppercase quota field is authorized it should write", `{"op":"replace","path":"/spec","value":{"certificateName":"credential","matchExpression":"true","deviceLabels":{},"APPROVALLIMIT":999}}`, true, true, false, 200, lo.ToPtr(int32(999)), nil},
		{"When quota removal is authorized it should write", `{"op":"remove","path":"/spec/approvalLimit"}`, true, true, false, 200, nil, nil},
		{"When quota is only tested it should not require quota permission", `{"op":"test","path":"/spec/approvalLimit","value":10}`, false, false, false, 200, lo.ToPtr(int32(10)), nil},
		{"When a label resembles quota it should preserve its case without quota permission", `{"op":"add","path":"/spec/deviceLabels/ApprovalLimit","value":"west"}`, false, false, false, 200, lo.ToPtr(int32(10)), map[string]string{"ApprovalLimit": "west"}},
		{"When status is changed it should remain protected", `{"op":"add","path":"/status","value":{}}`, false, false, false, 400, lo.ToPtr(int32(10)), nil},
		{"When certificate binding is changed it should remain immutable", `{"op":"replace","path":"/spec/certificateName","value":"other"}`, false, false, false, 409, lo.ToPtr(int32(10)), nil},
		{"When validation is unavailable it should allow a canonical draft quota change", `{"op":"add","path":"/spec/approvalLimit","value":999}`, true, true, true, 200, lo.ToPtr(int32(999)), nil},
		{"When validation is unavailable it should allow a draft quota alias change", `{"op":"add","path":"/spec/approvallimit","value":999}`, true, true, true, 200, lo.ToPtr(int32(999)), nil},
		{"When validation is unavailable it should allow a draft label change", `{"op":"add","path":"/spec/deviceLabels/region","value":"west"}`, false, false, true, 200, lo.ToPtr(int32(10)), map[string]string{"region": "west"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			memory := &routePolicyStore{
				policy: &domain.EnrollmentApprovalPolicy{
					ApiVersion: "v1beta1", Kind: "EnrollmentApprovalPolicy",
					Metadata: domain.ObjectMeta{Name: lo.ToPtr("west"), ResourceVersion: lo.ToPtr("1")},
					Spec:     domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}, ApprovalLimit: lo.ToPtr(int32(10))},
				},
				credential: &domain.CertificateSigningRequest{Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment"}, Status: &domain.CertificateSigningRequestStatus{Certificate: &certificate}},
			}
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
			body := fmt.Sprintf(`[{"op":"test","path":"/metadata/resourceVersion","value":"1"},%s]`, testCase.operation)
			request := httptest.NewRequest(http.MethodPatch, "/api/v1/enrollmentapprovalpolicies/west", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json-patch+json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			require.Equal(t, testCase.wantStatus, response.Code, response.Body.String())
			require.Equal(t, testCase.wantLimit, memory.policy.Spec.ApprovalLimit)
			if testCase.wantStatus == http.StatusOK {
				var result apiv1beta1.EnrollmentApprovalPolicy
				require.NoError(t, json.Unmarshal(response.Body.Bytes(), &result))
				require.Equal(t, testCase.wantLimit, result.Spec.ApprovalLimit)
			} else {
				require.Zero(t, memory.writes)
			}
			if testCase.wantLabels != nil {
				require.Equal(t, testCase.wantLabels, memory.policy.Spec.DeviceLabels)
			}
		})
	}
}
