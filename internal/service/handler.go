package service

import (
	b64 "encoding/base64"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/crypto"
	"github.com/flightctl/flightctl/internal/server"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

var (
	NullOrgId                = uuid.MustParse("00000000-0000-0000-0000-000000000000")
	MaxRecordsPerListRequest = 1000
	CurrentContinueVersion   = 1
)

type ListParams struct {
	Labels       map[string]string
	InvertLabels *bool
	Owner        *string
	Limit        int
	Continue     *Continue
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

type DataStore interface {
	GetDeviceStore() DeviceStore
	GetEnrollmentRequestStore() EnrollmentRequestStore
	GetFleetStore() FleetStore
	GetRepositoryStore() RepositoryStore
	GetResourceSyncStore() ResourceSyncStore
}

type ServiceHandler struct {
	deviceStore            DeviceStore
	enrollmentRequestStore EnrollmentRequestStore
	fleetStore             FleetStore
	repositoryStore        RepositoryStore
	resourceSyncStore      ResourceSyncStore
	ca                     *crypto.CA
	log                    logrus.FieldLogger
}

// Make sure we conform to servers Service interface
var _ server.Service = (*ServiceHandler)(nil)

func NewServiceHandler(store DataStore, ca *crypto.CA, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{
		deviceStore:            store.GetDeviceStore(),
		enrollmentRequestStore: store.GetEnrollmentRequestStore(),
		fleetStore:             store.GetFleetStore(),
		repositoryStore:        store.GetRepositoryStore(),
		resourceSyncStore:      store.GetResourceSyncStore(),
		ca:                     ca,
		log:                    log,
	}
}
