package transportv1beta1

import (
	"encoding/json"
	"net/http"
	"strings"
)

// unknownJSONFieldError marks a request body containing a field outside the
// declared schema.
type unknownJSONFieldError struct {
	cause error
}

func (e *unknownJSONFieldError) Error() string { return e.cause.Error() }

func (e *unknownJSONFieldError) Unwrap() error { return e.cause }

// decodeStrictJSON decodes a JSON request body and rejects unknown fields, so
// clients cannot smuggle server-owned metadata into a typed request.
func decodeStrictJSON(r *http.Request, dst any) error {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return &unknownJSONFieldError{cause: err}
		}
		return err
	}
	return nil
}
