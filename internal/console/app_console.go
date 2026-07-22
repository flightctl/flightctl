package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

// AppConsoleSession is the server-side session object bridging the WebSocket handler
// and the gRPC stream from the agent.
type AppConsoleSession struct {
	UUID       string
	OrgId      uuid.UUID
	DeviceName string
	AppName    string
	SendCh     chan []byte
	RecvCh     chan []byte
	ProtocolCh chan string
	// ErrCh carries a session-level failure reported by the agent (e.g. the requested
	// application does not exist). The WebSocket handler must close the client
	// connection with a distinguishable close code/reason instead of relaying this as
	// console payload data.
	ErrCh chan string
}

// AppConsoleDeviceService is the narrow interface AppConsoleSessionManager needs,
// avoiding a full service.Service dependency in flightctl-remote-access.
type AppConsoleDeviceService interface {
	GetDevice(ctx context.Context, orgId uuid.UUID, name string) (*domain.Device, domain.Status)
	UpdateDevice(ctx context.Context, orgId uuid.UUID, name string, device domain.Device, fieldsToUnset []string) (*domain.Device, error)
}

// AppConsoleSessionRegistration is implemented by the gRPC server that the agent connects to.
type AppConsoleSessionRegistration interface {
	StartSession(session *AppConsoleSession) error
	CloseSession(session *AppConsoleSession) error
}

// ConsoleEventNotifier signals the device agent that a console session needs handling.
// It is deliberately decoupled from rendered-spec versioning.
type ConsoleEventNotifier interface {
	NotifyConsole(ctx context.Context, orgId uuid.UUID, name string) error
	ClearConsoleNotification(ctx context.Context, orgId uuid.UUID, name string) error
}

// AppConsoleSessionManager manages application console sessions using device annotations.
// It mirrors ConsoleSessionManager, keyed on DeviceAnnotationRemoteSession with
// per-appName uniqueness enforcement (stored as a JSON array of DeviceRemoteSession).
type AppConsoleSessionManager struct {
	svc                 AppConsoleDeviceService
	log                 logrus.FieldLogger
	sessionRegistration AppConsoleSessionRegistration
	notifier            ConsoleEventNotifier
}

func NewAppConsoleSessionManager(
	svc AppConsoleDeviceService,
	log logrus.FieldLogger,
	reg AppConsoleSessionRegistration,
	notifier ConsoleEventNotifier,
) *AppConsoleSessionManager {
	return &AppConsoleSessionManager{
		svc:                 svc,
		log:                 log,
		sessionRegistration: reg,
		notifier:            notifier,
	}
}

// modifyAnnotations reads, modifies, and writes back the device annotations in an optimistic-lock
// loop. When enforceSessionStartGuards is true, the function also checks device readiness conditions
// (awaiting reconnect, conflict-paused, decommissioning) and returns 409 if any are set. Pass false
// for cleanup/rollback paths so they are never blocked by device state changes.
func (m *AppConsoleSessionManager) modifyAnnotations(ctx context.Context, orgId uuid.UUID, deviceName string, enforceSessionStartGuards bool, updater func(string) (string, error)) domain.Status {
	var (
		err      error
		newValue string
	)
	for i := 0; i != 10; i++ {
		device, status := m.svc.GetDevice(ctx, orgId, deviceName)
		if status.Code != http.StatusOK {
			return status
		}
		device.Metadata.Annotations = lo.ToPtr(util.EnsureMap(lo.FromPtr(device.Metadata.Annotations)))

		annotations := lo.FromPtr(device.Metadata.Annotations)
		if enforceSessionStartGuards {
			if waitingValue, exists := annotations[domain.DeviceAnnotationAwaitingReconnect]; exists && waitingValue == "true" {
				return domain.StatusConflict("Device is awaiting reconnection after restore")
			}
			if pausedValue, exists := annotations[domain.DeviceAnnotationConflictPaused]; exists && pausedValue == "true" {
				return domain.StatusConflict("Device is paused due to conflicts")
			}
			if device.Spec != nil && device.Spec.Decommissioning != nil {
				return domain.StatusConflict("Device is decommissioned")
			}
		}

		value, _ := util.GetFromMap(annotations, domain.DeviceAnnotationRemoteSession)
		newValue, err = updater(value)
		if err != nil {
			var dupErr *duplicateAppSessionError
			if errors.As(err, &dupErr) {
				return domain.StatusConflict(dupErr.Error())
			}
			return domain.StatusInternalServerError(err.Error())
		}
		if newValue == value {
			return domain.StatusOK()
		}
		if newValue == "" {
			delete(*device.Metadata.Annotations, domain.DeviceAnnotationRemoteSession)
		} else {
			(*device.Metadata.Annotations)[domain.DeviceAnnotationRemoteSession] = newValue
		}
		m.log.Debugf("updating remote-session annotations for device %s", deviceName)
		_, err = m.svc.UpdateDevice(ctx, orgId, deviceName, *device, nil)
		if !errors.Is(err, flterrors.ErrResourceVersionConflict) {
			break
		}
	}
	if err != nil {
		return domain.StatusInternalServerError(err.Error())
	}
	return domain.StatusOK()
}

