

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// Tests for writeJSONResponse.
// -----------------------------------------------------------------------------
func TestWriteJSONResponse(t *testing.T) {
	require := require.New(t)

	// given
	w := httptest.NewRecorder()
	code := http.StatusNotFound
	msg := "resource not found"

	// when
	writeJSONResponse(w, code, msg)

	// then
	require.Equal(code, w.Code)
	require.Equal("application/json", w.Header().Get("Content-Type"))

	var status api.Status
	err := json.Unmarshal(w.Body.Bytes(), &status)
	require.NoError(err)
	require.Equal(int32(code), status.Code)
	require.Equal(msg, status.Message)
}
