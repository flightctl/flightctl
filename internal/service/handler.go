package service

import (
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	*EventHandler
	store           store.Store
	ca              *crypto.CAClient
	log             logrus.FieldLogger
	callbackManager tasks_client.CallbackManager
	kvStore         kvstore.KVStore
	agentEndpoint   string
	uiUrl           string
	tpmCAPaths      []string
}

func NewServiceHandler(store store.Store, callbackManager tasks_client.CallbackManager, kvStore kvstore.KVStore, ca *crypto.CAClient, log logrus.FieldLogger, agentEndpoint string, uiUrl string, tpmCAPaths []string) *ServiceHandler {
	return &ServiceHandler{
		EventHandler:    NewEventHandler(store, log),
		store:           store,
		ca:              ca,
		log:             log,
		callbackManager: callbackManager,
		kvStore:         kvStore,
		agentEndpoint:   agentEndpoint,
		uiUrl:           uiUrl,
		tpmCAPaths:      tpmCAPaths,
	}
}
