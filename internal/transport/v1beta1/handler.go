package transportv1beta1

import (
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/service/authprovider"
	"github.com/flightctl/flightctl/internal/service/certificatesigningrequest"
	"github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/enrollmentrequest"
	"github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/service/organization"
	"github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/service/resourcesync"
	"github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

// TransportHandler holds one focused service interface field per resource type this transport
// version serves - see each per-resource file (device.go, fleet.go, etc.) for which field it
// uses. Fields not tied to a resource (authTokenProxy, authUserInfoProxy, authZ, authN) back
// the auth-adjacent, non-resource endpoints in auth_token.go/auth_userinfo.go/checkpermission.go.
type TransportHandler struct {
	authprovider              authprovider.Service
	certificatesigningrequest certificatesigningrequest.Service
	device                    device.Service
	enrollmentrequest         enrollmentrequest.Service
	event                     event.Service
	fleet                     fleet.Service
	organization              organization.Service
	repository                repository.Service
	resourcesync              resourcesync.Service
	templateversion           templateversion.Service
	converter                 convertv1beta1.Converter
	authN                     common.AuthNMiddleware
	authTokenProxy            *service.AuthTokenProxy
	authUserInfoProxy         *service.AuthUserInfoProxy
	authZ                     auth.AuthZMiddleware
}

type WebsocketHandler struct {
	ca                    *crypto.CAClient
	log                   logrus.FieldLogger
	consoleSessionManager *console.ConsoleSessionManager
}

// Make sure we conform to servers Transport interface
var _ server.Transport = (*TransportHandler)(nil)

func NewTransportHandler(
	authproviderSvc authprovider.Service,
	certificatesigningrequestSvc certificatesigningrequest.Service,
	deviceSvc device.Service,
	enrollmentrequestSvc enrollmentrequest.Service,
	eventSvc event.Service,
	fleetSvc fleet.Service,
	organizationSvc organization.Service,
	repositorySvc repository.Service,
	resourcesyncSvc resourcesync.Service,
	templateversionSvc templateversion.Service,
	converter convertv1beta1.Converter,
	authN common.AuthNMiddleware,
	authTokenProxy *service.AuthTokenProxy,
	authUserInfoProxy *service.AuthUserInfoProxy,
	authZ auth.AuthZMiddleware,
) *TransportHandler {
	return &TransportHandler{
		authprovider:              authproviderSvc,
		certificatesigningrequest: certificatesigningrequestSvc,
		device:                    deviceSvc,
		enrollmentrequest:         enrollmentrequestSvc,
		event:                     eventSvc,
		fleet:                     fleetSvc,
		organization:              organizationSvc,
		repository:                repositorySvc,
		resourcesync:              resourcesyncSvc,
		templateversion:           templateversionSvc,
		converter:                 converter,
		authN:                     authN,
		authTokenProxy:            authTokenProxy,
		authUserInfoProxy:         authUserInfoProxy,
		authZ:                     authZ,
	}
}

func NewWebsocketHandler(ca *crypto.CAClient, log logrus.FieldLogger, consoleSessionManager *console.ConsoleSessionManager) *WebsocketHandler {
	return &WebsocketHandler{
		ca:                    ca,
		log:                   log,
		consoleSessionManager: consoleSessionManager,
	}
}

func (h *WebsocketHandler) RegisterRoutes(r chi.Router) {
	// Websocket handler for console
	r.Get("/ws/v1/devices/{name}/console", h.HandleDeviceConsole)
}
