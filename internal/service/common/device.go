package common

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
)

func ReplaceDeviceStatus(ctx context.Context, st store.Store, request server.ReplaceDeviceStatusRequestObject) (server.ReplaceDeviceStatusResponseObject, error) {
	orgId := store.NullOrgId

	device := request.Body
	device.Status.LastSeen = time.Now()

	result, err := st.Device().UpdateStatus(ctx, orgId, device)
	switch err {
	case nil:
		return server.ReplaceDeviceStatus200JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.ReplaceDeviceStatus400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNameIsNil:
		return server.ReplaceDeviceStatus400JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrResourceNotFound:
		return server.ReplaceDeviceStatus404JSONResponse{}, nil
	default:
		return nil, err
	}
}

func GetRenderedDeviceSpec(ctx context.Context, st store.Store, request server.GetRenderedDeviceSpecRequestObject, consoleGrpcEndpoint string) (server.GetRenderedDeviceSpecResponseObject, error) {
	orgId := store.NullOrgId

	result, err := st.Device().GetRendered(ctx, orgId, request.Name, request.Params.KnownRenderedVersion, consoleGrpcEndpoint)
	switch err {
	case nil:
		if result == nil {
			return server.GetRenderedDeviceSpec204Response{}, nil
		}
		return server.GetRenderedDeviceSpec200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.GetRenderedDeviceSpec404JSONResponse{}, nil
	case flterrors.ErrResourceOwnerIsNil:
		return server.GetRenderedDeviceSpec409JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrTemplateVersionIsNil:
		return server.GetRenderedDeviceSpec409JSONResponse{Message: err.Error()}, nil
	case flterrors.ErrInvalidTemplateVersion:
		return server.GetRenderedDeviceSpec409JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}
