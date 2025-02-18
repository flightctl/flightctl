package service

import (
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/go-chi/chi/v5"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store           store.Store
	ca              *crypto.CA
	log             logrus.FieldLogger
	callbackManager tasks_client.CallbackManager
	kvStore         kvstore.KVStore
	agentEndpoint   string
	uiUrl           string
}

type WebsocketHandler struct {
	store                 store.Store
	ca                    *crypto.CA
	log                   logrus.FieldLogger
	consoleSessionManager *console.ConsoleSessionManager
}

// Make sure we conform to servers Service interface
var _ server.Service = (*ServiceHandler)(nil)

func NewServiceHandler(store store.Store, callbackManager tasks_client.CallbackManager, kvStore kvstore.KVStore, ca *crypto.CA, log logrus.FieldLogger, agentEndpoint string, uiUrl string) *ServiceHandler {
	return &ServiceHandler{
		store:           store,
		ca:              ca,
		log:             log,
		callbackManager: callbackManager,
		kvStore:         kvStore,
		agentEndpoint:   agentEndpoint,
		uiUrl:           uiUrl,
	}
}

func NewWebsocketHandler(store store.Store, ca *crypto.CA, log logrus.FieldLogger, consoleSessionManager *console.ConsoleSessionManager) *WebsocketHandler {
	return &WebsocketHandler{
		store:                 store,
		ca:                    ca,
		log:                   log,
		consoleSessionManager: consoleSessionManager,
	}
}

func (h *WebsocketHandler) RegisterRoutes(r chi.Router) {
	// Websocket handler for console
	r.Get("/ws/v1/devices/{name}/console", h.HandleDeviceConsole)
}
