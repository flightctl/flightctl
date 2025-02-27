package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

const (
	Forbidden                      = "Forbidden"
	AuthorizationServerUnavailable = "Authorization server unavailable"
)

func SetResponse(w http.ResponseWriter, body any, status api.Status) {
	w.Header().Set("Content-Type", "application/json")

	// Encode body into a buffer first to catch encoding errors before writing the response
	var buf bytes.Buffer
	var err error

	if body != nil && status.Code >= 200 && status.Code < 300 {
		err = json.NewEncoder(&buf).Encode(body)
	} else {
		err = json.NewEncoder(&buf).Encode(status)
	}

	if err != nil {
		// If encoding fails, send an internal server error response
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Now that encoding is successful, write the status and response
	w.WriteHeader(int(status.Code))
	_, _ = w.Write(buf.Bytes()) // Write the encoded JSON from the buffer
}

func SetParseFailureResponse(w http.ResponseWriter, err error) {
	SetResponse(w, nil, api.StatusInternalServerError(fmt.Sprintf("can't decode JSON body: %v", err)))
}
