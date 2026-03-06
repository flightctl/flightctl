package middleware_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http"
	"net/http/httptest"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api_server/middleware"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/stretchr/testify/require"
)

func TestWriteJSONError(t *testing.T) {
	require := require.New(t)

	// Test case 1: Standard error
	w := httptest.NewRecorder()
	err := errors.New("a regular error")
	middleware.WriteJSONError(w, http.StatusBadRequest, err)

	require.Equal(http.StatusBadRequest, w.Code)
	require.Equal("application/json", w.Header().Get("Content-Type"))

	var status api.Status
	err = json.Unmarshal(w.Body.Bytes(), &status)
	require.NoError(err)

	require.Equal("Status", status.Kind)
	require.Equal("Failure", status.Status)
	require.Equal(http.StatusBadRequest, status.Code)
	require.Equal("a regular error", status.Message)
	require.Equal(http.StatusText(http.StatusBadRequest), status.Reason)

	// Test case 2: flightctl error
	w = httptest.NewRecorder()
	err = flterrors.ErrNotOrgMember
	middleware.WriteJSONError(w, http.StatusForbidden, err)

	require.Equal(http.StatusForbidden, w.Code)
	require.Equal("application/json", w.Header().Get("Content-Type"))

	err = json.Unmarshal(w.Body.Bytes(), &status)
	require.NoError(err)
	require.Equal(http.StatusForbidden, status.Code)
	require.Equal(flterrors.ErrNotOrgMember.Error(), status.Message)
	require.Equal(http.StatusText(http.StatusForbidden), status.Reason)
}
