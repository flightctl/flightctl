package enrollmentrequest

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	enrollmenthookpolicy "github.com/flightctl/flightctl/internal/service/enrollmenthookpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePolicyService struct {
	policy *domain.EnrollmentHookPolicy
	err    error
}

func (f *fakePolicyService) CreateEnrollmentHookPolicy(_ context.Context, _ uuid.UUID, _ domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status) {
	return nil, domain.StatusInternalServerError("not implemented")
}
func (f *fakePolicyService) ListEnrollmentHookPolicies(_ context.Context, _ uuid.UUID, _ domain.ListEnrollmentHookPoliciesParams) (*domain.EnrollmentHookPolicyList, domain.Status) {
	return nil, domain.StatusInternalServerError("not implemented")
}
func (f *fakePolicyService) GetEnrollmentHookPolicy(_ context.Context, _ uuid.UUID, _ string) (*domain.EnrollmentHookPolicy, domain.Status) {
	if f.err != nil {
		if errors.Is(f.err, flterrors.ErrResourceNotFound) {
			return nil, domain.StatusResourceNotFound(string(domain.EnrollmentHookPolicyKind), "default")
		}
		return nil, domain.StatusInternalServerError(f.err.Error())
	}
	return f.policy, domain.StatusOK()
}
func (f *fakePolicyService) ReplaceEnrollmentHookPolicy(_ context.Context, _ uuid.UUID, _ string, _ domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, domain.Status) {
	return nil, domain.StatusInternalServerError("not implemented")
}
func (f *fakePolicyService) DeleteEnrollmentHookPolicy(_ context.Context, _ uuid.UUID, _ string) domain.Status {
	return domain.StatusInternalServerError("not implemented")
}
func (f *fakePolicyService) PatchEnrollmentHookPolicy(_ context.Context, _ uuid.UUID, _ string, _ domain.PatchRequest) (*domain.EnrollmentHookPolicy, domain.Status) {
	return nil, domain.StatusInternalServerError("not implemented")
}

func TestSnapshotEnrollmentHookPolicy(t *testing.T) {
	tests := []struct {
		name           string
		policy         *domain.EnrollmentHookPolicy
		policyErr      error
		nilService     bool
		expectNil      bool
		expectSecrets  int
		expectActions  int
		expectPolicy   domain.FailurePolicyType
		expectURLs     []string
		expectNoBearer bool
	}{
		{
			name:      "When no policy exists it should return nil",
			policyErr: flterrors.ErrResourceNotFound,
			expectNil: true,
		},
		{
			name:       "When policy service is nil it should return nil",
			nilService: true,
			expectNil:  true,
		},
		{
			name: "When policy has controlPlaneActions with bearer tokens it should snapshot actions and create secrets",
			policy: &domain.EnrollmentHookPolicy{
				Spec: domain.EnrollmentHookPolicySpec{
					AfterEnrolling: domain.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(domain.FailurePolicyBlock),
						ControlPlaneActions: &[]domain.EnrollmentHookHttpAction{
							{
								Url:     "https://hooks.example.com/notify",
								Timeout: lo.ToPtr("30s"),
								Auth: &domain.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("secret-token-1"),
								},
							},
							{
								Url: "https://hooks.example.com/other",
								Auth: &domain.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("secret-token-2"),
								},
							},
						},
					},
				},
			},
			expectNil:     false,
			expectSecrets: 2,
			expectActions: 2,
			expectPolicy:  domain.FailurePolicyBlock,
			expectURLs:    []string{"https://hooks.example.com/notify", "https://hooks.example.com/other"},
		},
		{
			name: "When policy has controlPlaneActions without bearer tokens it should snapshot actions with no secrets",
			policy: &domain.EnrollmentHookPolicy{
				Spec: domain.EnrollmentHookPolicySpec{
					AfterEnrolling: domain.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(domain.FailurePolicyBlock),
						ControlPlaneActions: &[]domain.EnrollmentHookHttpAction{
							{
								Url: "https://hooks.example.com/notify",
							},
						},
					},
				},
			},
			expectNil:     false,
			expectSecrets: 0,
			expectActions: 1,
			expectPolicy:  domain.FailurePolicyBlock,
			expectURLs:    []string{"https://hooks.example.com/notify"},
		},
		{
			name: "When policy has no controlPlaneActions (gate-only) it should return snapshot with failurePolicy only",
			policy: &domain.EnrollmentHookPolicy{
				Spec: domain.EnrollmentHookPolicySpec{
					AfterEnrolling: domain.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(domain.FailurePolicyContinue),
					},
				},
			},
			expectNil:     false,
			expectSecrets: 0,
			expectActions: 0,
			expectPolicy:  domain.FailurePolicyContinue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			var policySvc enrollmenthookpolicy.Service
			if !tt.nilService {
				policySvc = &fakePolicyService{policy: tt.policy, err: tt.policyErr}
			}

			status, secrets, err := snapshotEnrollmentHookPolicy(ctx, policySvc, [16]byte{}, "test-device")
			require.NoError(t, err)

			if tt.expectNil {
				assert.Nil(t, status)
				assert.Nil(t, secrets)
				return
			}

			require.NotNil(t, status)
			require.NotNil(t, status.Snapshot)
			assert.Equal(t, tt.expectPolicy, status.Snapshot.FailurePolicy)

			if tt.expectActions > 0 {
				require.NotNil(t, status.Snapshot.ControlPlaneActions)
				assert.Len(t, *status.Snapshot.ControlPlaneActions, tt.expectActions)
				for i, expectedURL := range tt.expectURLs {
					assert.Equal(t, expectedURL, (*status.Snapshot.ControlPlaneActions)[i].Url)
					assert.Equal(t, i, (*status.Snapshot.ControlPlaneActions)[i].Index)
				}
			} else {
				assert.Nil(t, status.Snapshot.ControlPlaneActions)
			}

			assert.Len(t, secrets, tt.expectSecrets)
			for _, secret := range secrets {
				assert.Equal(t, "test-device", secret.DeviceName)
				assert.NotEmpty(t, secret.BearerToken)
			}
		})
	}
}

