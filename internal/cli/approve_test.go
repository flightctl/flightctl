package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type approveTestSpec struct {
	Args       []string
	StatusCode int
	WantError  bool
}

var (
	approveLabelArray = "label1=a,label2=b"
	approveLabelMap   = map[string]string{
		"label1": "a",
		"label2": "b",
	}
)

func approveHandlerGen(tt approveTestSpec, t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		require := require.New(t)
		enrollmentReq := &api.EnrollmentRequestApproval{}
		body, err := io.ReadAll(req.Body)
		require.NoError(err)
		err = json.Unmarshal(body, enrollmentReq)
		require.NoError(err)

		require.True(maps.Equal(approveLabelMap, *enrollmentReq.Labels))

		headers := w.Header()
		headers["Content-Type"] = []string{"application/json"}
		w.WriteHeader(tt.StatusCode)
	}
}

func TestApprove(t *testing.T) {
	deviceName := uuid.New().String()

	args := []string{EnrollmentRequestKind + "/" + deviceName, "-l", approveLabelArray}

	specs := []approveTestSpec{
		{
			args,
			http.StatusOK,
			false,
		},
		{
			args,
			http.StatusUnprocessableEntity,
			true,
		},
		{
			args,
			http.StatusBadRequest,
			true,
		},
		{
			args,
			http.StatusUnauthorized,
			true,
		},
		{
			args,
			http.StatusNotFound,
			true,
		},
	}

	for _, tt := range specs {
		name := fmt.Sprintf("approve %d", tt.StatusCode)
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			configFile, err := os.CreateTemp("", "config-*.yaml")
			require.NoError(err)

			server := httptest.NewServer(approveHandlerGen(tt, t))

			config := &client.Config{}
			config.Service.Server = server.URL
			config.Service.InsecureSkipVerify = true
			y, err := yaml.Marshal(config)
			require.NoError(err)
			_, err = configFile.Write(y)
			require.NoError(err)

			cmd := NewCmdApprove()
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
