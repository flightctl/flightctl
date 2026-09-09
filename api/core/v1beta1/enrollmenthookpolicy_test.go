package v1beta1

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string                      { return &s }
func intPtr(i int) *int                            { return &i }
func fpPtr(f FailurePolicyType) *FailurePolicyType { return &f }

func validEnrollmentHookPolicy() EnrollmentHookPolicy {
	name := "default"
	return EnrollmentHookPolicy{
		Metadata: ObjectMeta{Name: &name},
		Spec: EnrollmentHookPolicySpec{
			AfterEnrolling: EnrollmentHookStageSpec{
				FailurePolicy: fpPtr(FailurePolicyBlock),
				ControlPlaneActions: &[]EnrollmentHookHttpAction{
					{
						Url:     "https://example.com/hook",
						Timeout: strPtr("30s"),
					},
				},
			},
		},
	}
}

func TestEnrollmentHookPolicy_Validate(t *testing.T) {
	t.Run("When name is default it should accept", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		errs := p.Validate()
		assert.Empty(t, errs)
	})

	t.Run("When name is not default it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		name := "other"
		p.Metadata.Name = &name
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), `must be "default"`)
	})

	t.Run("When name is nil it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		p.Metadata.Name = nil
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), `must be "default"`)
	})

	t.Run("When URL uses http scheme it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Url = "http://example.com/hook"
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "HTTPS")
	})

	t.Run("When URL uses loopback address it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Url = "https://127.0.0.1/hook"
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "loopback")
	})

	t.Run("When URL uses link-local address it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Url = "https://169.254.1.1/hook"
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "link-local")
	})

	t.Run("When URL is empty it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Url = ""
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "must not be empty")
	})

	t.Run("When URL is valid HTTPS it should accept", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Url = "https://hooks.example.com:8443/enroll"
		errs := p.Validate()
		assert.Empty(t, errs)
	})

	t.Run("When gate-only policy has no controlPlaneActions it should accept", func(t *testing.T) {
		name := "default"
		p := EnrollmentHookPolicy{
			Metadata: ObjectMeta{Name: &name},
			Spec: EnrollmentHookPolicySpec{
				AfterEnrolling: EnrollmentHookStageSpec{},
			},
		}
		errs := p.Validate()
		assert.Empty(t, errs)
		// Verify default failurePolicy was applied
		require.NotNil(t, p.Spec.AfterEnrolling.FailurePolicy)
		assert.Equal(t, FailurePolicyBlock, *p.Spec.AfterEnrolling.FailurePolicy)
	})

	t.Run("When failurePolicy is empty it should default to Block", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		p.Spec.AfterEnrolling.FailurePolicy = nil
		errs := p.Validate()
		assert.Empty(t, errs)
		require.NotNil(t, p.Spec.AfterEnrolling.FailurePolicy)
		assert.Equal(t, FailurePolicyBlock, *p.Spec.AfterEnrolling.FailurePolicy)
	})

	t.Run("When timeout is omitted it should default to 30s", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Timeout = nil
		errs := p.Validate()
		assert.Empty(t, errs)
		require.NotNil(t, (*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Timeout)
		assert.Equal(t, "30s", *(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Timeout)
	})

	t.Run("When timeout exceeds 5m it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Timeout = strPtr("10m")
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "must not exceed")
	})

	t.Run("When retry fields are omitted it should apply defaults", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Retry = &EnrollmentHookRetryPolicy{}
		errs := p.Validate()
		assert.Empty(t, errs)
		retry := (*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Retry
		assert.Equal(t, 5, *retry.MaxAttempts)
		assert.Equal(t, "exponential", *retry.BackoffPolicy)
		assert.Equal(t, "2s", *retry.BackoffDelay)
		assert.Equal(t, "2m", *retry.MaxBackoff)
		assert.Equal(t, "10m", *retry.Deadline)
	})

	t.Run("When maxAttempts exceeds 20 it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Retry = &EnrollmentHookRetryPolicy{
			MaxAttempts: intPtr(21),
		}
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "maxAttempts")
	})

	t.Run("When maxAttempts is less than 1 it should reject", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Retry = &EnrollmentHookRetryPolicy{
			MaxAttempts: intPtr(0),
		}
		errs := p.Validate()
		require.NotEmpty(t, errs)
		assert.Contains(t, errs[0].Error(), "maxAttempts")
	})
}

func TestEnrollmentHookPolicy_HideSensitiveData(t *testing.T) {
	t.Run("When bearer token is set it should replace with mask", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr("secret-token"),
		}
		err := p.HideSensitiveData()
		require.NoError(t, err)
		assert.Equal(t, MaskedValuePlaceholder, *(*p.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth.BearerToken)
	})

	t.Run("When no auth is set it should be a no-op", func(t *testing.T) {
		p := validEnrollmentHookPolicy()
		err := p.HideSensitiveData()
		require.NoError(t, err)
	})

	t.Run("When list items have bearer tokens it should redact all", func(t *testing.T) {
		p1 := validEnrollmentHookPolicy()
		(*p1.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr("token1"),
		}
		p2 := validEnrollmentHookPolicy()
		(*p2.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr("token2"),
		}
		list := &EnrollmentHookPolicyList{Items: []EnrollmentHookPolicy{p1, p2}}
		err := list.HideSensitiveData()
		require.NoError(t, err)
		assert.Equal(t, MaskedValuePlaceholder, *(*list.Items[0].Spec.AfterEnrolling.ControlPlaneActions)[0].Auth.BearerToken)
		assert.Equal(t, MaskedValuePlaceholder, *(*list.Items[1].Spec.AfterEnrolling.ControlPlaneActions)[0].Auth.BearerToken)
	})
}

func TestEnrollmentHookPolicy_PreserveSensitiveData(t *testing.T) {
	t.Run("When new token is masked it should restore from existing", func(t *testing.T) {
		existing := validEnrollmentHookPolicy()
		(*existing.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr("real-secret"),
		}
		newPolicy := validEnrollmentHookPolicy()
		(*newPolicy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr(MaskedValuePlaceholder),
		}
		err := newPolicy.PreserveSensitiveData(&existing)
		require.NoError(t, err)
		assert.Equal(t, "real-secret", *(*newPolicy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth.BearerToken)
	})

	t.Run("When new token is different it should keep new value", func(t *testing.T) {
		existing := validEnrollmentHookPolicy()
		(*existing.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr("old-secret"),
		}
		newPolicy := validEnrollmentHookPolicy()
		(*newPolicy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth = &EnrollmentHookAuth{
			BearerToken: strPtr("new-secret"),
		}
		err := newPolicy.PreserveSensitiveData(&existing)
		require.NoError(t, err)
		assert.Equal(t, "new-secret", *(*newPolicy.Spec.AfterEnrolling.ControlPlaneActions)[0].Auth.BearerToken)
	})
}
