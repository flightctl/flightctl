package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/pkg/version"
)

// (GET /api/version)
func (h *ServiceHandler) GetVersion(ctx context.Context, request server.GetVersionRequestObject) (server.GetVersionResponseObject, error) {
	versionInfo := version.Get()
	return server.GetVersion200JSONResponse{
		Version: versionInfo.GitVersion,
	}, nil
}
