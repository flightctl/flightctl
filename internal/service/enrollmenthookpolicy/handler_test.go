package enrollmenthookpolicy

import (
	"context"
	"testing"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/events"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	policies map[string]*domain.EnrollmentHookPolicy
	err      error
}

func newFakeStore() *fakeStore {
	return &fakeStore{policies: map[string]*domain.EnrollmentHookPolicy{}}
}

func (f *fakeStore) InitialMigration(_ context.Context) error { return f.err }

func (f *fakeStore) Create(_ context.Context, _ uuid.UUID, p *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	name := lo.FromPtr(p.Metadata.Name)
	if _, exists := f.policies[name]; exists {
		return nil, flterrors.ErrDuplicateName
	}
	f.policies[name] = p
	return p, nil
}

func (f *fakeStore) Update(_ context.Context, _ uuid.UUID, p *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, error) {
	if f.err != nil {
		return nil, nil, f.err
	}
	name := lo.FromPtr(p.Metadata.Name)
	old, exists := f.policies[name]
	if !exists {
		return nil, nil, flterrors.ErrResourceNotFound
	}
	f.policies[name] = p
	return p, old, nil
}

func (f *fakeStore) CreateOrUpdate(_ context.Context, orgId uuid.UUID, p *domain.EnrollmentHookPolicy) (*domain.EnrollmentHookPolicy, *domain.EnrollmentHookPolicy, bool, error) {
	name := lo.FromPtr(p.Metadata.Name)
	if _, exists := f.policies[name]; exists {
		result, old, err := f.Update(context.Background(), orgId, p)
		return result, old, false, err
	}
	result, err := f.Create(context.Background(), orgId, p)
	return result, nil, true, err
}

func (f *fakeStore) Get(_ context.Context, _ uuid.UUID, name string) (*domain.EnrollmentHookPolicy, error) {
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.policies[name]
	if !ok {
		return nil, flterrors.ErrResourceNotFound
	}
	return p, nil
}

func (f *fakeStore) List(_ context.Context, _ uuid.UUID, _ store.ListParams) (*domain.EnrollmentHookPolicyList, error) {
	if f.err != nil {
		return nil, f.err
	}
	var items []domain.EnrollmentHookPolicy
	for _, p := range f.policies {
		items = append(items, *p)
	}
	return &domain.EnrollmentHookPolicyList{Items: items}, nil
}

func (f *fakeStore) Delete(_ context.Context, _ uuid.UUID, name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if _, exists := f.policies[name]; !exists {
		return false, nil
	}
	delete(f.policies, name)
	return true, nil
}

type fakeEventsService struct {
	events.Service
	createdEvents int
}

func (f *fakeEventsService) CreateEvent(_ context.Context, _ uuid.UUID, _ *domain.Event) {
	f.createdEvents++
}

func (f *fakeEventsService) HandleGenericResourceDeletedEvents(_ context.Context, _ domain.ResourceKind, _ uuid.UUID, _ string, _, _ interface{}, _ bool, _ error) {
	f.createdEvents++
}

func newTestHandler() (*ServiceHandler, *fakeStore, *fakeEventsService) {
	fs := newFakeStore()
	fe := &fakeEventsService{}
	h := NewServiceHandler(fs, fe, logrus.New())
	return h, fs, fe
}

func strPtr(s string) *string                                       { return &s }
func fpPtr(f v1beta1.FailurePolicyType) *v1beta1.FailurePolicyType { return &f }

func validPolicy() domain.EnrollmentHookPolicy {
	name := "default"
	return domain.EnrollmentHookPolicy{
		Metadata: domain.ObjectMeta{Name: &name},
		Spec: domain.EnrollmentHookPolicySpec{
			AfterEnrolling: domain.EnrollmentHookStageSpec{
				FailurePolicy: fpPtr(v1beta1.FailurePolicyBlock),
				ControlPlaneActions: &[]domain.EnrollmentHookHttpAction{
					{
						Url:     "https://example.com/hook",
						Timeout: strPtr("30s"),
					},
				},
			},
		},
	}
}

func TestCreateEnrollmentHookPolicy(t *testing.T) {
	t.Run("When policy is valid it should create and set Ready condition", func(t *testing.T) {
		h, _, fakeEvents := newTestHandler()
		policy := validPolicy()

		result, status := h.CreateEnrollmentHookPolicy(context.Background(), uuid.New(), policy)
		assert.Equal(t, domain.StatusCreated().Code, status.Code)
		require.NotNil(t, result)
		require.NotNil(t, result.Status)
		require.NotNil(t, result.Status.Conditions)
		assert.Len(t, *result.Status.Conditions, 1)
		assert.Equal(t, domain.ConditionType("Ready"), (*result.Status.Conditions)[0].Type)
		assert.Equal(t, domain.ConditionStatusTrue, (*result.Status.Conditions)[0].Status)
		assert.Equal(t, 1, fakeEvents.createdEvents)
	})

	t.Run("When policy has validation errors it should reject", func(t *testing.T) {
		h, _, _ := newTestHandler()
		policy := validPolicy()
		badName := "not-default"
		policy.Metadata.Name = &badName

		result, status := h.CreateEnrollmentHookPolicy(context.Background(), uuid.New(), policy)
		assert.Equal(t, int32(400), status.Code)
		assert.Nil(t, result)
	})

	t.Run("When gate-only policy is valid it should create", func(t *testing.T) {
		h, _, _ := newTestHandler()
		name := "default"
		policy := domain.EnrollmentHookPolicy{
			Metadata: domain.ObjectMeta{Name: &name},
			Spec: domain.EnrollmentHookPolicySpec{
				AfterEnrolling: domain.EnrollmentHookStageSpec{},
			},
		}

		result, status := h.CreateEnrollmentHookPolicy(context.Background(), uuid.New(), policy)
		assert.Equal(t, domain.StatusCreated().Code, status.Code)
		require.NotNil(t, result)
	})
}

