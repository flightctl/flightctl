package middleware

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
)

// WriteJSONError writes a structured JSON error to the response writer.
func WriteJSONError(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)

	status := api.Status{
		Kind:       "Status",
		ApiVersion: "v1beta1",
		Status:     "Failure",
		Message:    err.Error(),
		Reason:     http.StatusText(code),
		Code:       int32(code),
	}

	// We can't do much if this fails. Maybe log it?
	_ = json.NewEncoder(w).Encode(status)
}
