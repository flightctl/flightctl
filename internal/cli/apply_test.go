package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type applyTestSpec struct {
	ResourceFile string
	Args         []string
	StatusCode   int
	WantError    bool
}

func applyHandlerGen(tt applyTestSpec, t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		require := require.New(t)
		body, err := io.ReadAll(req.Body)
		require.NoError(err)
		resourceFile, err := os.Open(tt.ResourceFile)
		require.NoError(err)
		resourceFileBytes, err := io.ReadAll(resourceFile)
		require.NoError(err)
		var (
			resourceFromRequest any
			resourceFromFile    any
		)
		err = json.Unmarshal(body, &resourceFromRequest)
		require.NoError(err)
		err = yaml.Unmarshal(resourceFileBytes, &resourceFromFile)
		require.NoError(err)

		require.True(
			reflect.DeepEqual(resourceFromRequest, resourceFromFile),
		)

		headers := w.Header()
		headers["Content-Type"] = []string{"application/json"}
		w.WriteHeader(tt.StatusCode)
		_, err = w.Write([]byte("{}"))
		require.NoError(err)
	}
}

func TestApply(t *testing.T) {
	p := "../../examples/"

	responses := []struct {
		StatusCode int
		WantError  bool
	}{
		{http.StatusOK, false},
		{http.StatusCreated, false},
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusNotFound, true},
		{http.StatusConflict, true},
	}

	resourceFiles := []string{
		"device.yaml",
		"enrollmentrequest.yaml",
		"fleet.yaml",
		"resourcesync.yaml",
		"repository-flightctl.yaml",
	}

	specs := []applyTestSpec{}

	for _, response := range responses {
		for _, resourceFile := range resourceFiles {
			specs = append(specs, applyTestSpec{
				p + resourceFile,
				[]string{"-f", p + resourceFile},
				response.StatusCode,
				response.WantError,
			})
		}
	}

	for _, tt := range specs {
		name := fmt.Sprintf("apply %s %d", tt.ResourceFile, tt.StatusCode)
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			configFile, err := os.CreateTemp("", "config-*.yaml")
			require.NoError(err)

			server := httptest.NewServer(applyHandlerGen(tt, t))

			config := &client.Config{}
			config.Service.Server = server.URL
			config.Service.InsecureSkipVerify = true
			y, err := yaml.Marshal(config)
			require.NoError(err)
			_, err = configFile.Write(y)
			require.NoError(err)

			cmd := NewCmdApply()
			args := append(tt.Args, []string{"--config-path", configFile.Name()}...)
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
