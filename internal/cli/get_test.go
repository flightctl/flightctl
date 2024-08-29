package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

type getTestSpec struct {
	TestName     string
	ResourceKind string
	ResourceName string
	Args         []string
	StatusCode   int
	WantError    bool
}

var (
	Name  = "foo"
	Owner = "bar"
)

// Return a struct with all of the fields that `get <kind>` accesses
func getTestResource(kind string) (any, error) {
	now := time.Now()

	switch kind {
	case DeviceKind:
		return &api.Device{
			ApiVersion: "v1alpha1",
			Kind:       DeviceKind,
			Metadata: api.ObjectMeta{
				Name:  &Name,
				Owner: &Owner,
			},
			Status: &api.DeviceStatus{
				Summary: api.DeviceSummaryStatus{
					Status: "alive",
				},
				Updated: api.DeviceUpdatedStatus{
					Status: "updated",
				},
				Applications: api.DeviceApplicationsStatus{
					Summary: api.ApplicationsSummaryStatus{
						Status: "running",
					},
				},
				LastSeen: now,
			},
		}, nil
	case TemplateVersionKind:
		return &api.TemplateVersion{
			ApiVersion: "v1alpha1",
			Kind:       TemplateVersionKind,
			Metadata: api.ObjectMeta{
				Name: &Name,
			},
		}, nil
	case EnrollmentRequestKind:
		return &api.EnrollmentRequest{
			ApiVersion: "v1alpha1",
			Kind:       EnrollmentRequestKind,
			Metadata: api.ObjectMeta{
				Name: &Name,
			},
			Status: &api.EnrollmentRequestStatus{
				Approval: &api.EnrollmentRequestApproval{
					Approved:   true,
					ApprovedBy: &Owner,
					ApprovedAt: &now,
					Labels: &map[string]string{
						"foo": "bar",
					},
				},
			},
		}, nil
	case FleetKind:
		return &api.Fleet{
			ApiVersion: "v1alpha1",
			Kind:       FleetKind,
			Metadata: api.ObjectMeta{
				Name:  &Name,
				Owner: &Owner,
			},
			Spec: api.FleetSpec{
				Selector: &api.LabelSelector{
					MatchLabels: map[string]string{
						"foo": "bar",
					},
				},
			},
			Status: &api.FleetStatus{
				Conditions: []api.Condition{
					{
						Type: api.FleetValid,
					},
				},
			},
		}, nil
	case RepositoryKind:
		resource := api.Repository{
			ApiVersion: "v1alpha1",
			Kind:       RepositoryKind,
			Metadata: api.ObjectMeta{
				Name: &Name,
			},
			Spec: api.RepositorySpec{},
			Status: &api.RepositoryStatus{
				Conditions: []api.Condition{
					{
						Type: api.RepositoryAccessible,
					},
				},
			},
		}
		return resource, nil

	case ResourceSyncKind:
		return &api.ResourceSync{
			ApiVersion: "v1alpha1",
			Kind:       RepositoryKind,
			Metadata: api.ObjectMeta{
				Name: &Name,
			},
			Spec: api.ResourceSyncSpec{
				Repository:     "flightctl",
				Path:           "/etc/motd",
				TargetRevision: "main",
			},
			Status: &api.ResourceSyncStatus{
				Conditions: []api.Condition{
					{
						Type:   api.ResourceSyncAccessible,
						Status: api.ConditionStatusTrue,
					},
					{
						Type:               api.ResourceSyncSynced,
						Status:             api.ConditionStatusTrue,
						LastTransitionTime: now,
					},
				},
			},
		}, nil
	}

	return nil, fmt.Errorf("Unexpected kind %s\n", kind)
}

func getHandlerGen(tt getTestSpec, t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		require := require.New(t)

		headers := w.Header()
		headers["Content-Type"] = []string{"application/json"}
		w.WriteHeader(tt.StatusCode)

		if tt.ResourceName == "" {
			_, err := w.Write([]byte("{}"))
			require.NoError(err)
			return
		}

		resource, err := getTestResource(tt.ResourceKind)
		require.NoError(err)
		j, err := json.Marshal(resource)
		require.NoError(err)
		_, err = w.Write(j)
		require.NoError(err)
	}
}

func TestGet(t *testing.T) {
	statusCodes := [][]int{
		{http.StatusOK, 0},
		{http.StatusUnauthorized, 1},
	}
	statusCodesFromName := [][]int{
		{http.StatusOK, 0},
		{http.StatusUnauthorized, 1},
		{http.StatusNotFound, 1},
	}

	kinds := []string{
		DeviceKind,
		EnrollmentRequestKind,
		FleetKind,
		RepositoryKind,
		ResourceSyncKind,
		TemplateVersionKind,
	}

	specs := []getTestSpec{}

	for _, statusCode := range statusCodes {
		for _, kind := range kinds {
			specs = append(specs, getTestSpec{
				fmt.Sprintf("%s %d", kind, statusCode[0]),
				kind,
				"",
				[]string{kind},
				statusCode[0],
				statusCode[1] != 0, // int -> bool
			})
		}
	}

	for _, statusCode := range statusCodesFromName {
		for _, kind := range kinds {
			deviceName := kind + "/" + Name
			specs = append(specs, getTestSpec{
				fmt.Sprintf("%s %d", deviceName, statusCode[0]),
				kind,
				Name,
				[]string{deviceName},
				statusCode[0],
				statusCode[1] != 0, // int -> bool
			})
		}
	}

	for i, spec := range specs {
		if spec.ResourceKind == TemplateVersionKind {
			specs[i].Args = append(spec.Args, "--fleetname", "foo")
		}
	}

	additionalSpecs := []getTestSpec{
		{
			"--selector",
			DeviceKind,
			"",
			[]string{DeviceKind, "--selector", "key1=value1,key2=value2"},
			http.StatusOK,
			false,
		},
		{
			"--status-filter",
			DeviceKind,
			"",
			[]string{DeviceKind, "--status-filter=update.status=UpToDate"},
			http.StatusOK,
			false,
		},
		{
			"--output=yaml",
			DeviceKind,
			"",
			[]string{DeviceKind, "--output", "yaml"},
			http.StatusOK,
			false,
		},
		{
			"--output=json",
			DeviceKind,
			"",
			[]string{DeviceKind, "--output", "json"},
			http.StatusOK,
			false,
		},
		{
			"--limit",
			DeviceKind,
			"",
			[]string{DeviceKind, "--limit", "32"},
			http.StatusOK,
			false,
		},
		{
			"--continue",
			DeviceKind,
			"",
			[]string{DeviceKind, "--continue", Name},
			http.StatusOK,
			false,
		},
		{
			"--fleetname",
			DeviceKind,
			"",
			[]string{DeviceKind, "--fleetname", "foo"},
			http.StatusOK,
			false,
		},
		{
			"--rendered",
			DeviceKind,
			Name,
			[]string{DeviceKind + "/" + Name, "--rendered"},
			http.StatusOK,
			false,
		},
	}

	specs = append(specs, additionalSpecs...)

	for _, tt := range specs {
		name := fmt.Sprintf("get %s %d", tt.Args[0], tt.StatusCode)
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			configFile, err := os.CreateTemp("", "config-*.yaml")
			require.NoError(err)

			server := httptest.NewServer(getHandlerGen(tt, t))

			config := &client.Config{}
			config.Service.Server = server.URL
			config.Service.InsecureSkipVerify = true
			y, err := yaml.Marshal(config)
			require.NoError(err)
			_, err = configFile.Write(y)
			require.NoError(err)

			cmd := NewCmdGet()
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
