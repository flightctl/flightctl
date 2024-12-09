package service

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/google/uuid"
)

func (h *ServiceHandler) RequestConsole(ctx context.Context, request server.RequestConsoleRequestObject) (server.RequestConsoleResponseObject, error) {
	orgId := store.NullOrgId

	// make sure the device exists
	_, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceNotFound:
			return server.RequestConsole404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	sessionId := uuid.New().String()

	annotations := map[string]string{api.DeviceAnnotationConsole: sessionId}

	if err := h.store.Device().UpdateAnnotations(ctx, orgId, request.Name, annotations, []string{}); err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.RequestConsole503JSONResponse{Message: "Unable to annotate device for console setup"}, err
	}

	// create a new console session
	return server.RequestConsole200JSONResponse{
		SessionID:    sessionId,
		GRPCEndpoint: h.consoleGrpcEndpoint,
	}, nil

}
