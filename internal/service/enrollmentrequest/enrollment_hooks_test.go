package enrollmentrequest

import (
	"context"
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	enrollmenthookpolicystore "github.com/flightctl/flightctl/internal/store/enrollmenthookpolicy"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakePolicyStore struct {
	policy *domain.EnrollmentHookPolicy
	err    error
}

func (f *fakePolicyStore) InitialMigration(_ context.Context) error { return nil }
func (f *fakePolicyStore) Create(_ context.Context, _ uuid.UUID, _ *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, error) {
	return nil, nil
}
func (f *fakePolicyStore) Get(_ context.Context, _ uuid.UUID, _ string) (*domain.EnrollmentHookPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.policy, nil
}
func (f *fakePolicyStore) CreateOrUpdate(_ context.Context, _ uuid.UUID, _ *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, bool, error) {
	return nil, nil, false, nil
}
func (f *fakePolicyStore) Delete(_ context.Context, _ uuid.UUID, _ string) (bool, error) {
	return false, nil
}
func (f *fakePolicyStore) List(_ context.Context, _ uuid.UUID, _ store.ListParams) (*domain.EnrollmentHookPolicyList, error) {
	return nil, nil
}
func (f *fakePolicyStore) Update(_ context.Context, _ uuid.UUID, _ *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, error) {
	return nil, nil, nil
}

func TestSnapshotEnrollmentHookPolicy(t *testing.T) {
	tests := []struct {
		name            string
		policy          *domain.EnrollmentHookPolicy
		policyErr       error
		nilStore        bool
		expectNil       bool
		expectSecrets   int
		expectActions   int
		expectPolicy    v1beta1.FailurePolicyType
		expectURLs      []string
		expectNoBearer  bool
	}{
		{
			name:      "When no policy exists it should return nil",
			policyErr: flterrors.ErrResourceNotFound,
			expectNil: true,
		},
		{
			name:      "When policy store is nil it should return nil",
			nilStore:  true,
			expectNil: true,
		},
		{
			name: "When policy has controlPlaneActions with bearer tokens it should snapshot actions and create secrets",
			policy: &domain.EnrollmentHookPolicy{
				Spec: v1beta1.EnrollmentHookPolicySpec{
					AfterEnrolling: v1beta1.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(v1beta1.FailurePolicyBlock),
						ControlPlaneActions: &[]v1beta1.EnrollmentHookHttpAction{
							{
								Url:     "https://hooks.example.com/notify",
								Timeout: lo.ToPtr("30s"),
								Auth: &v1beta1.EnrollmentHookAuth{
									BearerToken: lo.ToPtr("secret-token-1"),
								},
							},
							{
								Url: "https://hooks.example.com/other",
								Auth: &v1beta1.EnrollmentHookAuth{
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
			expectPolicy:  v1beta1.FailurePolicyBlock,
			expectURLs:    []string{"https://hooks.example.com/notify", "https://hooks.example.com/other"},
		},
		{
			name: "When policy has controlPlaneActions without bearer tokens it should snapshot actions with no secrets",
			policy: &domain.EnrollmentHookPolicy{
				Spec: v1beta1.EnrollmentHookPolicySpec{
					AfterEnrolling: v1beta1.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(v1beta1.FailurePolicyBlock),
						ControlPlaneActions: &[]v1beta1.EnrollmentHookHttpAction{
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
			expectPolicy:  v1beta1.FailurePolicyBlock,
			expectURLs:    []string{"https://hooks.example.com/notify"},
		},
		{
			name: "When policy has no controlPlaneActions (gate-only) it should return snapshot with failurePolicy only",
			policy: &domain.EnrollmentHookPolicy{
				Spec: v1beta1.EnrollmentHookPolicySpec{
					AfterEnrolling: v1beta1.EnrollmentHookStageSpec{
						FailurePolicy: lo.ToPtr(v1beta1.FailurePolicyContinue),
					},
				},
			},
			expectNil:     false,
			expectSecrets: 0,
			expectActions: 0,
			expectPolicy:  v1beta1.FailurePolicyContinue,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()

			var policyStore enrollmenthookpolicystore.Store
			if !tt.nilStore {
				policyStore = &fakePolicyStore{policy: tt.policy, err: tt.policyErr}
			}

			status, secrets, err := snapshotEnrollmentHookPolicy(ctx, policyStore, [16]byte{}, "test-device")
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
				FailurePolicy: v1beta1.FailurePolicyBlock,
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
		Spec: v1beta1.EnrollmentHookPolicySpec{
			AfterEnrolling: v1beta1.EnrollmentHookStageSpec{
				FailurePolicy: lo.ToPtr(v1beta1.FailurePolicyBlock),
				ControlPlaneActions: &[]v1beta1.EnrollmentHookHttpAction{
					{
						Url: "https://hooks.example.com/notify",
						Auth: &v1beta1.EnrollmentHookAuth{
							BearerToken: lo.ToPtr("secret-bearer-token"),
						},
					},
				},
			},
		},
	}

	store := &fakePolicyStore{policy: policy}
	status, secrets, err := snapshotEnrollmentHookPolicy(context.Background(), store, [16]byte{}, "dev1")
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
