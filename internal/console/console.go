package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

const ChannelSize = 2048

type ConsoleSession struct {
	UUID       string
	OrgId      uuid.UUID
	DeviceName string
	SendCh     chan []byte
	RecvCh     chan []byte
	ProtocolCh chan string
}

type InternalSessionRegistration interface {
	// Register a session with a given UUID and channels
	// Those channels will be used to read from and write to the session
	// in a way that this interface down to the gRPC session is abstracted
	StartSession(*ConsoleSession) error
	CloseSession(*ConsoleSession) error
}

type ConsoleSessionManager struct {
	serviceHandler *service.ServiceHandler
	log            logrus.FieldLogger
	// This one is the gRPC Handler of the agent for now, in the next iteration
	// this should be split so we funnel traffic through a queue in redis/valkey
	sessionRegistration InternalSessionRegistration
}

func NewConsoleSessionManager(serviceHandler *service.ServiceHandler, log logrus.FieldLogger, sessionRegistration InternalSessionRegistration) *ConsoleSessionManager {
	return &ConsoleSessionManager{
		serviceHandler:      serviceHandler,
		log:                 log,
		sessionRegistration: sessionRegistration,
	}
}

func (m *ConsoleSessionManager) modifyAnnotations(ctx context.Context, deviceName string, updater func(value string) (string, error)) error {
	var (
		err      error
		newValue string
	)
	for i := 0; i != 10; i++ {
		device, status := m.serviceHandler.GetDevice(ctx, deviceName)
		if status.Code != http.StatusOK {
			return service.ApiStatusToErr(status)
		}
		device.Metadata.Annotations = lo.ToPtr(util.EnsureMap(lo.FromPtr(device.Metadata.Annotations)))
		value, _ := util.GetFromMap(lo.FromPtr(device.Metadata.Annotations), api.DeviceAnnotationConsole)
		newValue, err = updater(value)
		if err != nil {
			return err
		}
		(*device.Metadata.Annotations)[api.DeviceAnnotationConsole] = newValue
		nextRenderedVersion, err := api.GetNextDeviceRenderedVersion(*device.Metadata.Annotations)
		if err != nil {
			return err
		}
		(*device.Metadata.Annotations)[api.DeviceAnnotationRenderedVersion] = nextRenderedVersion
		m.log.Infof("About to save annotations %+v", *device.Metadata.Annotations)
		_, err = m.serviceHandler.UpdateDevice(context.WithValue(ctx, consts.InternalRequestCtxKey, true), deviceName, *device, nil)
		if !errors.Is(err, flterrors.ErrResourceVersionConflict) {
			break
		}
	}
	return err
}

func addSession(sessionID string, sessionMetadata string) func(string) (string, error) {
	return func(existing string) (string, error) {
		var consoles []api.DeviceConsole
		if existing != "" {
			err := json.Unmarshal([]byte(existing), &consoles)
			if err != nil {
				return "", err
			}
		}
		consoles = append(consoles, api.DeviceConsole{
			SessionID:       sessionID,
			SessionMetadata: sessionMetadata,
		})
		b, err := json.Marshal(&consoles)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

func removeSession(sessionID string) func(string) (string, error) {
	return func(existing string) (string, error) {
		if existing == "" {
			return "", nil
		}
		var consoles []api.DeviceConsole
		err := json.Unmarshal([]byte(existing), &consoles)
		if err != nil {
			return "", err
		}
		_, i, found := lo.FindIndexOf(consoles, func(c api.DeviceConsole) bool { return c.SessionID == sessionID })
		if found && i >= 0 {
			consoles = append(consoles[:i], consoles[i+1:]...)
		}
		b, err := json.Marshal(&consoles)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

func (m *ConsoleSessionManager) StartSession(ctx context.Context, orgId uuid.UUID, deviceName, sessionMetadata string) (*ConsoleSession, error) {
	if sessionMetadata == "" {
		m.log.Error("incompatible client: missing session metadata")
		return nil, errors.New("incompatible client: missing session metadata")
	}
	m.log.Infof("Start session. Metadata %s", sessionMetadata)
	session := &ConsoleSession{
		OrgId:      orgId,
		DeviceName: deviceName,
		UUID:       uuid.New().String(),
		SendCh:     make(chan []byte, ChannelSize),
		RecvCh:     make(chan []byte, ChannelSize),
		ProtocolCh: make(chan string),
	}
	// we should move this to a separate table in the database
	if _, status := m.serviceHandler.GetDevice(ctx, deviceName); status.Code != http.StatusOK {
		return nil, service.ApiStatusToErr(status)
	}

	if err := m.modifyAnnotations(ctx, deviceName, addSession(session.UUID, sessionMetadata)); err != nil {
		return nil, err
	}
	// tell the gRPC service, or the message queue (in the future) that there is a session waiting, and provide
	// the channels to read and write to the websocket session
	if err := m.sessionRegistration.StartSession(session); err != nil {
		m.log.Errorf("Failed to start session %s for device %s: %v, rolling back device annotation", session.UUID, deviceName, err)
		// if we fail to register the session we should remove the annotation (best effort)
		if annErr := m.modifyAnnotations(ctx, deviceName, removeSession(session.UUID)); annErr != nil {
			m.log.Errorf("Failed to remove annotation from device %s: %v", deviceName, annErr)
		}
		return nil, err
	}
	return session, nil
}

func (m *ConsoleSessionManager) CloseSession(ctx context.Context, session *ConsoleSession) error {
	closeSessionErr := m.sessionRegistration.CloseSession(session)
	// make sure the device exists

	if err := m.modifyAnnotations(ctx, session.DeviceName, removeSession(session.UUID)); err != nil {
		return fmt.Errorf("failed to remove annotation from device %s: %w", session.DeviceName, err)
	}

	// we still want to signal if there was an error closing the session
	return closeSessionErr
}
