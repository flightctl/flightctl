package service

import (
	"encoding/json"
	"fmt"

	b64 "encoding/base64"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/google/uuid"
)

var (
	NullOrgId                = uuid.MustParse("00000000-0000-0000-0000-000000000000")
	MaxRecordsPerListRequest = 1000
	CurrentContinueVersion   = 1
)

type ListParams struct {
	Labels   map[string]string
	Limit    int
	Continue *Continue
}

type Continue struct {
	Version int
	Name    string
	Count   int64
}

func ParseContinueString(contStr *string) (*Continue, error) {
	var cont Continue

	if contStr == nil {
		return nil, nil
	}

	sDec, err := b64.StdEncoding.DecodeString(*contStr)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(sDec, &cont); err != nil {
		return nil, err
	}
	if cont.Version != CurrentContinueVersion {
		return nil, fmt.Errorf("continue string version %d must be %d", cont.Version, CurrentContinueVersion)
	}

	return &cont, nil
}

type DataStoreInterface interface {
	GetDeviceStore() DeviceStoreInterface
	GetEnrollmentRequestStore() EnrollmentRequestStoreInterface
	GetFleetStore() FleetStoreInterface
	GetRepositoryStore() RepositoryStoreInterface
}

type ServiceHandler struct {
	deviceStore            DeviceStoreInterface
	enrollmentRequestStore EnrollmentRequestStoreInterface
	fleetStore             FleetStoreInterface
	repositoryStore        RepositoryStoreInterface
	ca                     *crypto.CA
}

// Make sure we conform to StrictServerInterface
var _ server.StrictServerInterface = (*ServiceHandler)(nil)

func NewServiceHandler(store DataStoreInterface, ca *crypto.CA) *ServiceHandler {
	return &ServiceHandler{
		deviceStore:            store.GetDeviceStore(),
		enrollmentRequestStore: store.GetEnrollmentRequestStore(),
		fleetStore:             store.GetFleetStore(),
		repositoryStore:        store.GetRepositoryStore(),
		ca:                     ca,
	}
}
