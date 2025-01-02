package service

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
)

// (GET /api/v1/enrollmentconfig)
func (h *ServiceHandler) GetEnrollmentConfig(ctx context.Context, request server.GetEnrollmentConfigRequestObject) (server.GetEnrollmentConfigResponseObject, error) {
	cert, _, err := h.ca.Config.GetPEMBytes()
	if err != nil {
		return nil, fmt.Errorf("failed to get CA certificate")
	}
	return server.GetEnrollmentConfig200JSONResponse{
		EnrollmentService: v1alpha1.EnrollmentService{
			Service: v1alpha1.EnrollmentServiceService{
				CertificateAuthorityData: base64.StdEncoding.EncodeToString(cert),
				Server:                   h.agentEndpoint,
			},
			EnrollmentUiEndpoint: h.uiUrl,
		},
	}, nil
}
