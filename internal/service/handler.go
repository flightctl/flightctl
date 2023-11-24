package service

import (
	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/google/uuid"
)

var (
	NullOrgId = uuid.MustParse("00000000-0000-0000-0000-000000000000")
)

type DataStoreInterface interface {
	GetDeviceStore() DeviceStoreInterface
	GetEnrollmentRequestStore() EnrollmentRequestStoreInterface
	GetFleetStore() FleetStoreInterface
}

type ServiceHandler struct {
	deviceStore            DeviceStoreInterface
	enrollmentRequestStore EnrollmentRequestStoreInterface
	fleetStore             FleetStoreInterface
	ca                     *crypto.CA
}

// Make sure we conform to StrictServerInterface
var _ server.StrictServerInterface = (*ServiceHandler)(nil)

func NewServiceHandler(store DataStoreInterface, ca *crypto.CA) *ServiceHandler {
	return &ServiceHandler{
		deviceStore:            store.GetDeviceStore(),
		enrollmentRequestStore: store.GetEnrollmentRequestStore(),
		fleetStore:             store.GetFleetStore(),
		ca:                     ca,
	}
}
