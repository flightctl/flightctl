package service

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/internal/api/server"
	agentServer "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/store"
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
	if cn != crypto.ClientBootstrapCommonName {
		log.Warningf("an attempt to perform enrollment with a certificate with CN %q has been detected", cn)
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
func (s *AgentServiceHandler) GetRenderedDeviceSpec(ctx context.Context, request agentServer.GetRenderedDeviceSpecRequestObject) (agentServer.GetRenderedDeviceSpecResponseObject, error) {

	if err := ValidateDeviceAccessFromContext(ctx, request.Name, s.log); err != nil {
		return agentServer.GetRenderedDeviceSpec401JSONResponse{
			Message: err.Error(),
		}, err
	}

	serverRequest := server.GetRenderedDeviceSpecRequestObject{
		Name:   request.Name,
		Params: request.Params,
	}
	return common.GetRenderedDeviceSpec(ctx, s.store, serverRequest, s.agentGrpcEndpoint)
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
	return common.ReplaceDeviceStatus(ctx, s.store, serverRequest)
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
