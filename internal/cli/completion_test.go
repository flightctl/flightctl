package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func makeDeviceListResponse(t *testing.T, numItems int) *http.Response {
	t.Helper()

	items := make([]api.Device, numItems)
	for i := 0; i < numItems; i++ {
		name := fmt.Sprintf("machine-%d", i)
		items[i] = api.Device{
			ApiVersion: "v1",
			Kind:       api.DeviceKind,
			Metadata:   api.ObjectMeta{Name: &name},
		}
	}

	body, err := json.Marshal(api.DeviceList{
		ApiVersion: "v1",
		Kind:       api.DeviceListKind,
		Items:      items,
	})
	if err != nil {
		t.Fatalf("failed to marshal device list: %v", err)
	}

	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}

type fakeClientOptions struct {
	responses      []*http.Response
	client         *apiclient.ClientWithResponses
	fakeHTTPClient *fakeHTTPClient
	t              *testing.T
}

func newFakeClientOptionsWithResponses(t *testing.T, responses ...*http.Response) *fakeClientOptions {
	return &fakeClientOptions{
		responses: responses,
		t:         t,
	}
}

func (fo *fakeClientOptions) Complete(cmd *cobra.Command, args []string) error {
	return nil
}

func (fo *fakeClientOptions) BuildClient() (*apiclient.ClientWithResponses, error) {
	if fo.client == nil {
		fo.client, fo.fakeHTTPClient = newTestClient(fo.t, fo.responses...)
	}
	return fo.client, nil
}

func TestKindNameAutocompleter(t *testing.T) {
	t.Run("complete single kind/name arg", func(t *testing.T) {
		kna := KindNameAutocomplete{
			Options: newFakeClientOptionsWithResponses(t,
				makeDeviceListResponse(t, 3),
			),
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{EventKind, EnrollmentRequestKind, DeviceKind},
			FleetName:          new(string),
		}

		{
			suggestions, _ := kna.ValidArgsFunction(nil, []string{}, "de")
			require.ElementsMatch(t, []string{
				"device",
				"event",
				"enrollmentrequest",
			}, suggestions)
		}

		{
			suggestions, _ := kna.ValidArgsFunction(nil, []string{}, "device/")
			require.ElementsMatch(t, []string{
				"device/machine-0",
				"device/machine-1",
				"device/machine-2",
			}, suggestions)
		}
	})

	t.Run("complete single kind name arg", func(t *testing.T) {
		kna := KindNameAutocomplete{
			Options: newFakeClientOptionsWithResponses(t,
				makeDeviceListResponse(t, 3),
			),
			AllowMultipleNames: false,
			AllowedKinds:       []ResourceKind{EventKind, EnrollmentRequestKind, DeviceKind},
			FleetName:          new(string),
		}

		{
			suggestions, _ := kna.ValidArgsFunction(nil, []string{"device"}, "")
			require.ElementsMatch(t, []string{
				"machine-0",
				"machine-1",
				"machine-2",
			}, suggestions)
		}
	})

	t.Run("complete single kind and multiple name arg without duplicates", func(t *testing.T) {
		kna := KindNameAutocomplete{
			Options: newFakeClientOptionsWithResponses(t,
				makeDeviceListResponse(t, 3),
			),
			AllowMultipleNames: true,
			AllowedKinds:       []ResourceKind{EventKind, EnrollmentRequestKind, DeviceKind},
			FleetName:          new(string),
		}

		{
			suggestions, _ := kna.ValidArgsFunction(nil, []string{"device", "machine-0"}, "")
			require.ElementsMatch(t, []string{
				"machine-1",
				"machine-2",
			}, suggestions)
		}
	})

	t.Run("does not autocomplete unsupported kinds", func(t *testing.T) {
		kna := KindNameAutocomplete{
			Options:            newFakeClientOptionsWithResponses(t),
			AllowMultipleNames: true,
			AllowedKinds:       []ResourceKind{EventKind, EnrollmentRequestKind, DeviceKind},
			FleetName:          new(string),
		}

		{
			suggestions, _ := kna.ValidArgsFunction(nil, []string{}, "certi")
			require.ElementsMatch(t, []string{
				"events",
				"enrollmentrequests",
				"devices",
			}, suggestions)
		}
	})
}
