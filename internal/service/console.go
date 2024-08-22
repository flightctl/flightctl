package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
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

	annotations := map[string]string{model.DeviceAnnotationConsole: sessionId}

	if err := h.store.Device().UpdateAnnotations(ctx, orgId, request.Name, annotations, []string{}); err != nil {
		return server.RequestConsole401JSONResponse{Message: "Unable to annotate device for console setup"}, err
	}

	// create a new console session
	return server.RequestConsole200JSONResponse{
		SessionID:    sessionId,
		GRPCEndpoint: h.consoleGrpcEndpoint,
	}, nil

}