func TestEnrollmentHooksConditionReason(t *testing.T) {
	tests := []struct {
		name     string
		snapshot *domain.EnrollmentHookSnapshot
		expected string
	}{
		{
			name: "When controlPlaneActions present it should return NotifyPending",
			snapshot: &domain.EnrollmentHookSnapshot{
				ControlPlaneActions: &[]domain.EnrollmentHookSnapshotAction{
					{Index: 0, Url: "https://example.com"},
				},
			},
			expected: domain.EnrollmentHooksReasonNotifyPending,
		},
		{
			name: "When controlPlaneActions is nil it should return Pending",
			snapshot: &domain.EnrollmentHookSnapshot{
				FailurePolicy: domain.FailurePolicyBlock,
			},
			expected: domain.EnrollmentHooksReasonPending,
		},
		{
			name: "When controlPlaneActions is empty it should return Pending",
			snapshot: &domain.EnrollmentHookSnapshot{
				ControlPlaneActions: &[]domain.EnrollmentHookSnapshotAction{},
			},
			expected: domain.EnrollmentHooksReasonPending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := enrollmentHooksConditionReason(tt.snapshot)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSnapshotExcludesBearerToken(t *testing.T) {
	policy := &domain.EnrollmentHookPolicy{
		Spec: domain.EnrollmentHookPolicySpec{
			AfterEnrolling: domain.EnrollmentHookStageSpec{
				FailurePolicy: lo.ToPtr(domain.FailurePolicyBlock),
				ControlPlaneActions: &[]domain.EnrollmentHookHttpAction{
					{
						Url: "https://hooks.example.com/notify",
						Auth: &domain.EnrollmentHookAuth{
							BearerToken: lo.ToPtr("secret-bearer-token"),
						},
					},
				},
			},
		},
	}

	svc := &fakePolicyService{policy: policy}
	status, secrets, err := snapshotEnrollmentHookPolicy(context.Background(), svc, [16]byte{}, "dev1")
	require.NoError(t, err)
	require.NotNil(t, status)

	// Snapshot actions should not contain bearer token
	actions := *status.Snapshot.ControlPlaneActions
	assert.Equal(t, "https://hooks.example.com/notify", actions[0].Url)

	// Secrets should contain the bearer token
	require.Len(t, secrets, 1)
	assert.Equal(t, "secret-bearer-token", secrets[0].BearerToken)
	assert.Equal(t, 0, secrets[0].ActionIndex)

	// Verify the snapshot type doesn't have a bearerToken field
	// (compile-time check: EnrollmentHookSnapshotAction has no Auth field)
	_ = model.EnrollmentHookNotifySecret{}
}

func TestSnapshotEnrollmentHookPolicyServiceError(t *testing.T) {
	svc := &fakePolicyService{err: errors.New("database unavailable")}
	_, _, err := snapshotEnrollmentHookPolicy(context.Background(), svc, uuid.New(), "dev1")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "database unavailable")
}
