package middleware

import (
	"encoding/json"
	"net/http"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

func WriteJSONError(w http.ResponseWriter, code int, reason string, err error) {
	status := api.Status{
		Code:    int32(code),
		Message: err.Error(),
		Reason:  reason,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(status)
}
