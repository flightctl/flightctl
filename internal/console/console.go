package console

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
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
	deviceSvc deviceservice.Service
	log       logrus.FieldLogger
	notifier  ConsoleEventNotifier
	// This one is the gRPC Handler of the agent for now, in the next iteration
	// this should be split so we funnel traffic through a queue in redis/valkey
	sessionRegistration InternalSessionRegistration
}

func NewConsoleSessionManager(deviceSvc deviceservice.Service, log logrus.FieldLogger, sessionRegistration InternalSessionRegistration, notifier ConsoleEventNotifier) *ConsoleSessionManager {
	return &ConsoleSessionManager{
		deviceSvc:           deviceSvc,
		log:                 log,
		notifier:            notifier,
		sessionRegistration: sessionRegistration,
	}
}

func (m *ConsoleSessionManager) modifyAnnotations(ctx context.Context, orgId uuid.UUID, deviceName string, updater func(value string) (string, error)) domain.Status {
	var (
		err      error
		newValue string
	)
	for i := 0; i != 10; i++ {
		device, status := m.deviceSvc.GetDevice(ctx, orgId, deviceName)
		if status.Code != http.StatusOK {
			return status
		}
		device.Metadata.Annotations = lo.ToPtr(util.EnsureMap(lo.FromPtr(device.Metadata.Annotations)))

		// Check if device is in waiting, paused, or decommissioned state - prevent console updates
		annotations := lo.FromPtr(device.Metadata.Annotations)
		if waitingValue, exists := annotations[domain.DeviceAnnotationAwaitingReconnect]; exists && waitingValue == "true" {
			return domain.StatusConflict("Device is awaiting reconnection after restore")
		}
		if pausedValue, exists := annotations[domain.DeviceAnnotationConflictPaused]; exists && pausedValue == "true" {
			return domain.StatusConflict("Device is paused due to conflicts")
		}
		if device.Spec != nil && device.Spec.Decommissioning != nil {
			return domain.StatusConflict("Device is decommissioned")
		}

		value, _ := util.GetFromMap(annotations, domain.DeviceAnnotationConsole)
		newValue, err = updater(value)
		if err != nil {
			return domain.StatusInternalServerError(err.Error())
		}
		(*device.Metadata.Annotations)[domain.DeviceAnnotationConsole] = newValue
		m.log.Infof("About to save annotations %+v", *device.Metadata.Annotations)
		_, err = m.deviceSvc.UpdateDevice(ctx, orgId, deviceName, *device, nil)
		if !errors.Is(err, flterrors.ErrResourceVersionConflict) {
			break
		}
	}
	if err != nil {
		return domain.StatusInternalServerError(err.Error())
	}
	return domain.StatusOK()
}

func addSession(sessionID string, sessionMetadata string) func(string) (string, error) {
	return func(existing string) (string, error) {
		var consoles []domain.DeviceConsole
		if existing != "" {
			err := json.Unmarshal([]byte(existing), &consoles)
			if err != nil {
				return "", err
			}
		}
		consoles = append(consoles, domain.DeviceConsole{
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
		var consoles []domain.DeviceConsole
		err := json.Unmarshal([]byte(existing), &consoles)
		if err != nil {
			return "", err
		}
		_, i, found := lo.FindIndexOf(consoles, func(c domain.DeviceConsole) bool { return c.SessionID == sessionID })
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

func (m *ConsoleSessionManager) StartSession(ctx context.Context, orgId uuid.UUID, deviceName, sessionMetadata string) (*ConsoleSession, domain.Status) {
	// Guard: missing session metadata
	if sessionMetadata == "" {
		m.log.Error("incompatible client: missing session metadata")
		return nil, domain.StatusBadRequest("incompatible client: missing session metadata")
	}

	m.log.Infof("Start session. Metadata %s", sessionMetadata)

	// Guard: device doesn't exist - check once at the beginning
	device, status := m.deviceSvc.GetDevice(ctx, orgId, deviceName)
	if status.Code != http.StatusOK {
		return nil, status
	}

	// Guard: device is in an invalid state for console access
	if device.Spec != nil && device.Spec.Decommissioning != nil {
		return nil, domain.StatusConflict("Device is decommissioned")
	}
	annotations := util.EnsureMap(lo.FromPtr(device.Metadata.Annotations))
	if waitingValue, exists := annotations[domain.DeviceAnnotationAwaitingReconnect]; exists && waitingValue == "true" {
		return nil, domain.StatusConflict("Device is awaiting reconnection after restore")
	}
	if pausedValue, exists := annotations[domain.DeviceAnnotationConflictPaused]; exists && pausedValue == "true" {
		return nil, domain.StatusConflict("Device is paused due to conflicts")
	}

	session := &ConsoleSession{
		OrgId:      orgId,
		DeviceName: deviceName,
		UUID:       uuid.New().String(),
		SendCh:     make(chan []byte, ChannelSize),
		RecvCh:     make(chan []byte, ChannelSize),
		ProtocolCh: make(chan string),
	}

	// Now that we know the device exists and is accessible, modify annotations
	if status := m.modifyAnnotations(ctx, orgId, deviceName, addSession(session.UUID, sessionMetadata)); status.Code != http.StatusOK {
		// If modifyAnnotations fails, check if the device still exists to return the correct error
		if _, deviceStatus := m.deviceSvc.GetDevice(ctx, orgId, deviceName); deviceStatus.Code != http.StatusOK {
			return nil, deviceStatus
		}
		return nil, status
	}

	// Register the session with the gRPC service
	if err := m.sessionRegistration.StartSession(session); err != nil {
		m.log.Errorf("Failed to start session %s for device %s: %v, rolling back device annotation", session.UUID, deviceName, err)
		// Best effort cleanup of annotations
		if annStatus := m.modifyAnnotations(ctx, orgId, deviceName, removeSession(session.UUID)); annStatus.Code != http.StatusOK {
			m.log.Errorf("Failed to remove annotation from device %s: %v", deviceName, annStatus)
		}
		return nil, domain.StatusInternalServerError(err.Error())
	}

	if err := m.notifier.NotifyConsole(ctx, orgId, deviceName); err != nil {
		m.log.Warnf("StartSession: failed to notify device %s: %v", deviceName, err)
	}

	return session, domain.StatusOK()
}

func (m *ConsoleSessionManager) CloseSession(ctx context.Context, session *ConsoleSession) domain.Status {
	closeSessionErr := m.sessionRegistration.CloseSession(session)
	// make sure the device exists

	if status := m.modifyAnnotations(ctx, session.OrgId, session.DeviceName, removeSession(session.UUID)); status.Code != http.StatusOK {
		return status
	}

	// we still want to signal if there was an error closing the session
	if closeSessionErr != nil {
		return domain.StatusInternalServerError(closeSessionErr.Error())
	}
	return domain.StatusOK()
}
