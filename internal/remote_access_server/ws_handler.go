package remote_access_server

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/console"
	"github.com/flightctl/flightctl/internal/transport"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// AppConsoleHandler handles WebSocket connections for application-level console sessions.
type AppConsoleHandler struct {
	log                      logrus.FieldLogger
	appConsoleSessionManager *console.AppConsoleSessionManager
}

// NewAppConsoleHandler returns an AppConsoleHandler that upgrades HTTP connections
// to WebSocket and bridges them to AppConsoleSessions.
func NewAppConsoleHandler(log logrus.FieldLogger, mgr *console.AppConsoleSessionManager) *AppConsoleHandler {
	return &AppConsoleHandler{
		log:                      log,
		appConsoleSessionManager: mgr,
	}
}

// RegisterRoutes mounts the application console WebSocket endpoint.
func (h *AppConsoleHandler) RegisterRoutes(r chi.Router) {
	r.Get("/ws/v1/devices/{name}/applications/{appname}/console", h.HandleApplicationConsole)
}

// HandleApplicationConsole upgrades the HTTP connection to WebSocket and bridges
// it bidirectionally to the AppConsoleSession for the given device and application.
func (h *AppConsoleHandler) HandleApplicationConsole(w http.ResponseWriter, r *http.Request) {
	deviceName := chi.URLParam(r, "name")
	appName := chi.URLParam(r, "appname")

	h.log.Infof("websocket application console connection requested for device: %s app: %s", deviceName, appName)

	consoleType := r.URL.Query().Get("consoleType")
	if consoleType == "" {
		http.Error(w, "consoleType is required", http.StatusBadRequest)
		return
	}
	if consoleType != string(api.ConsoleTypeSerial) && consoleType != string(api.ConsoleTypeVnc) {
		http.Error(w, fmt.Sprintf("invalid consoleType %q: must be %q or %q", consoleType, api.ConsoleTypeSerial, api.ConsoleTypeVnc), http.StatusBadRequest)
		return
	}

	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "expected a WebSocket upgrade request", http.StatusBadRequest)
		return
	}

	orgId := transport.OrgIDFromContext(r.Context())

	session, status := h.appConsoleSessionManager.StartSession(r.Context(), orgId, deviceName, appName, consoleType)
	if status.Code != http.StatusOK {
		http.Error(w, status.Message, int(status.Code))
		return
	}

	sessionStarted := true
	closeSession := func() {
		if !sessionStarted {
			return
		}
		// Create the timeout here so the 30 s window starts at cleanup time, not session-start time.
		// Use a background context so annotation removal is not cancelled by a client disconnect.
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()
		if closeStatus := h.appConsoleSessionManager.CloseSession(closeCtx, session); closeStatus.Code != http.StatusOK {
			h.log.Errorf("error closing app console session %s for device %s app %s: %v", session.UUID, deviceName, appName, closeStatus.Message)
		}
		sessionStarted = false
	}
	defer closeSession()

	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	var (
		selectedProtocol string
		ok               bool
	)
	select {
	case selectedProtocol, ok = <-session.ProtocolCh:
		if !ok {
			close(session.SendCh)
			h.log.Errorf("failed selecting protocol for device: %s app: %s", deviceName, appName)
			http.Error(w,
				fmt.Sprintf("failed selecting protocol for device: %s app: %s", deviceName, appName),
				http.StatusInternalServerError)
			return
		}
	case <-timer.C:
		close(session.SendCh)
		h.log.Errorf("timed out waiting for protocol for device: %s app: %s", deviceName, appName)
		http.Error(w,
			fmt.Sprintf("timed out waiting for protocol for device: %s app: %s", deviceName, appName),
			http.StatusGatewayTimeout)
		return
	case <-r.Context().Done():
		close(session.SendCh)
		h.log.Infof("client disconnected while waiting for protocol for device: %s app: %s", deviceName, appName)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		Subprotocols: []string{selectedProtocol},
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		// Close the agent-side send channel so pipeChannelToStream in Stream() unblocks promptly.
		close(session.SendCh)
		h.log.Errorf("failed to upgrade connection to WebSocket for device %s app %s: %v", deviceName, appName, err)
		return
	}
	conn.SetReadLimit(1 << 20)

	stopWriter := make(chan struct{})
	writerDone := make(chan struct{})
	wg := sync.WaitGroup{}
	wg.Add(2)

	go func() {
		defer func() {
			close(stopWriter)
			close(session.SendCh)
			wg.Done()
		}()
		for {
			msgType, message, err := conn.ReadMessage()
			if err != nil {
				h.log.Infof("websocket app console session %s closed for device %s app %s: %v", session.UUID, deviceName, appName, err)
				break
			}
			if msgType == websocket.BinaryMessage {
				select {
				case session.SendCh <- message:
				case <-writerDone:
					return
				case <-r.Context().Done():
					return
				}
			} else {
				h.log.Warningf("received unexpected message type %d from app console websocket session %s for device %s app %s",
					msgType, session.UUID, deviceName, appName)
			}
		}
	}()

	go func() {
		defer func() {
			close(writerDone)
			h.log.Debugf("sending close message to app console websocket for device %s app %s", deviceName, appName)
			if err := conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second*5),
			); err != nil {
				h.log.Errorf("failed to write close message to app console websocket for session %s: %v", session.UUID, err)
			}
			conn.Close()
			wg.Done()
		}()
		for {
			select {
			case <-stopWriter:
				h.log.Debugf("app console device channel closed for session %s", session.UUID)
				return
			case message, ok := <-session.RecvCh:
				if !ok {
					h.log.Debugf("app console channel from device closed for session %s", session.UUID)
					return
				}
				if err := conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
					h.log.Errorf("failed to write message to app console websocket for device %s app %s: %v", deviceName, appName, err)
					return
				}
			}
		}
	}()

	wg.Wait()
	h.log.Infof("ending app console session %s to device %s app %s", session.UUID, deviceName, appName)
}
