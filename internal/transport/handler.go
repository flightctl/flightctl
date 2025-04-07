package transport

import (
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

type TransportHandler struct {
	serviceHandler        *service.ServiceHandler
	consoleSessionManager *console.ConsoleSessionManager
	log                   logrus.FieldLogger
}

type WebsocketHandler struct {
	ca                    *crypto.CAClient
	log                   logrus.FieldLogger
	consoleSessionManager *console.ConsoleSessionManager
}

// Make sure we conform to servers Transport interface
var _ server.Transport = (*TransportHandler)(nil)

func NewTransportHandler(serviceHandler *service.ServiceHandler, consoleSessionManager *console.ConsoleSessionManager, log logrus.FieldLogger) *TransportHandler {
	return &TransportHandler{
		serviceHandler:        serviceHandler,
		consoleSessionManager: consoleSessionManager,
		log:                   log,
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
