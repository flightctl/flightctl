package model

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentApprovalPolicyConversionOwnsGeneration(t *testing.T) {
	record := &EnrollmentApprovalPolicy{Resource: Resource{Name: "west", Generation: lo.ToPtr(int64(2))}}
	resource, err := record.ToApiResource()
	require.NoError(t, err)
	*resource.Metadata.Generation = 9
	require.Equal(t, int64(2), *record.Generation)
	converted, err := NewEnrollmentApprovalPolicyFromApiResource(resource)
	require.NoError(t, err)
	*converted.Generation = 10
	require.Equal(t, int64(9), *resource.Metadata.Generation)

	record.Generation = nil
	resource, err = record.ToApiResource()
	require.NoError(t, err)
	require.Nil(t, resource.Metadata.Generation)
	converted, err = NewEnrollmentApprovalPolicyFromApiResource(resource)
	require.NoError(t, err)
	require.Nil(t, converted.Generation)
}

func TestPolicyModelNumericRoundTrip(t *testing.T) {
	for _, limit := range []*int32{nil, lo.ToPtr(int32(0)), lo.ToPtr(int32(math.MaxInt32))} {
		for _, count := range []int64{0, math.MaxInt32 + 1, math.MaxInt64} {
			t.Run(fmt.Sprintf("When limit is %v and count is %d it should preserve persisted accounting", limit, count), func(t *testing.T) {
				resource := &domain.EnrollmentApprovalPolicy{Metadata: domain.ObjectMeta{Name: lo.ToPtr("west")}, Spec: domain.EnrollmentApprovalPolicySpec{ApprovalLimit: limit}, Status: &domain.EnrollmentApprovalPolicyStatus{EnrollmentCount: count}}
				record, err := NewEnrollmentApprovalPolicyFromApiResource(resource)
				require.NoError(t, err)
				encoded, err := json.Marshal(record)
				require.NoError(t, err)
				var decoded EnrollmentApprovalPolicy
				require.NoError(t, json.Unmarshal(encoded, &decoded))
				result, err := decoded.ToApiResource()
				require.NoError(t, err)
				require.Equal(t, limit, result.Spec.ApprovalLimit)
				require.Equal(t, count, result.Status.EnrollmentCount)
			})
		}
	}
}