// addAppSession returns an updater closure that appends a new session entry, returning 409 if
// an entry for appName already exists.
func addAppSession(sessionID, appName, consoleType string) func(string) (string, error) {
	return func(existing string) (string, error) {
		var sessions []domain.DeviceRemoteSession
		if existing != "" {
			if err := json.Unmarshal([]byte(existing), &sessions); err != nil {
				return "", err
			}
		}
		for _, s := range sessions {
			if s.AppName == appName {
				return "", &duplicateAppSessionError{appName: appName}
			}
		}
		sessions = append(sessions, domain.DeviceRemoteSession{
			SessionID:   sessionID,
			AppName:     appName,
			ConsoleType: consoleType,
		})
		b, err := json.Marshal(&sessions)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// replaceAppSession returns an updater closure that atomically removes any existing session
// entry for appName and adds a new one in its place, recording the removed entry's SessionID
// (if any) via ReplacesSessionID. That field is what lets the agent distinguish an explicit
// takeover from an unrelated close-then-reopen of the same app: it only cancels a running
// session when a new entry explicitly names it as replaced, never just because it vanished.
// If no existing entry is found, this behaves exactly like addAppSession.
//
// The updater may run more than once (modifyAnnotations retries on conflict), so
// replacedSessionID is overwritten on every call; only the value from the call that actually
// commits reflects the session that was really evicted. If non-nil, it is set to the removed
// entry's SessionID, or "" if there was none.
func replaceAppSession(sessionID, appName, consoleType string, replacedSessionID *string) func(string) (string, error) {
	return func(existing string) (string, error) {
		var sessions []domain.DeviceRemoteSession
		if existing != "" {
			if err := json.Unmarshal([]byte(existing), &sessions); err != nil {
				return "", err
			}
		}
		var replacesSessionID string
		filtered := sessions[:0]
		for _, s := range sessions {
			if s.AppName == appName {
				replacesSessionID = s.SessionID
				continue
			}
			filtered = append(filtered, s)
		}
		if replacedSessionID != nil {
			*replacedSessionID = replacesSessionID
		}
		filtered = append(filtered, domain.DeviceRemoteSession{
			SessionID:         sessionID,
			AppName:           appName,
			ConsoleType:       consoleType,
			ReplacesSessionID: replacesSessionID,
		})
		b, err := json.Marshal(&filtered)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// removeAppSession returns an updater closure that removes the session entry for the given sessionID.
func removeAppSession(sessionID string) func(string) (string, error) {
	return func(existing string) (string, error) {
		if existing == "" {
			return existing, nil
		}
		var sessions []domain.DeviceRemoteSession
		if err := json.Unmarshal([]byte(existing), &sessions); err != nil {
			return "", err
		}
		filtered := sessions[:0]
		removed := false
		for _, s := range sessions {
			if s.SessionID != sessionID {
				filtered = append(filtered, s)
			} else {
				removed = true
			}
		}
		if !removed {
			return existing, nil
		}
		if len(filtered) == 0 {
			return "", nil
		}
		b, err := json.Marshal(&filtered)
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
}

// duplicateAppSessionError signals a 409 conflict when a session for the same appName already exists.
type duplicateAppSessionError struct {
	appName string
}

func (e *duplicateAppSessionError) Error() string {
	return "console session already active for application " + e.appName
}

// StartSession validates inputs, guards against duplicates via annotation, and registers the session.
// When force is true and a session already exists for appName, that session's annotation entry
// is atomically replaced (see replaceAppSession) instead of returning a 409 conflict; the agent
// is responsible for noticing the takeover and tearing down the replaced session itself.
func (m *AppConsoleSessionManager) StartSession(ctx context.Context, orgId uuid.UUID, deviceName, appName, consoleType string, force bool) (*AppConsoleSession, domain.Status) {
	if appName == "" {
		return nil, domain.StatusBadRequest("appName is required")
	}
	if consoleType == "" {
		return nil, domain.StatusBadRequest("consoleType is required")
	}
	if consoleType != string(api.ConsoleTypeSerial) && consoleType != string(api.ConsoleTypeVnc) {
		return nil, domain.StatusBadRequest(fmt.Sprintf("invalid consoleType: must be %q or %q", api.ConsoleTypeSerial, api.ConsoleTypeVnc))
	}

	device, status := m.svc.GetDevice(ctx, orgId, deviceName)
	if status.Code != http.StatusOK {
		return nil, status
	}
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

	// Check for duplicate session for this appName in the current annotation value (fast path).
	// Skipped when force is set: the atomic updater below replaces any existing entry instead.
	if !force {
		if val, ok := annotations[domain.DeviceAnnotationRemoteSession]; ok && val != "" {
			var sessions []domain.DeviceRemoteSession
			if err := json.Unmarshal([]byte(val), &sessions); err == nil {
				for _, s := range sessions {
					if s.AppName == appName {
						return nil, domain.StatusConflict("console session already active for application " + appName)
					}
				}
			}
		}
	}

	session := &AppConsoleSession{
		UUID:       uuid.New().String(),
		OrgId:      orgId,
		DeviceName: deviceName,
		AppName:    appName,
		SendCh:     make(chan []byte, ChannelSize),
		RecvCh:     make(chan []byte, ChannelSize),
		ProtocolCh: make(chan string, 1),
		ErrCh:      make(chan string, 1),
	}

	var replacedSessionID string
	updater := addAppSession(session.UUID, appName, consoleType)
	if force {
		updater = replaceAppSession(session.UUID, appName, consoleType, &replacedSessionID)
	}
	if status := m.modifyAnnotations(ctx, orgId, deviceName, true, updater); status.Code != http.StatusOK {
		// Attempt rollback in case the DB write succeeded but the Redis publish failed,
		// which would leave a stale annotation entry that permanently blocks future sessions.
		// Derive from ctx via WithoutCancel (not context.Background()) so the rollback keeps the
		// request's tracing span and other values but isn't cancelled by a client disconnect.
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer rollbackCancel()
		if annStatus := m.modifyAnnotations(rollbackCtx, orgId, deviceName, false, removeAppSession(session.UUID)); annStatus.Code != http.StatusOK {
			m.log.Errorf("Failed to roll back annotation for device %s after failed session start: %v", deviceName, annStatus)
		}
		return nil, status
	}
	if force && replacedSessionID != "" {
		m.log.Infof("app console session %s for device %s app %s forcibly replaced active session %s", session.UUID, deviceName, appName, replacedSessionID)
	}

	if err := m.sessionRegistration.StartSession(session); err != nil {
		m.log.Errorf("Failed to start app console session %s for device %s app %s: %v, rolling back annotation", session.UUID, deviceName, appName, err)
		// Derive from ctx via WithoutCancel (not context.Background()) so the rollback keeps the
		// request's tracing span and other values but isn't cancelled by a client disconnect.
		rollbackCtx, rollbackCancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer rollbackCancel()
		if annStatus := m.modifyAnnotations(rollbackCtx, orgId, deviceName, false, removeAppSession(session.UUID)); annStatus.Code != http.StatusOK {
			m.log.Errorf("Failed to remove annotation from device %s: %v", deviceName, annStatus)
		}
		return nil, domain.StatusInternalServerError(err.Error())
	}

	if err := m.notifier.NotifyConsole(ctx, orgId, deviceName); err != nil {
		m.log.Warnf("StartSession: failed to notify device %s: %v", deviceName, err)
	}

	return session, domain.StatusOK()
}

// CloseSession removes the annotation entry and unregisters the session.
// Annotation cleanup runs first so that a DB failure does not leave the session
// removed from pendingStreams while the device annotation still advertises it.
func (m *AppConsoleSessionManager) CloseSession(ctx context.Context, session *AppConsoleSession) domain.Status {
	if status := m.modifyAnnotations(ctx, session.OrgId, session.DeviceName, false, removeAppSession(session.UUID)); status.Code != http.StatusOK {
		return status
	}
	if err := m.sessionRegistration.CloseSession(session); err != nil {
		return domain.StatusInternalServerError(err.Error())
	}
	return domain.StatusOK()
}
