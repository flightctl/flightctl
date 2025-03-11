package service

import (
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
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
