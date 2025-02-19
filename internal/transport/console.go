package transport

import (
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/auth"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow connections from any origin
	},
}

func (h *WebsocketHandler) HandleDeviceConsole(w http.ResponseWriter, r *http.Request) {
	allowed, err := auth.GetAuthZ().CheckPermission(r.Context(), "devices/console", "get")
	if err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		http.Error(w, AuthorizationServerUnavailable, http.StatusServiceUnavailable)
		return
	}
	if !allowed {
		http.Error(w, Forbidden, http.StatusForbidden)
		return
	}
	orgId := store.NullOrgId
	deviceName := chi.URLParam(r, "name")

	h.log.Debugf("websocket console connection requested for device: %s", deviceName)
	consoleSession, err := h.consoleSessionManager.StartSession(r.Context(), orgId, deviceName)
	// check for errors
	if err != nil {
		switch {
		case errors.Is(err, flterrors.ErrResourceNotFound):
			h.log.Errorf("console requested for unknown device: %s", deviceName)
			http.Error(w, "Device not found", http.StatusNotFound)
		default:
			h.log.Errorf("There was an error retrieving DB from database during console request: %v", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Upgrade the HTTP connection to a WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Errorf("Failed to upgrade connection to WebSocket: %v", err)
		http.Error(w,
			fmt.Sprintf("Failed to upgrade connection to WebSocket: %v", err),
			http.StatusInternalServerError)
		return
	}

	stopWriter := make(chan struct{})

	wg := sync.WaitGroup{}
	wg.Add(2)

	// go routine to read from the websocket connection and send to the console session
	go func() {
		defer func() {
			// ensure that the other go func ends now
			close(stopWriter)
			// tell the console session that we are done
			close(consoleSession.SendCh)
			wg.Done()
		}()

		for {
			// Read message from the WebSocket connection
			msgType, message, err := conn.ReadMessage()
			if err != nil {
				h.log.Infof("websocket console session %s closed for device %s: %v", consoleSession.UUID, deviceName, err)
				break
			}
			// if it's binary or text message, forward it to the console session
			if msgType == websocket.BinaryMessage || msgType == websocket.TextMessage {
				consoleSession.SendCh <- message
			} else {
				h.log.Warningf("Received unexpected message type %d from console websocket session %s for device %s",
					msgType, consoleSession.UUID, deviceName)
			}
		}
	}()

	// go routine to read from the console session and write to the websocket connection
	go func() {
		defer func() {
			h.log.Debugf("Sending close message to console websocket for %s", deviceName)

			if err := conn.WriteControl(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
				time.Now().Add(time.Second*5),
			); err != nil {
				h.log.Errorf("Failed to write close message to console websocket for session %s: %v", consoleSession.UUID, err)
			}

			// close the websocket connection so that the other go routine ends
			conn.Close()
			wg.Done()
		}()

		for {
			select {
			case <-stopWriter:
				h.log.Debugf("The console from the device channel has been closed for session %s", consoleSession.UUID)
				return

			case message, ok := <-consoleSession.RecvCh:
				if !ok {
					h.log.Debugf("The console channel from the device  has been closed for session %s", consoleSession.UUID)
					return
				}

				// echo the message received from the device console back to the websocket client
				if err := conn.WriteMessage(websocket.BinaryMessage, message); err != nil {
					h.log.Errorf("Failed to write message to console websocket for %s: %v", deviceName, err)
					// make sure that the reader goroutine is also stopped
					return
				}
			}
		}
	}()

	wg.Wait()
	h.log.Infof("Ending console session %s to device %s", consoleSession.UUID, deviceName)
	err = h.consoleSessionManager.CloseSession(r.Context(), consoleSession)
	if err != nil {
		h.log.Errorf("Error closing console session %s for device %s: %v", consoleSession.UUID, deviceName, err)
	}
}
