package v1beta1

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestPolicyAccountingRoundTrip(t *testing.T) {
	for _, limit := range []*int32{nil, lo.ToPtr(int32(0)), lo.ToPtr(int32(math.MaxInt32))} {
		for _, count := range []int64{0, math.MaxInt32 + 1, math.MaxInt64} {
			t.Run(fmt.Sprintf("When limit is %v and count is %d it should preserve both widths", limit, count), func(t *testing.T) {
				converter := NewEnrollmentApprovalPolicyConverter()
				policy := api.EnrollmentApprovalPolicy{Spec: api.EnrollmentApprovalPolicySpec{ApprovalLimit: limit}, Status: &api.EnrollmentApprovalPolicyStatus{EnrollmentCount: count}}
				resource := converter.ToDomain(policy)
				encoded, err := json.Marshal(converter.FromDomain(&resource))
				require.NoError(t, err)
				var decoded api.EnrollmentApprovalPolicy
				require.NoError(t, json.Unmarshal(encoded, &decoded))
				require.Equal(t, limit, decoded.Spec.ApprovalLimit)
				require.Equal(t, count, decoded.Status.EnrollmentCount)
				require.Equal(t, policy.Status, converter.StatusFromDomain(resource.Status))
			})
		}
	}
}
