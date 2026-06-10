package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	v1alpha1client "github.com/flightctl/flightctl/internal/api/client/v1alpha1"
	imagebuilderclient "github.com/flightctl/flightctl/internal/api/imagebuilder/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

func makeDeviceListResponse(t *testing.T, numItems int) *http.Response {
	t.Helper()

	items := make([]api.Device, numItems)
	for i := 0; i < numItems; i++ {
		name := fmt.Sprintf("machine-%d", i)
		items[i] = api.Device{
			ApiVersion: "v1beta1",
			Kind:       api.DeviceKind,
			Metadata:   api.ObjectMeta{Name: &name},
		}
	}

	body, err := json.Marshal(api.DeviceList{
		ApiVersion: "v1beta1",
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
	client         *client.Client
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

func (fo *fakeClientOptions) BuildClient() (*client.Client, error) {
	if fo.client == nil {
		var apiClient *apiclient.ClientWithResponses
		apiClient, fo.fakeHTTPClient = newTestClient(fo.t, fo.responses...)
		fo.client = &client.Client{
			ClientWithResponses: apiClient,
		}
	}
	return fo.client, nil
}

func (fo *fakeClientOptions) BuildImageBuilderClient(opts ...imagebuilderclient.ClientOption) (*client.ImageBuilderClient, error) {
	return nil, fmt.Errorf("imagebuilder not configured in test")
}

// requestCapturingHTTPClient records every request it receives and returns a
// fixed response body, refreshing the body reader on each call.
type requestCapturingHTTPClient struct {
	requests []*http.Request
	body     []byte
	header   http.Header
}

func (r *requestCapturingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	r.requests = append(r.requests, req.Clone(req.Context()))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     r.header,
		Body:       io.NopCloser(bytes.NewReader(r.body)),
	}, nil
}

func newRequestCapturingClientWithEmptyCatalogItemList(t *testing.T) *requestCapturingHTTPClient {
	t.Helper()
	body, err := json.Marshal(apiv1alpha1.CatalogItemList{
		ApiVersion: "v1alpha1",
		Kind:       "CatalogItemList",
		Items:      []apiv1alpha1.CatalogItem{},
	})
	if err != nil {
		t.Fatalf("failed to marshal CatalogItemList: %v", err)
	}
	return &requestCapturingHTTPClient{
		body:   body,
		header: http.Header{"Content-Type": []string{"application/json"}},
	}
}

// fakeClientOptionsWithV1Alpha1 extends fakeClientOptions with a real v1alpha1
// client backed by a requestCapturingHTTPClient, so tests can assert on the
// exact HTTP requests sent during completion.
type fakeClientOptionsWithV1Alpha1 struct {
	*fakeClientOptions
	v1alpha1Fake *requestCapturingHTTPClient
}

func newFakeClientOptionsWithV1Alpha1(t *testing.T) *fakeClientOptionsWithV1Alpha1 {
	t.Helper()
	return &fakeClientOptionsWithV1Alpha1{
		fakeClientOptions: newFakeClientOptionsWithResponses(t),
		v1alpha1Fake:      newRequestCapturingClientWithEmptyCatalogItemList(t),
	}
}

func (fo *fakeClientOptionsWithV1Alpha1) BuildClient() (*client.Client, error) {
	if fo.fakeClientOptions.client == nil {
		apiClient, fakeHTTP := newTestClient(fo.t)
		fo.fakeClientOptions.fakeHTTPClient = fakeHTTP

		v1alpha1C, err := v1alpha1client.NewClientWithResponses("http://example.com", v1alpha1client.WithHTTPClient(fo.v1alpha1Fake))
		if err != nil {
			return nil, err
		}
		fo.fakeClientOptions.client = client.NewTestClientWithV1Alpha1(apiClient, v1alpha1C)
	}
	return fo.fakeClientOptions.client, nil
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

	t.Run("returns empty for imagebuilder kinds when imagebuilder not configured", func(t *testing.T) {
		for _, kind := range []ResourceKind{ImageBuildKind, ImageExportKind, ImagePromotionKind} {
			kna := KindNameAutocomplete{
				Options:      newFakeClientOptionsWithResponses(t),
				AllowedKinds: []ResourceKind{kind},
			}

			suggestions, directive := kna.ValidArgsFunction(nil, []string{kind.String()}, "")
			require.Empty(t, suggestions, "expected no suggestions for kind %s when imagebuilder is not configured", kind)
			require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
		}
	})

	t.Run("returns empty for catalog kinds when v1alpha1 client is not available", func(t *testing.T) {
		catalogName := ""
		for _, kind := range []ResourceKind{CatalogKind, CatalogItemKind} {
			kna := KindNameAutocomplete{
				Options:      newFakeClientOptionsWithResponses(t),
				AllowedKinds: []ResourceKind{kind},
				CatalogName:  &catalogName,
			}

			suggestions, directive := kna.ValidArgsFunction(nil, []string{kind.String()}, "")
			require.Empty(t, suggestions, "expected no suggestions for kind %s when v1alpha1 is not configured", kind)
			require.Equal(t, cobra.ShellCompDirectiveNoFileComp, directive)
		}
	})

	t.Run("sets field selector when catalog name is valid", func(t *testing.T) {
		opts := newFakeClientOptionsWithV1Alpha1(t)
		catalogName := "my-catalog"
		kna := KindNameAutocomplete{
			Options:      opts,
			AllowedKinds: []ResourceKind{CatalogItemKind},
			CatalogName:  &catalogName,
		}

		kna.ValidArgsFunction(nil, []string{CatalogItemKind.String()}, "")

		require.Len(t, opts.v1alpha1Fake.requests, 1, "expected one API call")
		gotFieldSelector := opts.v1alpha1Fake.requests[0].URL.Query().Get("fieldSelector")
		require.Contains(t, gotFieldSelector, "metadata.catalog=my-catalog", "field selector should scope to the catalog")
	})

	t.Run("does not inject field selector when catalog name contains a comma", func(t *testing.T) {
		opts := newFakeClientOptionsWithV1Alpha1(t)
		maliciousCatalogName := "valid-catalog,extra=injected"
		kna := KindNameAutocomplete{
			Options:      opts,
			AllowedKinds: []ResourceKind{CatalogItemKind},
			CatalogName:  &maliciousCatalogName,
		}

		// The comma guard must fire: the field selector must not contain the catalog
		// name, preventing injection of additional filter conditions.
		kna.ValidArgsFunction(nil, []string{CatalogItemKind.String()}, "")

		require.Len(t, opts.v1alpha1Fake.requests, 1, "expected one API call even when catalog name is invalid")
		gotFieldSelector := opts.v1alpha1Fake.requests[0].URL.Query().Get("fieldSelector")
		require.NotContains(t, gotFieldSelector, "metadata.catalog=", "field selector must not contain catalog filter when catalog name has a comma")
	})
}
