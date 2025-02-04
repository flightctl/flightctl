package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/pkg/version"
)

// (GET /api/version)
func (h *ServiceHandler) GetVersion(ctx context.Context, request server.GetVersionRequestObject) (server.GetVersionResponseObject, error) {
	allowed, err := auth.GetAuthZ().CheckPermission(ctx, "version", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.GetVersion503JSONResponse{Message: AuthorizationServerUnavailable}, nil
	}
	if !allowed {
		return server.GetVersion403JSONResponse{Message: Forbidden}, nil
	}

	versionInfo := version.Get()
	return server.GetVersion200JSONResponse{
		Version: versionInfo.GitVersion,
	}, nil
}
