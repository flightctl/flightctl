package enrollmentapprovalpolicy_test

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	policyservice "github.com/flightctl/flightctl/internal/service/enrollmentapprovalpolicy"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func disableOrDeletePolicy(t *testing.T, service *policyservice.ServiceHandler, orgID uuid.UUID, policy domain.EnrollmentApprovalPolicy, operation string) domain.Status {
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

func TestPolicyDisableOrDeleteIneligibleCredential(t *testing.T) {
	for _, state := range []string{"revoked", "expired"} {
		for _, operation := range []string{"put", "patch", "delete"} {
			t.Run("When the credential is "+state+" it should allow "+operation+" without expression validation", func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					memory, policy := policyFixture(t)
					policy.Spec.Enabled = true
					service := newTestPolicyService(t, memory, &testPolicyValidator{}, nil)
					created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
					require.Equal(t, int32(201), status.Code)
					created.Status.EnrollmentCount = 9
					invalidatePolicyCredential(memory, state)

					service = newTestPolicyService(t, memory, policyservice.UnavailableExpressionValidator{}, nil)
					status = disableOrDeletePolicy(t, service, memory.orgID, *created, operation)
					require.Equal(t, int32(200), status.Code, status.Message)
					require.Equal(t, 2, memory.writes)
					if operation == "delete" {
						require.Nil(t, memory.policy)
					} else {
						require.False(t, memory.policy.Spec.Enabled)
						require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
					}
				})
			})
		}
	}
}

func TestPolicyWritesAfterCredentialInvalidation(t *testing.T) {
	for _, state := range []string{"revoked", "expired"} {
		for _, operation := range []string{"create-disabled", "create-enabled", "enable using put", "enable using patch", "edit-disabled"} {
			t.Run("When "+state+" and "+operation+" it should enforce resulting-state eligibility", func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					memory, policy := policyFixture(t)
					service := newTestPolicyService(t, memory, &testPolicyValidator{}, nil)
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
					case "enable using put":
						candidate.Spec.Enabled = true
						_, status = service.ReplaceEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", candidate)
					case "enable using patch":
						_, status = service.PatchEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", domain.PatchRequest{{Op: "test", Path: "/metadata/resourceVersion", Value: "1"}, {Op: "replace", Path: "/spec/enabled", Value: true}})
					default:
						candidate.Spec.MatchExpression = "false"
						_, status = service.ReplaceEnrollmentApprovalPolicy(t.Context(), memory.orgID, "west", candidate)
					}
					switch operation {
					case "create-disabled":
						require.Equal(t, int32(201), status.Code)
						require.Equal(t, 2, memory.writes)
					case "edit-disabled":
						require.Equal(t, int32(200), status.Code)
						require.Equal(t, 2, memory.writes)
					default:
						require.Equal(t, int32(400), status.Code)
						require.Equal(t, 1, memory.writes)
					}
					require.False(t, memory.policy.Spec.Enabled)
				})
			})
		}
	}
}

func TestPolicyDisablePreservesBindingAndIntegrity(t *testing.T) {
	cases := []struct {
		name   string
		change func(*testing.T, *policyMemoryStore, *domain.EnrollmentApprovalPolicy, *uuid.UUID)
		code   int32
	}{
		{"signer", func(_ *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Spec.SignerName = "other"
		}, 200},
		{"unissued", func(_ *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Status.Certificate = nil
		}, 200},
		{"malformed", func(_ *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Status.Certificate = lo.ToPtr([]byte("invalid"))
		}, 200},
		{"not-yet-valid", func(t *testing.T, memory *policyMemoryStore, _ *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			memory.credential.Status.Certificate = lo.ToPtr(policyCertificate(t, time.Now().Add(time.Hour), time.Now().Add(2*time.Hour)))
		}, 200},
		{"issuance", func(_ *testing.T, _ *policyMemoryStore, policy *domain.EnrollmentApprovalPolicy, _ *uuid.UUID) {
			policy.Spec.AutoApprovalWindowDays = lo.ToPtr(int32(1))
		}, 200},
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
		t.Run("When disabling with "+testCase.name+" it should preserve policy integrity", func(t *testing.T) {
			memory, policy := policyFixture(t)
			policy.Spec.Enabled = true
			service := newTestPolicyService(t, memory, &testPolicyValidator{}, nil)
			created, status := service.CreateEnrollmentApprovalPolicy(t.Context(), memory.orgID, policy)
			require.Equal(t, int32(201), status.Code)
			created.Status.EnrollmentCount = 9
			candidate := *created
			orgID := memory.orgID
			memory.credential.Status.Revocation = &domain.EnrollmentCertificateRevocation{}
			testCase.change(t, memory, &candidate, &orgID)
			status = disableOrDeletePolicy(t, service, orgID, candidate, "put")
			require.Equal(t, testCase.code, status.Code)
			if testCase.code == 200 {
				require.Equal(t, 2, memory.writes)
				require.False(t, memory.policy.Spec.Enabled)
			} else {
				require.Equal(t, 1, memory.writes)
				require.True(t, memory.policy.Spec.Enabled)
			}
			require.Equal(t, int64(9), memory.policy.Status.EnrollmentCount)
		})
	}
}

func TestPolicyDisableRejectsProtectedStatus(t *testing.T) {
	for _, operation := range []string{"put", "patch"} {
		t.Run("When disabling using "+operation+" it should reject protected status", func(t *testing.T) {
			memory, policy := policyFixture(t)
			policy.Spec.Enabled = true
			service := newTestPolicyService(t, memory, &testPolicyValidator{}, nil)
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
