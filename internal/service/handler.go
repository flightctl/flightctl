package service

import (
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/org/resolvers"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	eventHandler  *EventHandler
	store         store.Store
	ca            *crypto.CAClient
	log           logrus.FieldLogger
	workerClient  worker_client.WorkerClient
	kvStore       kvstore.KVStore
	agentEndpoint string
	uiUrl         string
	tpmCAPaths    []string
	orgResolver   resolvers.Resolver
}

func NewServiceHandler(store store.Store, workerClient worker_client.WorkerClient, kvStore kvstore.KVStore, ca *crypto.CAClient, log logrus.FieldLogger, agentEndpoint string, uiUrl string, tpmCAPaths []string, orgResolver resolvers.Resolver) *ServiceHandler {
	return &ServiceHandler{
		eventHandler:  NewEventHandler(store, workerClient, log),
		store:         store,
		ca:            ca,
		log:           log,
		workerClient:  workerClient,
		kvStore:       kvStore,
		agentEndpoint: agentEndpoint,
		uiUrl:         uiUrl,
		tpmCAPaths:    tpmCAPaths,
		orgResolver:   orgResolver,
	}
}
