package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	agentServer "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/sosreport"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type AgentServiceHandler struct {
	store             store.Store
	ca                *crypto.CA
	log               logrus.FieldLogger
	agentGrpcEndpoint string
}

// Make sure we conform to servers Service interface
var _ agentServer.Service = (*AgentServiceHandler)(nil)

func ValidateDeviceAccessFromContext(ctx context.Context, name string, log logrus.FieldLogger) error {
	cn, ok := ctx.Value(middleware.TLSCommonNameContextKey).(string)
	if !ok {
		log.Warningf("an attempt to access device %q without a CN in tls certificate has been detected", name)
		return errors.New("no common name in certificate")
	}
	if expectedCn, err := crypto.CNFromDeviceFingerprint(name); err != nil {
		return err
	} else if cn != expectedCn {
		log.Warningf("an attempt to access device %q with a certificate with CN %q has been detected", name, cn)
		return errors.New("invalid tls CN for device")
	}
	// all good, you shall pass
	return nil
}

func ValidateEnrollmentAccessFromContext(ctx context.Context, log logrus.FieldLogger) error {
	cn, ok := ctx.Value(middleware.TLSCommonNameContextKey).(string)
	if !ok {
		return errors.New("no common name in certificate")
	}
	if cn != crypto.ClientBootstrapCommonName &&
		!strings.HasPrefix(cn, crypto.ClientBootstrapCommonNamePrefix) {
		log.Errorf("an attempt to perform enrollment with a certificate with CN %q has been detected", cn)
		return errors.New("invalid tls CN for enrollment request")
	}
	// all good, you shall pass
	return nil
}

func NewAgentServiceHandler(store store.Store, ca *crypto.CA, log logrus.FieldLogger, agentGrpcEndpoint string) *AgentServiceHandler {
	return &AgentServiceHandler{
		store:             store,
		ca:                ca,
		log:               log,
		agentGrpcEndpoint: agentGrpcEndpoint,
	}
}

// (GET /api/v1/devices/{name}/rendered)
func (s *AgentServiceHandler) GetRenderedDevice(ctx context.Context, request agentServer.GetRenderedDeviceRequestObject) (agentServer.GetRenderedDeviceResponseObject, error) {

	if err := ValidateDeviceAccessFromContext(ctx, request.Name, s.log); err != nil {
		return agentServer.GetRenderedDevice401JSONResponse{
			Message: err.Error(),
		}, err
	}

	serverRequest := server.GetRenderedDeviceRequestObject{
		Name:   request.Name,
		Params: request.Params,
	}

	return common.GetRenderedDevice(ctx, s.store, s.log, serverRequest, s.agentGrpcEndpoint)
}

// (PUT /api/v1/devices/{name}/status)
func (s *AgentServiceHandler) ReplaceDeviceStatus(ctx context.Context, request agentServer.ReplaceDeviceStatusRequestObject) (agentServer.ReplaceDeviceStatusResponseObject, error) {

	if err := ValidateDeviceAccessFromContext(ctx, request.Name, s.log); err != nil {
		return agentServer.ReplaceDeviceStatus401JSONResponse{
			Message: err.Error(),
		}, err
	}

	serverRequest := server.ReplaceDeviceStatusRequestObject{
		Name: request.Name,
		Body: request.Body,
	}
	return common.ReplaceDeviceStatus(ctx, s.store, s.log, serverRequest)
}

// (POST /api/v1/enrollmentrequests)
func (s *AgentServiceHandler) CreateEnrollmentRequest(ctx context.Context, request agentServer.CreateEnrollmentRequestRequestObject) (agentServer.CreateEnrollmentRequestResponseObject, error) {

	if err := ValidateEnrollmentAccessFromContext(ctx, s.log); err != nil {
		return agentServer.CreateEnrollmentRequest401JSONResponse{
			Message: err.Error(),
		}, err
	}

	serverRequest := server.CreateEnrollmentRequestRequestObject{
		Body: request.Body,
	}
	return common.CreateEnrollmentRequest(ctx, s.store, serverRequest)
}

// (GET /api/v1/enrollmentrequests/{name})
func (s *AgentServiceHandler) ReadEnrollmentRequest(ctx context.Context, request agentServer.ReadEnrollmentRequestRequestObject) (agentServer.ReadEnrollmentRequestResponseObject, error) {

	if err := ValidateEnrollmentAccessFromContext(ctx, s.log); err != nil {
		return agentServer.ReadEnrollmentRequest401JSONResponse{
			Message: err.Error(),
		}, err
	}
	serverRequest := server.ReadEnrollmentRequestRequestObject{
		Name: request.Name,
	}
	return common.ReadEnrollmentRequest(ctx, s.store, serverRequest)
}

// (POST /api/v1/sosreports/{sosSessionID})
func (s *AgentServiceHandler) UploadSosReport(ctx context.Context, request agentServer.UploadSosReportRequestObject) (agentServer.UploadSosReportResponseObject, error) {
	data, exists := sosreport.Sessions.Get(request.SosSessionID)
	if !exists {
		return agentServer.UploadSosReport404JSONResponse(api.StatusResourceNotFound("session-id", request.SosSessionID.String())), nil
	}
	data.RcvChan <- request.Body
	timer := time.NewTimer(time.Hour)
	select {
	case err := <-data.ErrChan:
		if err != nil {
			return nil, err
		}
	case <-timer.C:
		return agentServer.UploadSosReport504JSONResponse(api.StatusGatewayTimeoutError("timeout waiting for the file to be sent to the client")), nil
	}
	return agentServer.UploadSosReport204Response{}, nil
}

// (GET /api/v1/devices/{name}/commands)
func (s *AgentServiceHandler) GetNextCommands(ctx context.Context, request agentServer.GetNextCommandsRequestObject) (agentServer.GetNextCommandsResponseObject, error) {
	if err := ValidateDeviceAccessFromContext(ctx, request.Name, s.log); err != nil {
		return agentServer.GetNextCommands401JSONResponse{
			Message: err.Error(),
		}, err
	}
	orgId := store.NullOrgId
	device, err := s.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrResourceNameIsNil):
			return agentServer.GetNextCommands400JSONResponse(api.StatusBadRequest(err.Error())), nil
		case errors.Is(err, flterrors.ErrResourceNotFound):
			return agentServer.GetNextCommands404JSONResponse(api.StatusResourceNotFound("Device", request.Name)), nil
		default:
			return nil, err
		}
	}
	var commands []agentServer.DeviceCommand
	annotation, exists := util.GetFromMap(lo.FromPtr(device.Metadata.Annotations), api.DeviceAnnotationSosReports)
	if exists {
		var ids []string
		if err := json.Unmarshal([]byte(annotation), &ids); err != nil {
			return agentServer.GetNextCommands500JSONResponse(api.StatusInternalServerError(fmt.Sprintf("unmarshal ids: %v", err))), nil
		}
		for _, idStr := range ids {
			id, err := uuid.Parse(idStr)
			if err != nil {
				return agentServer.GetNextCommands500JSONResponse(api.StatusInternalServerError(fmt.Sprintf("parse id %s: %v", idStr, err))), nil
			}
			var cmd agentServer.DeviceCommand
			err = cmd.FromUploadSosReportCommand(agentServer.UploadSosReportCommand{
				Id: id,
			})
			if err != nil {
				return agentServer.GetNextCommands500JSONResponse(api.StatusInternalServerError(fmt.Sprintf("FromUploadSosReportCommand: %v", err))), nil
			}
			commands = append(commands, cmd)
		}
	}
	return agentServer.GetNextCommands200JSONResponse{
		Commands: &commands,
	}, nil
}
