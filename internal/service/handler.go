package service

import (
	"github.com/flightctl/flightctl/pkg/server"
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
}

// Make sure we conform to StrictServerInterface
var _ server.StrictServerInterface = (*ServiceHandler)(nil)

func NewServiceHandler(store DataStoreInterface) *ServiceHandler {
	return &ServiceHandler{
		deviceStore:            store.GetDeviceStore(),
		enrollmentRequestStore: store.GetEnrollmentRequestStore(),
		fleetStore:             store.GetFleetStore(),
	}
}
