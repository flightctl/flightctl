package service

import (
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store               store.Store
	ca                  *crypto.CA
	log                 logrus.FieldLogger
	callbackManager     tasks.CallbackManager
	consoleGrpcEndpoint string
}

// Make sure we conform to servers Service interface
var _ server.Service = (*ServiceHandler)(nil)

func NewServiceHandler(store store.Store, callbackManager tasks.CallbackManager, ca *crypto.CA, log logrus.FieldLogger, consoleGrpcEndpoint string) *ServiceHandler {
	return &ServiceHandler{
		store:               store,
		ca:                  ca,
		log:                 log,
		callbackManager:     callbackManager,
		consoleGrpcEndpoint: consoleGrpcEndpoint,
	}
}
