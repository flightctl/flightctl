package transportv1beta1

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	enrollmentrequestservice "github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestApproveEnrollmentRequestRejectsUnknownFields(t *testing.T) {
	service := enrollmentrequestservice.NewMockService(gomock.NewController(t))
	handler := &TransportHandler{enrollmentrequest: service, converter: convertv1beta1.NewConverter()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/enrollmentrequests/test/approval",
		strings.NewReader(`{"approved":true,"status":{"conditions":[]}}`),
	)

	handler.ApproveEnrollmentRequest(response, request, "test")

	require.Equal(t, http.StatusBadRequest, response.Code)
	assert.Contains(t, response.Body.String(), `unknown field`)
}

func TestApproveEnrollmentRequestAcceptsSchemaFields(t *testing.T) {
	orgID := uuid.New()
	ctx := util.WithOrganizationID(context.Background(), orgID)
	service := enrollmentrequestservice.NewMockService(gomock.NewController(t))
	service.EXPECT().ApproveEnrollmentRequest(gomock.Any(), orgID, "test", domain.EnrollmentRequestApproval{
		Approved: true,
	}).Return(&domain.EnrollmentRequestApprovalStatus{Approved: true}, domain.StatusOK())
	handler := &TransportHandler{enrollmentrequest: service, converter: convertv1beta1.NewConverter()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPut,
		"/enrollmentrequests/test/approval",
		strings.NewReader(`{"approved":true}`),
	).WithContext(ctx)

	handler.ApproveEnrollmentRequest(response, request, "test")

	require.Equal(t, http.StatusOK, response.Code)
}
