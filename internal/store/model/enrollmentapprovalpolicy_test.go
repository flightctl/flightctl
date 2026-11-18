package model

import (
	"testing"

	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestEnrollmentApprovalPolicyConversionOwnsGeneration(t *testing.T) {
	record := &EnrollmentApprovalPolicy{Resource: Resource{Name: "west", Generation: lo.ToPtr(int64(2))}}
	resource, err := record.ToApiResource()
	require.NoError(t, err)
	*resource.Metadata.Generation = 9
	require.Equal(t, int64(2), *record.Generation)
}
