package service

import (
	"context"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/go-chi/chi"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow connections from any origin
	},
}

func (h *WebsocketHandler) HandleDeviceConsole(w http.ResponseWriter, r *http.Request) {
	// Extract the device name from the URL
	deviceName := chi.URLParam(r, "name")
	h.log.Printf("WebSocket connection requested for device: %s", deviceName)

	// Upgrade the HTTP connection to a WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.log.Errorf("Failed to upgrade connection to WebSocket: %v", err)
		return
	}
	defer conn.Close()

	h.log.Printf("WebSocket connection established for device: %s", deviceName)

	for {
		// Read message from the WebSocket connection
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			h.log.Printf("WebSocket connection closed for device %s: %v", deviceName, err)
			break
		}

		// Log the message received (for debugging)
		h.log.Printf("Received message from %s: %s", deviceName, string(message))

		// Echo the message back to the client
		err = conn.WriteMessage(messageType, message)
		if err != nil {
			h.log.Printf("Failed to write message to %s: %v", deviceName, err)
			break
		}
	}
}
func (h *ServiceHandler) RequestConsole(ctx context.Context, request server.RequestConsoleRequestObject) (server.RequestConsoleResponseObject, error) {
	orgId := store.NullOrgId

	// make sure the device exists
	_, err := h.store.Device().Get(ctx, orgId, request.Name)
	if err != nil {
		switch err {
		case flterrors.ErrResourceNotFound:
			return server.RequestConsole404JSONResponse{}, nil
		default:
			return nil, err
		}
	}

	sessionId := uuid.New().String()

	annotations := map[string]string{api.DeviceAnnotationConsole: sessionId}

	if err := h.store.Device().UpdateAnnotations(ctx, orgId, request.Name, annotations, []string{}); err != nil {
		h.log.WithError(err).Error("failed to check authorization permission")
		return server.RequestConsole503JSONResponse{Message: "Unable to annotate device for console setup"}, err
	}

	// create a new console session
	return server.RequestConsole200JSONResponse{
		SessionID:    sessionId,
		GRPCEndpoint: h.consoleGrpcEndpoint,
	}, nil

}
