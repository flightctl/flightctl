package transport

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

func (h *WebsocketHandler) injectProtocolsToMetadata(metadataStr string, protocols []string) (string, error) {
	var metadata api.DeviceConsoleSessionMetadata
	if err := json.Unmarshal([]byte(metadataStr), &metadata); err != nil {
		return "", err
	}
	metadata.Protocols = protocols
	b, err := json.Marshal(&metadata)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (h *WebsocketHandler) HandleDeviceConsole(w http.ResponseWriter, r *http.Request) {
	orgId := store.NullOrgId
	deviceName := chi.URLParam(r, "name")

	h.log.Infof("websocket console connection requested for device: %s", deviceName)

	// Extract metadata
	metadata, err := h.injectProtocolsToMetadata(r.URL.Query().Get(api.DeviceQueryConsoleSessionMetadata),
		websocket.Subprotocols(r))
	if err != nil {
		h.log.Errorf("failed injecting protocols to metadata for device %s: %v", deviceName, err)
		http.Error(w, "protocols injection error", http.StatusInternalServerError)
		return
	}
	consoleSession, err := h.consoleSessionManager.StartSession(r.Context(), orgId, deviceName, metadata)
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

	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	var (
		selectedProtocol string
		ok               bool
	)
	select {
	case selectedProtocol, ok = <-consoleSession.ProtocolCh:
		if !ok {
			h.log.Errorf("failed selecting protocol for device: %s", deviceName)
			http.Error(w,
				fmt.Sprintf("failed selecting protocol for device: %s", deviceName),
				http.StatusInternalServerError)
			return
		}
	case <-timer.C:
		h.log.Errorf("timed out waiting for protocol for device: %s", deviceName)
		http.Error(w,
			fmt.Sprintf("timed out waiting for protocol for device: %s", deviceName),
			http.StatusInternalServerError)
		return
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow connections from any origin
		},
		Subprotocols: []string{
			selectedProtocol, // Required for Kubernetes-compatible streaming
		},
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
			if msgType == websocket.BinaryMessage {
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
