package v1

import (
	"bytes"
	"encoding/json"
	"net/http"

	v1 "github.com/flightctl/flightctl/api/v1"
)

// setV1Response writes a v1 API response with the given status.
func setV1Response(w http.ResponseWriter, body any, status v1.Status) {
	code := http.StatusInternalServerError
	if status.Code != nil {
		code = int(*status.Code)
	}

	// Never write a body for 204/304 (and generally 1xx), per RFC 7231
	if code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200) {
		w.WriteHeader(code)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	var buf bytes.Buffer
	var err error

	if body != nil && code >= 200 && code < 300 {
		err = json.NewEncoder(&buf).Encode(body)
	} else {
		err = json.NewEncoder(&buf).Encode(status)
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// setV1SuccessResponse writes a successful v1 API response.
func setV1SuccessResponse(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}

// setErrorResponse writes an error response in v1 format.
func setErrorResponse(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")

	code32 := int32(code) //nolint:gosec // HTTP status codes (100-599) always fit in int32
	status := v1.Status{
		Code:    &code32,
		Message: &message,
	}

	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(status); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(code)
	_, _ = w.Write(buf.Bytes())
}
