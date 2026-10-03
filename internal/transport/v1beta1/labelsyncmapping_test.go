package transportv1beta1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	apiv1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestGetDeviceLabelSyncProvenance(t *testing.T) {
	orgID := uuid.New()
	ctx := util.WithOrganizationID(context.Background(), orgID)
	service := labelsyncmappingservice.NewMockService(gomock.NewController(t))
	service.EXPECT().GetDeviceLabelSyncProvenance(gomock.Any(), orgID, "edge-01").Return(&domain.LabelSyncProvenanceList{
		Items: []domain.LabelSyncProvenanceItem{{Key: "hardware.architecture", Owners: []string{"device-facts"}}},
	}, domain.StatusOK())
	handler := &TransportHandler{labelsyncmapping: service, converter: convertv1beta1.NewConverter()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/devices/edge-01/labelsyncprovenance", nil).WithContext(ctx)

	handler.GetDeviceLabelSyncProvenance(response, request, "edge-01")

	require.Equal(t, http.StatusOK, response.Code)
	var body apiv1beta1.LabelSyncProvenanceList
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	assert.Equal(t, []apiv1beta1.LabelSyncProvenanceItem{{Key: "hardware.architecture", Owners: []string{"device-facts"}}}, body.Items)
}

func TestGetLabelSyncProvenance(t *testing.T) {
	orgID := uuid.New()
	ctx := util.WithOrganizationID(context.Background(), orgID)
	keys := []string{"hardware.architecture", "operator.label"}
	service := labelsyncmappingservice.NewMockService(gomock.NewController(t))
	service.EXPECT().GetLabelSyncProvenance(gomock.Any(), orgID, keys).Return(&domain.LabelSyncProvenanceList{
		Items: []domain.LabelSyncProvenanceItem{
			{Key: "hardware.architecture", Owners: []string{"device-facts", "hardware-map"}},
			{Key: "operator.label", Owners: []string{}},
		},
	}, domain.StatusOK())
	handler := &TransportHandler{labelsyncmapping: service, converter: convertv1beta1.NewConverter()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/labelsyncprovenance?labelKeys=hardware.architecture&labelKeys=operator.label", nil).WithContext(ctx)

	handler.GetLabelSyncProvenance(response, request, apiv1beta1.GetLabelSyncProvenanceParams{LabelKeys: keys})

	require.Equal(t, http.StatusOK, response.Code)
	var body apiv1beta1.LabelSyncProvenanceList
	require.NoError(t, json.NewDecoder(response.Body).Decode(&body))
	assert.Equal(t, []apiv1beta1.LabelSyncProvenanceItem{
		{Key: "hardware.architecture", Owners: []string{"device-facts", "hardware-map"}},
		{Key: "operator.label", Owners: []string{}},
	}, body.Items)
}

func TestGetDeviceLabelSyncProvenanceReturnsServiceError(t *testing.T) {
	service := labelsyncmappingservice.NewMockService(gomock.NewController(t))
	service.EXPECT().GetDeviceLabelSyncProvenance(gomock.Any(), uuid.Nil, "missing").Return(nil, domain.StatusResourceNotFound(domain.DeviceKind, "missing"))
	handler := &TransportHandler{labelsyncmapping: service, converter: convertv1beta1.NewConverter()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/devices/missing/labelsyncprovenance", nil)

	handler.GetDeviceLabelSyncProvenance(response, request, "missing")

	assert.Equal(t, http.StatusNotFound, response.Code)
}
