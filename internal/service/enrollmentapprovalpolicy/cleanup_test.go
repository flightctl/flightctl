package enrollmentapprovalpolicy_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func cleanupPolicy(t *testing.T, service *policyservice.ServiceHandler, orgID uuid.UUID, policy domain.EnrollmentApprovalPolicy, operation string) domain.Status {
	t.Helper()
	policy.Status = nil
	policy.Spec.Enabled = false
	switch operation {
	case "delete":
		return service.DeleteEnrollmentApprovalPolicy(t.Context(), orgID, *policy.Metadata.Name)
	case "put":
		_, status := service.ReplaceEnrollmentApprovalPolicy(t.Context(), orgID, *policy.Metadata.Name, policy)
		return status
	default:
		_, status := service.PatchEnrollmentApprovalPolicy(t.Context(), orgID, *policy.Metadata.Name, domain.PatchRequest{
			{Op: "test", Path: "/metadata/resourceVersion", Value: *policy.Metadata.ResourceVersion},
			{Op: "replace", Path: "/spec/enabled", Value: false},
		})
		return status
	}
}

func invalidatePolicyCredential(memory *policyMemoryStore, state string) {
	if state == "revoked" {
		memory.credential.Status.Revocation = &domain.EnrollmentCertificateRevocation{}
	} else {
		time.Sleep(2 * time.Hour)
	}
}

func TestPolicyCleanupAfterCredentialInvalidation(t *testing.T) {
	for _, state := range []string{"revoked", "expired"} {
		for _, operation := range []string{"put", "patch", "delete"} {
			for _, unavailable := range []bool{false, true} {
				t.Run(state+"/"+operation+"/unavailable="+lo.Ternary(unavailable, "true", "false"), func(t *testing.T) {
					synctest.Test(t, func(t *testing.T) {
						memory, policy := policyFixture(t)
						policy.Spec.Enabled = true
						validator := &testPolicyValidator{}
						service := policyservice.NewServiceHandler(memory, validator, "enrollment", nil, logrus.New())
						created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
						require.Equal(t, int32(201), status.Code)
						created.Status.EnrollmentCount = 9
						invalidatePolicyCredential(memory, state)
						if unavailable {
							service = policyservice.NewServiceHandler(memory, policyservice.UnavailablePolicySetValidator{}, "enrollment", events.NewMockService(gomock.NewController(t)), logrus.New())
						}
						status = cleanupPolicy(t, service, memory.orgID, *created, operation)
						if unavailable {
							require.Equal(t, int32(503), status.Code)
							require.Equal(t, "PolicyValidationUnavailable", status.Reason)
							require.Equal(t, 1, memory.writes)
							require.True(t, memory.policy.Spec.Enabled)
							require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
						} else {
							require.Equal(t, int32(200), status.Code, status.Message)
							require.Equal(t, 2, memory.writes)
							if operation == "delete" {
								require.Nil(t, memory.policy)
								require.Nil(t, validator.candidate)
							} else {
								require.False(t, memory.policy.Spec.Enabled)
								require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
								require.NotNil(t, validator.candidate)
							}
						}
					})
				})
			}
		}
	}
}

func TestPolicyInvalidCredentialRejectsNonCleanup(t *testing.T) {
	for _, state := range []string{"revoked", "expired"} {
		for _, operation := range []string{"create-disabled", "create-enabled", "enable", "edit-disabled"} {
			t.Run(state+"/"+operation, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					memory, policy := policyFixture(t)
					service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
					created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
					require.Equal(t, int32(201), status.Code)
					invalidatePolicyCredential(memory, state)
					candidate := *created
					candidate.Status = nil
					switch operation {
					case "create-disabled", "create-enabled":
						candidate.Metadata.Name = lo.ToPtr("another")
						candidate.Metadata.ResourceVersion = nil
						candidate.Spec.Enabled = operation == "create-enabled"
						_, status = service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, candidate)
					case "enable":
						_, status = service.PatchEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", domain.PatchRequest{{Op: "test", Path: "/metadata/resourceVersion", Value: "1"}, {Op: "replace", Path: "/spec/enabled", Value: true}})
					default:
						candidate.Spec.MatchExpression = "false"
						_, status = service.ReplaceEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", candidate)
					}
					require.Equal(t, int32(400), status.Code)
					require.Equal(t, 1, memory.writes)
					require.False(t, memory.policy.Spec.Enabled)
				})
			})
		}
	}
}

func TestPolicyCleanupPreservesEligibilityAndIntegrity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *policyMemoryStore, *domain.EnrollmentApprovalPolicy, *uuid.UUID)
		code   int32
	}{
		{"signer", func(_ *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Spec.SignerName = "other"
		}, 400},
		{"unissued", func(_ *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Status.Certificate = nil
		}, 400},
		{"malformed", func(_ *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Status.Certificate = lo.ToPtr([]byte("invalid"))
		}, 400},
		{"not-yet-valid", func(t *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Status.Certificate = lo.ToPtr(policyCertificate(t, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)))
		}, 400},
		{"issuance", func(_ *testing.T, _ *policyMemoryStore, policy *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			policy.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(1))
		}, 400},
		{"binding", func(_ *testing.T, _ *policyMemoryStore, policy *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			policy.Spec.CertificateName = "other"
		}, 409},
		{"version", func(_ *testing.T, _ *policyMemoryStore, policy *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			policy.Metadata.ResourceVersion = lo.ToPtr("stale")
		}, 409},
		{"tenant", func(_ *testing.T, _ *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, orgID *uuid.UUID) {
			*orgID = uuid.New()
		}, 404},
		{"shape", func(_ *testing.T, _ *policyMemoryStore, policy *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			policy.Spec.MatchExpression = ""
		}, 400},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			memory, policy := policyFixture(t)
			policy.Spec.Enabled = true
			service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
			created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
			require.Equal(t, int32(201), status.Code)
			created.Status.EnrollmentCount = 9
			candidate := *created
			orgID := memory.orgID
			memory.credential.Status.Revocation = &domain.EnrollmentCertificateRevocation{}
			testCase.change(t, memory, &candidate, &orgID)
			status = cleanupPolicy(t, service, orgID, candidate, "put")
			require.Equal(t, testCase.code, status.Code)
			require.Equal(t, 1, memory.writes)
			require.True(t, memory.policy.Spec.Enabled)
			require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
		})
	}
}

func TestPolicyCleanupRejectsProtectedStatus(t *testing.T) {
	for _, operation := range []string{"put", "patch"} {
		t.Run(operation, func(t *testing.T) {
			memory, policy := policyFixture(t)
			policy.Spec.Enabled = true
			service := policyservice.NewServiceHandler(memory, &testPolicyValidator{}, "enrollment", nil, logrus.New())
			created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
			require.Equal(t, int32(201), status.Code)
			memory.credential.Status.Revocation = &domain.EnrollmentCertificateRevocation{}
			candidate := *created
			candidate.Spec.Enabled = false
			if operation == "put" {
				_, status = service.ReplaceEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", candidate)
			} else {
				_, status = service.PatchEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", domain.PatchRequest{
					{Op: "test", Path: "/metadata/resourceVersion", Value: "1"},
					{Op: "replace", Path: "/spec/enabled", Value: false},
					{Op: "replace", Path: "/status/enrollmentCount", Value: 0},
				})
			}
			require.Equal(t, "ProtectedField", status.Reason)
			require.Equal(t, 1, memory.writes)
			require.True(t, memory.policy.Spec.Enabled)
		})
	}
}
