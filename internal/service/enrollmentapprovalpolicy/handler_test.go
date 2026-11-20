package enrollmentapprovalpolicy_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	policystore "github.com/flightctl/flightctl/internal/store/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type policyMemoryStore struct {
	policy     *domain.EnrollmentApprovalPolicy
	credential *domain.CertificateSigningRequest
	orgID      uuid.UUID
	writes     int
	listError  error
}

func (*policyMemoryStore) InitialMigration(context.Context) error { return nil }
func (memory *policyMemoryStore) Get(_ context.Context, orgID uuid.UUID, _ string) (*domain.EnrollmentApprovalPolicy, error) {
	if orgID != memory.orgID || memory.policy == nil {
		return nil, flterrors.ErrResourceNotFound
	}
	encoded, _ := json.Marshal(memory.policy)
	var result domain.EnrollmentApprovalPolicy
	_ = json.Unmarshal(encoded, &result)
	return &result, nil
}
func (memory *policyMemoryStore) List(context.Context, uuid.UUID, store.ListParams) (*domain.EnrollmentApprovalPolicyList, error) {
	return &domain.EnrollmentApprovalPolicyList{}, memory.listError
}

func TestPolicyListSelectorFailure(t *testing.T) {
	memory, _ := policyFixture(t)
	memory.listError = fmt.Errorf("listing: %w", selector.NewSelectorError(flterrors.ErrFieldSelectorUnknownSelector, fmt.Errorf("unsupported field")))
	service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
	_, status := service.ListEnrollmentApprovalPolicies(t.Context(), memory.orgID, domain.ListEnrollmentApprovalPoliciesParams{})
	require.Equal(t, int32(400), status.Code)
}
func (memory *policyMemoryStore) Mutate(ctx context.Context, orgID uuid.UUID, name, _ string, operation policystore.Operation, apply policystore.ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, error) {
	if orgID != memory.orgID {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	current, _ := memory.Get(ctx, orgID, name)
	previous, err := store.CloneJSON(current)
	if err != nil {
		return nil, nil, err
	}
	var existingStatus *domain.EnrollmentApprovalPolicyStatus
	if current != nil {
		existingStatus = current.Status
	}
	candidate, err := apply(current, memory.credential, nil)
	if err != nil {
		return nil, nil, err
	}
	memory.writes++
	if operation == policystore.Delete {
		memory.policy = nil
		return nil, previous, nil
	}
	candidate.Status = &domain.EnrollmentApprovalPolicyStatus{}
	if current != nil {
		candidate.Status = existingStatus
	}
	candidate.Metadata.ResourceVersion = lo.ToPtr("1")
	memory.policy = candidate
	return candidate, previous, nil
}

type testPolicyValidator struct {
	err       error
	candidate *domain.EnrollmentApprovalPolicy
	enabled   []domain.EnrollmentApprovalPolicy
}

func (validator *testPolicyValidator) ValidatePolicySet(_ context.Context, candidate *domain.EnrollmentApprovalPolicy, enabled []domain.EnrollmentApprovalPolicy) error {
	validator.candidate, validator.enabled = candidate, enabled
	return validator.err
}

func policyCertificate(t *testing.T, notBefore, notAfter time.Time) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: notBefore, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func policyFixture(t *testing.T) (*policyMemoryStore, domain.EnrollmentApprovalPolicy) {
	t.Helper()
	certificate := policyCertificate(t, time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	memory := &policyMemoryStore{orgID: uuid.New(), credential: &domain.CertificateSigningRequest{Spec: domain.CertificateSigningRequestSpec{SignerName: "enrollment"}, Status: &domain.CertificateSigningRequestStatus{Certificate: &certificate}}}
	return memory, domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west")}, Spec: domain.EnrollmentApprovalPolicySpec{CertificateName: "credential", MatchExpression: "true", DeviceLabels: map[string]string{}}}
}

func TestPolicyUnavailableDoesNotWrite(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "enabled"}[enabled], func(t *testing.T) {
			memory, policy := policyFixture(t)
			policy.Spec.Enabled = enabled
			service := policyservice.NewServiceHandler(memory, policyservice.UnavailablePolicySetValidator{}, "enrollment", nil, logrus.New())
			result, status := service.CreateEnrollmentApprovalPolicy(context.Background(), memory.orgID, policy)
			require.Nil(t, result)
			require.Equal(t, int32(503), status.Code)
			require.Equal(t, "PolicyValidationUnavailable", status.Reason)
			require.Zero(t, memory.writes)
		})
	}
}

