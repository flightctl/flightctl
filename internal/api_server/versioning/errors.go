package versioning

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
)

// ErrNotAcceptable indicates the requested version is not supported for the endpoint
var ErrNotAcceptable = errors.New("requested API version not acceptable")

func writeNotAcceptable(w http.ResponseWriter, supported []Version, fallbackVersion Version) {
	strs := make([]string, len(supported))
	for i, v := range supported {
		strs[i] = string(v)
	}
	supportedStr := strings.Join(strs, ", ")

	// Set supported versions header
	if len(supported) > 0 {
		w.Header().Set(HeaderAPIVersionsSupported, supportedStr)
		// Use first supported version (most preferred) as the version header
		w.Header().Set(HeaderAPIVersion, string(supported[0]))
	} else {
		w.Header().Set(HeaderAPIVersion, string(fallbackVersion))
	}

	status := api.Status{
		Code:    http.StatusNotAcceptable,
		Message: "Requested API version is not supported for this endpoint. Supported versions: " + supportedStr,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotAcceptable)
	_ = json.NewEncoder(w).Encode(status)
}
