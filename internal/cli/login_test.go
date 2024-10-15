package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type loginTestSpec struct {
	Args       []string
	StatusCode int
	WantError  bool
}

func loginHandlerGen(tt loginTestSpec, t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		require := require.New(t)
		headers := w.Header()
		headers["Content-Type"] = []string{"application/json"}
		w.WriteHeader(tt.StatusCode)

		authConfig := api.AuthConfig{
			AuthType: "OIDC",
		}
		j, err := json.Marshal(authConfig)
		require.NoError(err)
		_, err = w.Write(j)
		require.NoError(err)
	}
}

func TestLogin(t *testing.T) {

	args := []string{"--token", "foo"}

	specs := []loginTestSpec{
		{
			args,
			http.StatusOK,
			false,
		},
		{
			args,
			http.StatusTeapot,
			false,
		},
	}

	for _, tt := range specs {
		name := fmt.Sprintf("login %d", tt.StatusCode)
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			configFile, err := os.CreateTemp("", "config-*.yaml")
			require.NoError(err)

			server := httptest.NewServer(loginHandlerGen(tt, t))

			config := &client.Config{}
			config.Service.Server = server.URL
			config.Service.InsecureSkipVerify = true
			y, err := yaml.Marshal(config)
			require.NoError(err)
			_, err = configFile.Write(y)
			require.NoError(err)

			cmd := NewCmdLogin()
			args = append(tt.Args, []string{server.URL, "--config-path", configFile.Name()}...)
			cmd.SetArgs(args)
			err = cmd.Execute()

			if tt.WantError {
				require.Error(err)
			} else {
				require.NoError(err)
			}
		})
	}
}
