package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type deleteTestSpec struct {
	ResourceKind string
	ResourceName string
	Args         []string
	StatusCode   int
	WantError    bool
}

func deleteHandlerGen(tt deleteTestSpec, t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		require := require.New(t)

		headers := w.Header()
		headers["Content-Type"] = []string{"application/json"}
		w.WriteHeader(tt.StatusCode)
		_, err := w.Write([]byte("{}"))
		require.NoError(err)
	}
}

func TestDelete(t *testing.T) {
	type Response struct {
		StatusCode int
		WantError  bool
	}
	responses := []Response{
		{http.StatusOK, false},
		{http.StatusUnauthorized, true},
	}
	responsesFromNamed := []Response{
		{http.StatusOK, false},
		{http.StatusUnauthorized, true},
		{http.StatusNotFound, true},
	}

	kinds := []string{
		DeviceKind,
		EnrollmentRequestKind,
		FleetKind,
		TemplateVersionKind,
		RepositoryKind,
		ResourceSyncKind,
	}

	specs := []deleteTestSpec{}

	for _, response := range responses {
		for _, kind := range kinds {
			specs = append(specs, deleteTestSpec{
				kind,
				"",
				[]string{kind},
				response.StatusCode,
				response.WantError,
			})
		}
	}

	for _, response := range responsesFromNamed {
		for _, kind := range kinds {
			specs = append(specs, deleteTestSpec{
				kind,
				"foo",
				[]string{kind + "/foo"},
				response.StatusCode,
				response.WantError,
			})
		}
	}

	for i, spec := range specs {
		if spec.ResourceKind == TemplateVersionKind {
			specs[i].Args = append(specs[i].Args, []string{"--fleetname", "foo"}...)
		}
	}

	for _, tt := range specs {
		name := fmt.Sprintf("delete %s %d", tt.ResourceKind, tt.StatusCode)
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			configFile, err := os.CreateTemp("", "config-*.yaml")
			require.NoError(err)

			server := httptest.NewServer(deleteHandlerGen(tt, t))

			config := &client.Config{}
			config.Service.Server = server.URL
			config.Service.InsecureSkipVerify = true
			y, err := yaml.Marshal(config)
			require.NoError(err)
			_, err = configFile.Write(y)
			require.NoError(err)

			cmd := NewCmdDelete()
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
