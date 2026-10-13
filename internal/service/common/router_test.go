package common

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/require"
)

// getRouterFor has to clear Servers to skip server name validation, but the
// spec it is handed is a process-wide cached instance shared with the request
// validator installed at server start-up. Clearing it in place would strip the
// servers out from under that validator on the first patch request.
func TestGetRouterForDoesNotMutateSharedSpec(t *testing.T) {
	shared, err := openapi3.NewLoader().LoadFromData([]byte(`
openapi: 3.0.3
info:
  title: test
  version: v1
servers:
  - url: https://example.test/api
paths:
  /widgets:
    get:
      responses:
        "200":
          description: ok
`))
	require.NoError(t, err)
	require.Len(t, shared.Servers, 1)

	getter := func() (*openapi3.T, error) { return shared, nil }
	router, err := getRouterFor(getter)
	require.NoError(t, err)
	require.NotNil(t, router)

	require.Len(t, shared.Servers, 1, "getRouterFor must not clear Servers on the shared spec")
	require.Equal(t, "https://example.test/api", shared.Servers[0].URL)
}