func TestPolicyUnavailableRejectsExistingDraftMutationsWithoutEvents(t *testing.T) {
	memory, policy := policyFixture(t)
	policy.Metadata.ResourceVersion = lo.ToPtr("1")
	policy.Status = &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 9}
	memory.policy = &policy
	eventService := events.NewMockService(gomock.NewController(t))
	service := policyservice.NewServiceHandler(memory, policyservice.UnavailablePolicySetValidator{}, "enrollment", eventService, logrus.New())
	replacement := policy
	replacement.Status = nil
	_, status := service.ReplaceEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", replacement)
	require.Equal(t, "PolicyValidationUnavailable", status.Reason)
	_, status = service.PatchEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", domain.PatchRequest{{Op: "test", Path: "/metadata/resourceVersion", Value: "1"}, {Op: "replace", Path: "/spec/enabled", Value: true}})
	require.Equal(t, "PolicyValidationUnavailable", status.Reason)
	status = service.DeleteEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west")
	require.Equal(t, "PolicyValidationUnavailable", status.Reason)
	require.Zero(t, memory.writes)
	require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
}

func TestPolicyValidationErrorMapping(t *testing.T) {
	for _, testCase := range []struct {
		err    error
		code   int32
		reason string
	}{
		{policyservice.InvalidPolicyError{Message: "unsupported expression"}, 400, "InvalidPolicy"},
		{policyservice.PolicyOverlapError{PolicyName: "other"}, 409, "PolicyOverlap"},
	} {
		memory, policy := policyFixture(t)
		service := policyservice.NewServiceHandler(memory, &testPolicyValidator{err: testCase.err}, "enrollment", nil, logrus.New())
		_, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
		require.Equal(t, testCase.code, status.Code)
		require.Equal(t, testCase.reason, status.Reason)
		require.Zero(t, memory.writes)
	}
}

func TestPolicyLifecycleContracts(t *testing.T) {
	memory, policy := policyFixture(t)
	validator := &testPolicyValidator{}
	service := policyservice.NewServiceHandler(memory, validator, "enrollment", nil, logrus.New())
	created, status := service.CreateEnrollmentApprovalPolicy(context.Background(), memory.orgID, policy)
	require.Equal(t, int32(201), status.Code)
	require.Zero(t, created.Status.EnrollmentCount)
	require.NotNil(t, validator.candidate)
	require.Empty(t, validator.enabled)
	count, status := service.GetEnrollmentApprovalPolicyStatus(context.Background(), memory.orgID, "west")
	require.Equal(t, int32(200), status.Code)
	require.Zero(t, count.EnrollmentCount)
	_, status = service.GetEnrollmentApprovalPolicy(context.Background(), uuid.New(), "west")
	require.Equal(t, int32(404), status.Code)
	policy.Metadata.ResourceVersion = lo.ToPtr("stale")
	_, status = service.ReplaceEnrollmentApprovalPolicy(context.Background(), memory.orgID, "west", policy)
	require.Equal(t, int32(409), status.Code)
	policy.Metadata.ResourceVersion = lo.ToPtr("1")
	policy.Spec.CertificateName = "other"
	_, status = service.ReplaceEnrollmentApprovalPolicy(context.Background(), memory.orgID, "west", policy)
	require.Equal(t, "ImmutableCertificateBinding", status.Reason)
	policy.Spec.CertificateName = "credential"
	policy.Status = &domain.EnrollmentApprovalPolicyStatus{}
	_, status = service.ReplaceEnrollmentApprovalPolicy(context.Background(), memory.orgID, "west", policy)
	require.Equal(t, "ProtectedField", status.Reason)
	_, status = service.PatchEnrollmentApprovalPolicy(context.Background(), memory.orgID, "west", nil)
	require.Equal(t, "ResourceVersionConflict", status.Reason)
	require.Equal(t, 1, memory.writes)
}

