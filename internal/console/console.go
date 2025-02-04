package console

import (
	"context"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks_client"
	"github.com/google/uuid"
	"github.com/sirupsen/logrus"
)

const ChannelSize = 2048

type ConsoleSession struct {
	UUID       string
	OrgId      uuid.UUID
	DeviceName string
	SendCh     chan []byte
	RecvCh     chan []byte
}

type InternalSessionRegistration interface {
	// Register a session with a given UUID and channels
	// Those channels will be used to read from and write to the session
	// in a way that this interface down to the gRPC session is abstracted
	StartSession(*ConsoleSession) error
	CloseSession(*ConsoleSession) error
}

type ConsoleSessionManager struct {
	store           store.Store
	log             logrus.FieldLogger
	callbackManager tasks_client.CallbackManager
	kvStore         kvstore.KVStore
	// This one is the gRPC Handler of the agent for now, in the next iteration
	// this should be split so we funnel traffic through a queue in redis/valkey
	sessionRegistration InternalSessionRegistration
}

func NewConsoleSessionManager(store store.Store, callbackManager tasks_client.CallbackManager, kvStore kvstore.KVStore, log logrus.FieldLogger, sessionRegistration InternalSessionRegistration) *ConsoleSessionManager {
	return &ConsoleSessionManager{
		store:               store,
		log:                 log,
		callbackManager:     callbackManager,
		kvStore:             kvStore,
		sessionRegistration: sessionRegistration,
	}
}

func (m *ConsoleSessionManager) StartSession(ctx context.Context, orgId uuid.UUID, deviceName string) (*ConsoleSession, error) {

	session := &ConsoleSession{
		OrgId:      orgId,
		DeviceName: deviceName,
		UUID:       uuid.New().String(),
		SendCh:     make(chan []byte, ChannelSize),
		RecvCh:     make(chan []byte, ChannelSize),
	}
	// TODO(majopela): This still signals console session creation through an annotation on the device
	// we should move this to a separate table in the database
	if _, err := m.store.Device().Get(ctx, orgId, deviceName); err != nil {
		return nil, err
	}

	annotations := map[string]string{api.DeviceAnnotationConsole: session.UUID}
	if err := m.store.Device().UpdateAnnotations(ctx, orgId, deviceName, annotations, []string{}); err != nil {
		return nil, err
	}

	// tell the gRPC service, or the message queue (in the future) that there is a session waiting, and provide
	// the channels to read and write to the websocket session
	if err := m.sessionRegistration.StartSession(session); err != nil {
		m.log.Errorf("Failed to start session %s for device %s: %v, rolling back device annotation", session.UUID, deviceName, err)
		// if we fail to register the session we should remove the annotation (best effort)
		deleteAnnotations := []string{api.DeviceAnnotationConsole}
		err = m.store.Device().UpdateAnnotations(ctx, orgId, deviceName, map[string]string{}, deleteAnnotations)
		if err != nil {
			m.log.Errorf("Failed to remove annotation from device %s: %v", deviceName, err)
		}
		return nil, err
	}
	return session, nil
}

func (m *ConsoleSessionManager) CloseSession(ctx context.Context, session *ConsoleSession) error {
	closeSessionErr := m.sessionRegistration.CloseSession(session)
	// make sure the device exists
	device, err := m.store.Device().Get(ctx, session.OrgId, session.DeviceName)
	if err != nil {
		return err
	}

	// if the device is still attached to the same session, remove the annotation
	if device.Metadata.Annotations != nil {
		annotation, ok := (*device.Metadata.Annotations)[api.DeviceAnnotationConsole]
		if ok && annotation == session.UUID {
			deleteAnnotations := []string{api.DeviceAnnotationConsole}

			if err := m.store.Device().UpdateAnnotations(ctx, session.OrgId, session.DeviceName, map[string]string{}, deleteAnnotations); err != nil {
				return err
			}
		}
	}
	// we still want to signal if there was an error closing the session
	return closeSessionErr
}
