package organization

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/stretchr/testify/require"
)

func TestWrapWithTracing(t *testing.T) {
	t.Run("When inner is nil it should return nil", func(t *testing.T) {
		require.Nil(t, WrapWithTracing(nil))
	})

	t.Run("When inner is non-nil it should delegate calls and return the result unchanged", func(t *testing.T) {
		handler, _ := newTestHandler([]*model.Organization{})
		traced := WrapWithTracing(handler)
		require.NotNil(t, traced)

		result, status := traced.ListOrganizations(context.Background(), domain.ListOrganizationsParams{})
		expected, expectedStatus := handler.ListOrganizations(context.Background(), domain.ListOrganizationsParams{})
		require.Equal(t, expectedStatus, status)
		require.Equal(t, expected, result)

		allResult, allStatus := traced.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})
		expectedAll, expectedAllStatus := handler.ListAllOrganizations(context.Background(), domain.ListOrganizationsParams{})
		require.Equal(t, expectedAllStatus, allStatus)
		require.Equal(t, expectedAll, allResult)
	})
}