func TestPolicyCredentialValidation(t *testing.T) {
	cases := []struct {
		name   string
		change func(*policyMemoryStore, *domain.EnrollmentApprovalPolicy)
	}{
		{"When signer differs it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Spec.SignerName = "other"
		}},
		{"When certificate is unissued it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status.Certificate = nil
		}},
		{"When credential status is absent it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status = nil
		}},
		{"When credential is revoked it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status.Revocation = &domain.EnrollmentCertificateRevocation{}
		}},
		{"When certificate PEM is malformed it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status.Certificate = lo.ToPtr([]byte("invalid certificate"))
		}},
		{"When certificate PEM has trailing data it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			certificate := append([]byte(nil), (*memory.credential.Status.Certificate)...)
			memory.credential.Status.Certificate = lo.ToPtr(append(certificate, []byte("unexpected trailing data")...))
		}},
		{"When certificate DER is malformed it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status.Certificate = lo.ToPtr(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("invalid DER")}))
		}},
		{"When certificate has expired it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status.Certificate = lo.ToPtr(policyCertificate(t, time.Now().Add(-2*time.Hour), time.Now().Add(-time.Hour)))
		}},
		{"When certificate is not yet valid it should reject", func(memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy) {
			memory.credential.Status.Certificate = lo.ToPtr(policyCertificate(t, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)))
		}},
		{"When window has no issuance time it should reject", func(_ *policyMemoryStore, policy *domain.EnrollmentApprovalPolicy) {
			policy.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(1))
		}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			memory, policy := policyFixture(t)
			testCase.change(memory, &policy)
			service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
			_, status := service.CreateEnrollmentApprovalPolicy(context.Background(), memory.orgID, policy)
			require.Equal(t, int32(400), status.Code)
			require.Zero(t, memory.writes)
		})
	}
}

func TestPolicyPatchProtectsStatusAndMetadata(t *testing.T) {
	for _, path := range []string{"/status", "/status/enrollmentCount", "/metadata", "/metadata/name", "/metadata/resourceVersion", "/metadata/annotations", "/metadata/annotations/flightctl.io~1server-note", "/kind", "/apiVersion", ""} {
		t.Run(path, func(t *testing.T) {
			memory, policy := policyFixture(t)
			policy.Metadata.ResourceVersion = lo.ToPtr("1")
			policy.Status = &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: 9}
			memory.policy = &policy
			service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
			_, status := service.PatchEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", domain.PatchRequest{
				{Op: "test", Path: "/metadata/resourceVersion", Value: "1"},
				{Op: "remove", Path: path},
			})
			require.Equal(t, int32(400), status.Code)
			require.Equal(t, "ProtectedField", status.Reason)
			require.Zero(t, memory.writes)
			require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
		})
	}
}

func TestPolicyReplacementPreservesOmittedMetadata(t *testing.T) {
	memory, policy := policyFixture(t)
	service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
	created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
	require.Equal(t, int32(201), status.Code)
	memory.policy.Metadata.Labels = lo.ToPtr(map[string]string{"region": "west"})
	memory.policy.Metadata.Annotations = lo.ToPtr(map[string]string{"flightctl.io/server-note": "retained"})
	replacement := *created
	replacement.Status = nil
	replacement.Metadata.Labels = nil
	replacement.Metadata.Annotations = nil
	updated, status := service.ReplaceEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", replacement)
	require.Equal(t, int32(200), status.Code)
	require.Equal(t, "west", (*updated.Metadata.Labels)["region"])
	require.Equal(t, "retained", (*updated.Metadata.Annotations)["flightctl.io/server-note"])
}

func TestPolicyUnexpectedStorageFailureUsesInternalServerError(t *testing.T) {
	memory, _ := policyFixture(t)
	memory.listError = errors.New("database unavailable")
	service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
	_, status := service.ListEnrollmentApprovalPolicies(t.Context(), memory.orgID, domain.ListEnrollmentApprovalPoliciesParams{})
	require.Equal(t, int32(500), status.Code)
}