func TestReplaceEnrollmentHookPolicy(t *testing.T) {
	t.Run("When name mismatch it should reject", func(t *testing.T) {
		h, _, _ := newTestHandler()
		policy := validPolicy()
		wrongName := "wrong"
		policy.Metadata.Name = &wrongName

		result, status := h.ReplaceEnrollmentHookPolicy(context.Background(), uuid.New(), "default", policy)
		assert.Equal(t, int32(400), status.Code)
		assert.Nil(t, result)
	})

	t.Run("When valid it should preserve sensitive data and set Ready", func(t *testing.T) {
		h, fs, fakeEvents := newTestHandler()
		orgId := uuid.New()
		existing := validPolicy()
		(*existing.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &domain.EnrollmentHookAuth{
			BearerToken: strPtr("real-secret"),
		}
		fs.policies["default"] = &existing

		policy := validPolicy()
		(*policy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &domain.EnrollmentHookAuth{
			BearerToken: strPtr("*****"),
		}

		result, status := h.ReplaceEnrollmentHookPolicy(context.Background(), orgId, "default", policy)
		assert.Equal(t, domain.StatusOK().Code, status.Code)
		require.NotNil(t, result)
		// Verify sensitive data was preserved
		assert.Equal(t, "real-secret", *(*result.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth.BearerToken)
		assert.Equal(t, 1, fakeEvents.createdEvents)
	})
}

func TestDeleteEnrollmentHookPolicy(t *testing.T) {
	t.Run("When policy exists it should delete and emit event", func(t *testing.T) {
		h, fs, fakeEvents := newTestHandler()
		orgId := uuid.New()
		existing := validPolicy()
		fs.policies["default"] = &existing

		status := h.DeleteEnrollmentHookPolicy(context.Background(), orgId, "default")
		assert.Equal(t, domain.StatusOK().Code, status.Code)
		assert.Equal(t, 1, fakeEvents.createdEvents)
	})
}

func TestGetEnrollmentHookPolicy(t *testing.T) {
	t.Run("When policy exists it should return it", func(t *testing.T) {
		h, fs, _ := newTestHandler()
		orgId := uuid.New()
		expected := validPolicy()
		fs.policies["default"] = &expected

		result, status := h.GetEnrollmentHookPolicy(context.Background(), orgId, "default")
		assert.Equal(t, domain.StatusOK().Code, status.Code)
		require.NotNil(t, result)
	})
}

func TestListEnrollmentHookPolicies(t *testing.T) {
	t.Run("When listing it should delegate to store", func(t *testing.T) {
		h, fs, _ := newTestHandler()
		orgId := uuid.New()
		p := validPolicy()
		fs.policies["default"] = &p

		result, status := h.ListEnrollmentHookPolicies(context.Background(), orgId, domain.ListEnrollmentHookPoliciesParams{})
		assert.Equal(t, domain.StatusOK().Code, status.Code)
		require.NotNil(t, result)
		assert.Len(t, result.Items, 1)
	})
}

func TestSanitizeEnrollmentHookPolicy(t *testing.T) {
	t.Run("When policy has status and managed metadata it should clear them", func(t *testing.T) {
		policy := validPolicy()
		policy.Status = &domain.EnrollmentHookPolicyStatus{
			Conditions: &[]domain.Condition{{Type: "Ready", Status: domain.ConditionStatusTrue}},
		}

		SanitizeEnrollmentHookPolicy(&policy)
		assert.Nil(t, policy.Status)
	})

	t.Run("When policy is nil it should not panic", func(t *testing.T) {
		SanitizeEnrollmentHookPolicy(nil)
	})
}

func TestFromUntrustedHelpers(t *testing.T) {
	t.Run("When CreateFromUntrusted is called it should sanitize before creating", func(t *testing.T) {
		h, _, _ := newTestHandler()
		policy := validPolicy()
		policy.Status = &domain.EnrollmentHookPolicyStatus{
			Conditions: &[]domain.Condition{{Type: "Ready", Status: domain.ConditionStatusTrue}},
		}

		result, status := CreateEnrollmentHookPolicyFromUntrusted(context.Background(), h, uuid.New(), policy)
		assert.Equal(t, domain.StatusCreated().Code, status.Code)
		require.NotNil(t, result)
		// The handler should have re-set the status conditions (not the original untrusted ones)
		require.NotNil(t, result.Status)
	})
}
