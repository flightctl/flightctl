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
	getError   error
	lastError  error
}

func (*policyMemoryStore) InitialMigration(context.Context) error { return nil }
func (memory *policyMemoryStore) Get(_ context.Context, orgID uuid.UUID, name string) (*domain.EnrollmentApprovalPolicy, error) {
	if memory.getError != nil {
		return nil, memory.getError
	}
	if orgID != memory.orgID || memory.policy == nil || lo.FromPtr(memory.policy.Metadata.Name) != name {
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
func (memory *policyMemoryStore) WithPolicySetTransaction(ctx context.Context, _ uuid.UUID, _, _ string, fn func(context.Context, *domain.CertificateSigningRequest, []domain.EnrollmentApprovalPolicy) error) error {
	return fn(ctx, memory.credential, nil)
}
func (memory *policyMemoryStore) Delete(context.Context, uuid.UUID, string) error {
	memory.policy = nil
	memory.writes++
	return nil
}
func (memory *policyMemoryStore) Mutate(ctx context.Context, orgID uuid.UUID, name string, apply policystore.ApplyFunc) (*domain.EnrollmentApprovalPolicy, *domain.EnrollmentApprovalPolicy, bool, error) {
	current, _ := memory.Get(ctx, orgID, name)
	previous, err := store.CloneJSON(current)
	if err != nil {
		return nil, nil, false, err
	}
	var status *domain.EnrollmentApprovalPolicyStatus
	if current != nil {
		status = current.Status
	}
	m := &store.Mutation[domain.EnrollmentApprovalPolicy]{}
	m.SetResource(current)
	if err := apply(m); err != nil {
		memory.lastError = err
		return nil, nil, false, err
	}
	candidate := m.Resource()
	candidate.Status = &domain.EnrollmentApprovalPolicyStatus{}
	if status != nil {
		candidate.Status = status
	}
	candidate.Metadata.ResourceVersion = lo.ToPtr("1")
	memory.policy = candidate
	memory.writes++
	return candidate, previous, previous == nil, nil
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
		t.Run(map[bool]string{false: "When disabled it should fail closed", true: "When enabled it should fail closed"}[enabled], func(t *testing.T) {
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
	var decoded any
	jsonError := json.Unmarshal([]byte(`{"enabled":`), &decoded)
	var syntaxError *json.SyntaxError
	require.ErrorAs(t, jsonError, &syntaxError)
	sentinel := errors.New("validation cause")
	cause := errors.Join(sentinel, jsonError)
	for _, testCase := range []struct {
		name    string
		err     error
		target  any
		code    int32
		reason  string
		message string
	}{
		{"semantic value", policyservice.InvalidPolicyError{Message: "unsupported expression", Cause: cause}, new(policyservice.InvalidPolicyError), 400, "InvalidPolicy", "unsupported expression"},
		{"semantic pointer", &policyservice.InvalidPolicyError{Message: "unsupported expression", Cause: cause}, new(*policyservice.InvalidPolicyError), 400, "InvalidPolicy", "unsupported expression"},
		{"input value", policyservice.InvalidPolicyInputError{Cause: cause}, new(policyservice.InvalidPolicyInputError), 400, "Bad Request", cause.Error()},
		{"input pointer", &policyservice.InvalidPolicyInputError{Cause: cause}, new(*policyservice.InvalidPolicyInputError), 400, "Bad Request", cause.Error()},
		{"overlap value", policyservice.PolicyOverlapError{PolicyName: "other"}, new(policyservice.PolicyOverlapError), 409, "PolicyOverlap", "enabled policy overlaps or cannot be proved disjoint from policy other"},
		{"overlap pointer", &policyservice.PolicyOverlapError{PolicyName: "other"}, new(*policyservice.PolicyOverlapError), 409, "PolicyOverlap", "enabled policy overlaps or cannot be proved disjoint from policy other"},
	} {
		for _, form := range []string{"direct", "wrapped"} {
			t.Run("When a "+form+" "+testCase.name+" is returned it should preserve classification and causes", func(t *testing.T) {
				injected := testCase.err
				if form == "wrapped" {
					injected = fmt.Errorf("validate: %w", injected)
				}
				memory, policy := policyFixture(t)
				service := policyservice.NewServiceHandler(memory, &testPolicyValidator{err: injected}, "enrollment", nil, logrus.New())
				result, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
				require.Nil(t, result)
				require.Zero(t, memory.writes)
				require.ErrorIs(t, memory.lastError, injected)
				require.ErrorIs(t, memory.lastError, testCase.err)
				require.ErrorAs(t, memory.lastError, testCase.target)
				if testCase.reason != "PolicyOverlap" {
					require.ErrorIs(t, memory.lastError, sentinel)
					var actual *json.SyntaxError
					require.ErrorAs(t, memory.lastError, &actual)
					require.Same(t, syntaxError, actual)
				}
				require.Equal(t, testCase.code, status.Code)
				require.Equal(t, testCase.reason, status.Reason)
				require.Equal(t, testCase.message, status.Message)
			})
		}
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
	fetched, status := service.GetEnrollmentApprovalPolicy(context.Background(), memory.orgID, "west")
	require.Equal(t, int32(200), status.Code)
	require.Zero(t, fetched.Status.EnrollmentCount)
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
		t.Run("When patching "+path+" it should reject protected fields", func(t *testing.T) {
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
	for _, operation := range []string{"list", "get"} {
		t.Run("When storage fails during "+operation+" it should hide internal details", func(t *testing.T) {
			memory, _ := policyFixture(t)
			cause := errors.New("internal database connection detail")
			memory.listError, memory.getError = cause, cause
			service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
			var status domain.Status
			if operation == "list" {
				_, status = service.ListEnrollmentApprovalPolicies(t.Context(), memory.orgID, domain.ListEnrollmentApprovalPoliciesParams{})
			} else {
				_, status = service.GetEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west")
			}
			require.Equal(t, int32(500), status.Code)
			require.Equal(t, "Internal Server Error", status.Reason)
			require.NotContains(t, status.Message, cause.Error())
		})
	}
}

func TestPolicyServiceDependencies(t *testing.T) {
	memory, _ := policyFixture(t)
	t.Run("When the logger is nil it should panic", func(t *testing.T) {
		require.Panics(t, func() { policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, nil) })
	})
	t.Run("When events are absent it should construct", func(t *testing.T) {
		require.NotNil(t, policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New()))
	})
}

func TestPolicyUnknownCredential(t *testing.T) {
	memory, policy := policyFixture(t)
	memory.credential = nil
	service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
	_, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
	require.Equal(t, int32(400), status.Code)
	require.Equal(t, "InvalidPolicy", status.Reason)
	require.Contains(t, status.Message, "spec.certificateName")
	require.Contains(t, status.Message, policy.Spec.CertificateName)
	require.Zero(t, memory.writes)
}

func TestPolicyPatchCauseAndClassification(t *testing.T) {
	memory, policy := policyFixture(t)
	service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
	_, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
	require.Equal(t, int32(201), status.Code)
	_, status = service.PatchEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", domain.PatchRequest{
		{Op: "test", Path: "/metadata/resourceVersion", Value: "1"},
		{Op: "add", Path: "/spec/approvalLimit", Value: "wrong type"},
	})
	require.Equal(t, int32(400), status.Code)
	require.Equal(t, "Bad Request", status.Reason)
	var cause *json.UnmarshalTypeError
	require.ErrorAs(t, memory.lastError, &cause)
	require.Equal(t, 1, memory.writes)
}

func TestPolicySemanticCauseTakesPrecedence(t *testing.T) {
	for _, storageCause := range []struct {
		name string
		err  error
	}{
		{"notfound", flterrors.ErrResourceNotFound},
		{"conflict", flterrors.ErrResourceVersionConflict},
	} {
		semantic := policyservice.InvalidPolicyError{Message: "invalid binding", Cause: storageCause.err}
		for _, testCase := range []struct {
			name    string
			err     error
			reason  string
			message string
		}{
			{"semantic value", semantic, "InvalidPolicy", "invalid binding"},
			{"semantic pointer", &semantic, "InvalidPolicy", "invalid binding"},
			{"input value", policyservice.InvalidPolicyInputError{Cause: storageCause.err}, "Bad Request", storageCause.err.Error()},
			{"input pointer", &policyservice.InvalidPolicyInputError{Cause: storageCause.err}, "Bad Request", storageCause.err.Error()},
			{"input value wrapping semantic pointer", policyservice.InvalidPolicyInputError{Cause: &semantic}, "InvalidPolicy", "invalid binding"},
			{"input pointer wrapping semantic value", &policyservice.InvalidPolicyInputError{Cause: semantic}, "InvalidPolicy", "invalid binding"},
		} {
			t.Run("When a wrapped "+testCase.name+" contains "+storageCause.name+" it should preserve policy classification", func(t *testing.T) {
				memory, policy := policyFixture(t)
				injected := fmt.Errorf("validate: %w", testCase.err)
				validator := &testPolicyValidator{err: injected}
				service := policyservice.NewServiceHandler(memory, validator, "enrollment", nil, logrus.New())
				result, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
				require.Nil(t, result)
				require.Zero(t, memory.writes)
				require.ErrorIs(t, memory.lastError, injected)
				require.ErrorIs(t, memory.lastError, storageCause.err)
				require.Equal(t, int32(400), status.Code)
				require.Equal(t, testCase.reason, status.Reason)
				require.Equal(t, testCase.message, status.Message)
			})
		}
	}
}
