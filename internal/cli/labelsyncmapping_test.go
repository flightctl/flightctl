package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	apiclient "github.com/flightctl/flightctl/internal/api/client"
	"github.com/flightctl/flightctl/internal/client"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// recordingHTTPClient captures the requests issued by the generated API client
// so tests can assert on the method, path and query string that a CLI command
// dispatches, and replies with a canned response.
type recordingHTTPClient struct {
	requests []*http.Request
	bodies   []string
	// response supplies the reply for every call.
	response func() *http.Response
}

func (r *recordingHTTPClient) Do(req *http.Request) (*http.Response, error) {
	body := ""
	if req.Body != nil {
		raw, err := io.ReadAll(req.Body)
		if err != nil {
			return nil, err
		}
		body = string(raw)
	}
	r.requests = append(r.requests, req)
	r.bodies = append(r.bodies, body)

	return r.response(), nil
}

func jsonResponse(t *testing.T, statusCode int, payload interface{}) *http.Response {
	t.Helper()

	raw, err := json.Marshal(payload)
	require.NoError(t, err)

	return &http.Response{
		StatusCode: statusCode,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(raw)),
	}
}

func newRecordingClient(t *testing.T, statusCode int, payload interface{}) (*client.Client, *recordingHTTPClient) {
	t.Helper()

	rec := &recordingHTTPClient{
		response: func() *http.Response { return jsonResponse(t, statusCode, payload) },
	}
	apiClient, err := apiclient.NewClientWithResponses("http://example.com", apiclient.WithHTTPClient(rec))
	require.NoError(t, err)

	return client.NewTestClient(apiClient), rec
}

// writeMinimalClientConfig writes an empty client config so that
// GlobalOptions.Validate does not fail on a missing login.
func writeMinimalClientConfig(t *testing.T) string {
	t.Helper()

	configFile := filepath.Join(t.TempDir(), "client.yaml")
	require.NoError(t, os.WriteFile(configFile, []byte("{}"), 0600))
	return configFile
}

func testLabelSyncMapping(name string) api.LabelSyncMapping {
	return api.LabelSyncMapping{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingKind(api.LabelSyncMappingKindValue),
		Metadata:   api.ObjectMeta{Name: lo.ToPtr(name)},
		Spec: api.LabelSyncMappingSpec{
			Expression:   "device.metadata.name",
			Key:          lo.ToPtr("flightctl.io/name"),
			ResourceType: api.LabelSyncMappingSpecResourceType("Device"),
		},
	}
}

func TestLabelSyncMappingKindParsing(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantKind ResourceKind
		wantErr  bool
	}{
		{name: "When given the singular kind it should resolve", input: "labelsyncmapping", wantKind: LabelSyncMappingKind},
		{name: "When given the plural kind it should resolve", input: "labelsyncmappings", wantKind: LabelSyncMappingKind},
		{name: "When given the shortname it should resolve", input: "lsm", wantKind: LabelSyncMappingKind},
		{name: "When given mixed case it should resolve", input: "LabelSyncMapping", wantKind: LabelSyncMappingKind},
		{name: "When given an unknown kind it should fail", input: "labelsyncmappingz", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			kind, err := ResourceKindFromString(tt.input)
			if tt.wantErr {
				require.Error(err)
				require.Equal(InvalidKind, kind)
				return
			}
			require.NoError(err)
			require.Equal(tt.wantKind, kind)
		})
	}
}

func TestLabelSyncMappingKindRegistration(t *testing.T) {
	require := require.New(t)

	_, ok := resourceKindSet[LabelSyncMappingKind]
	require.True(ok, "LabelSyncMappingKind must be a known resource kind")
	require.Equal("labelsyncmappings", LabelSyncMappingKind.ToPlural())
	require.Contains(getValidPluralResourceKinds(), "labelsyncmappings")
	require.True(slices.Contains(validResourceKinds, LabelSyncMappingKind))

	// LabelSyncMapping is a v1beta1 resource and must not require v1alpha1.
	_, isAlpha := alphaResources[LabelSyncMappingKind]
	require.False(isAlpha, "LabelSyncMapping must not be registered as an alpha resource")
	require.NoError(validateResourceAPIVersion(genericResource{"apiVersion": "flightctl.io/v1beta1"}, LabelSyncMappingKind))
}

func TestLabelSyncMappingGetList(t *testing.T) {
	require := require.New(t)

	list := api.LabelSyncMappingList{
		ApiVersion: api.LabelSyncMappingAPIVersion,
		Kind:       api.LabelSyncMappingListKind,
		Items:      []api.LabelSyncMapping{testLabelSyncMapping("mapping-a")},
	}
	c, rec := newRecordingClient(t, http.StatusOK, list)

	o := DefaultGetOptions()
	o.Limit = 25
	o.Continue = "token-1"

	response, err := o.getResourceList(context.Background(), c, LabelSyncMappingKind)
	require.NoError(err)

	listResponse, ok := response.(*apiclient.ListLabelSyncMappingsResponse)
	require.True(ok, "expected a ListLabelSyncMappingsResponse, got %T", response)
	require.NotNil(listResponse.JSON200)
	require.Len(listResponse.JSON200.Items, 1)

	require.Len(rec.requests, 1)
	req := rec.requests[0]
	require.Equal(http.MethodGet, req.Method)
	require.Equal("/labelsyncmappings", req.URL.Path)
	require.Equal("25", req.URL.Query().Get("limit"))
	require.Equal("token-1", req.URL.Query().Get("continue"))
}

func TestLabelSyncMappingGetSingle(t *testing.T) {
	require := require.New(t)

	c, rec := newRecordingClient(t, http.StatusOK, testLabelSyncMapping("mapping-a"))

	o := DefaultGetOptions()
	response, err := o.getSingleResource(context.Background(), c, LabelSyncMappingKind, "mapping-a")
	require.NoError(err)

	getResponse, ok := response.(*apiclient.GetLabelSyncMappingResponse)
	require.True(ok, "expected a GetLabelSyncMappingResponse, got %T", response)
	require.NotNil(getResponse.JSON200)
	require.Equal("mapping-a", *getResponse.JSON200.Metadata.Name)

	require.Len(rec.requests, 1)
	require.Equal(http.MethodGet, rec.requests[0].Method)
	require.Equal("/labelsyncmappings/mapping-a", rec.requests[0].URL.Path)
}

func TestLabelSyncMappingGetValidate(t *testing.T) {
	configFile := writeMinimalClientConfig(t)

	tests := []struct {
		name          string
		args          []string
		labelSelector string
		fieldSelector string
		wantErr       bool
		errContains   string
	}{
		{
			name: "When listing without selectors it should be accepted",
			args: []string{"labelsyncmappings"},
		},
		{
			name: "When getting a single mapping it should be accepted",
			args: []string{"labelsyncmappings", "mapping-a"},
		},
		{
			name:          "When a label selector is given it should be rejected",
			args:          []string{"labelsyncmappings"},
			labelSelector: "key=value",
			wantErr:       true,
			errContains:   "'--selector' is not supported",
		},
		{
			name:          "When a field selector is given it should be rejected",
			args:          []string{"labelsyncmappings"},
			fieldSelector: "metadata.name=mapping-a",
			wantErr:       true,
			errContains:   "'--field-selector' is not supported",
		},
		{
			name:        "When several names are given it should be rejected",
			args:        []string{"labelsyncmappings", "mapping-a", "mapping-b"},
			wantErr:     true,
			errContains: "only one labelsyncmapping name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			o := DefaultGetOptions()
			o.ConfigFilePath = configFile
			o.LabelSelector = tt.labelSelector
			o.FieldSelector = tt.fieldSelector

			err := o.Validate(tt.args)
			if tt.wantErr {
				require.Error(err)
				require.Contains(err.Error(), tt.errContains)
				return
			}
			require.NoError(err)
		})
	}
}

// newSequencedRecordingClient replies with one canned response per call, so a
// test can drive an apply that falls back from a create to a replace.
func newSequencedRecordingClient(t *testing.T, responses ...func() *http.Response) (*client.Client, *recordingHTTPClient) {
	t.Helper()

	call := 0
	rec := &recordingHTTPClient{}
	rec.response = func() *http.Response {
		require.Less(t, call, len(responses), "the client issued more requests than the test expected")
		next := responses[call]
		call++
		return next()
	}
	apiClient, err := apiclient.NewClientWithResponses("http://example.com", apiclient.WithHTTPClient(rec))
	require.NoError(t, err)

	return client.NewTestClient(apiClient), rec
}

// TestLabelSyncMappingApplyCreates checks that apply posts a mapping that does
// not exist yet and stops there, the same way it does for enrollment hook
// policies.
func TestLabelSyncMappingApplyCreates(t *testing.T) {
	require := require.New(t)

	c, rec := newRecordingClient(t, http.StatusCreated, testLabelSyncMapping("mapping-a"))

	body, err := json.Marshal(testLabelSyncMapping("mapping-a"))
	require.NoError(err)

	result := applyResourceByKind(context.Background(), c, nil, LabelSyncMappingKind, "mapping-a", body)
	require.NoError(result.err)
	require.NotNil(result.httpResponse)
	require.Equal(http.StatusCreated, result.httpResponse.StatusCode)
	require.Nil(result.status, "a successful apply must not report an API status error")

	require.Len(rec.requests, 1, "a create that succeeds must not be followed by a replace")
	require.Equal(http.MethodPost, rec.requests[0].Method)
	require.Equal("/labelsyncmappings", rec.requests[0].URL.Path)
	require.JSONEq(string(body), rec.bodies[0])
}

// TestLabelSyncMappingApplyReplacesOnConflict checks that a create rejected
// with 409 falls back to replacing the named mapping.
func TestLabelSyncMappingApplyReplacesOnConflict(t *testing.T) {
	require := require.New(t)

	c, rec := newSequencedRecordingClient(t,
		func() *http.Response {
			return jsonResponse(t, http.StatusConflict, api.Status{
				Code:    http.StatusConflict,
				Message: "mapping already exists",
			})
		},
		func() *http.Response {
			return jsonResponse(t, http.StatusOK, testLabelSyncMapping("mapping-a"))
		},
	)

	body, err := json.Marshal(testLabelSyncMapping("mapping-a"))
	require.NoError(err)

	result := applyResourceByKind(context.Background(), c, nil, LabelSyncMappingKind, "mapping-a", body)
	require.NoError(result.err)
	require.NotNil(result.httpResponse)
	require.Equal(http.StatusOK, result.httpResponse.StatusCode)
	require.Nil(result.status, "the replace that resolved the conflict must not report an API status error")

	require.Len(rec.requests, 2, "a conflicting create must be followed by a replace")
	require.Equal(http.MethodPost, rec.requests[0].Method)
	require.Equal("/labelsyncmappings", rec.requests[0].URL.Path)
	require.Equal(http.MethodPut, rec.requests[1].Method)
	require.Equal("/labelsyncmappings/mapping-a", rec.requests[1].URL.Path)
	require.JSONEq(string(body), rec.bodies[0])
	require.JSONEq(string(body), rec.bodies[1], "the replace must send the same document as the create")
}

func TestLabelSyncMappingApplyFailureIsReported(t *testing.T) {
	require := require.New(t)

	c, rec := newRecordingClient(t, http.StatusUnprocessableEntity, api.Status{
		Code:    http.StatusUnprocessableEntity,
		Message: "invalid expression",
	})

	body, err := json.Marshal(testLabelSyncMapping("mapping-a"))
	require.NoError(err)

	result := applyResourceByKind(context.Background(), c, nil, LabelSyncMappingKind, "mapping-a", body)
	require.NoError(result.err)
	require.NotNil(result.httpResponse)
	require.Equal(http.StatusUnprocessableEntity, result.httpResponse.StatusCode)
	require.NotNil(result.status)
	require.Equal("invalid expression", result.status.Message)

	require.Len(rec.requests, 1, "a create rejected for any reason other than a conflict must not be retried")
	require.Equal(http.MethodPost, rec.requests[0].Method)
	require.Equal("/labelsyncmappings", rec.requests[0].URL.Path)
}

// TestLabelSyncMappingApplyConflictFailureIsReported checks that a replace that
// fails after a conflicting create still surfaces the API status.
func TestLabelSyncMappingApplyConflictFailureIsReported(t *testing.T) {
	require := require.New(t)

	c, rec := newSequencedRecordingClient(t,
		func() *http.Response {
			return jsonResponse(t, http.StatusConflict, api.Status{
				Code:    http.StatusConflict,
				Message: "mapping already exists",
			})
		},
		func() *http.Response {
			return jsonResponse(t, http.StatusUnprocessableEntity, api.Status{
				Code:    http.StatusUnprocessableEntity,
				Message: "invalid expression",
			})
		},
	)

	body, err := json.Marshal(testLabelSyncMapping("mapping-a"))
	require.NoError(err)

	result := applyResourceByKind(context.Background(), c, nil, LabelSyncMappingKind, "mapping-a", body)
	require.NoError(result.err)
	require.NotNil(result.httpResponse)
	require.Equal(http.StatusUnprocessableEntity, result.httpResponse.StatusCode)
	require.NotNil(result.status)
	require.Equal("invalid expression", result.status.Message)

	require.Len(rec.requests, 2)
	require.Equal(http.MethodPut, rec.requests[1].Method)
}

func TestLabelSyncMappingEditIsSupported(t *testing.T) {
	require := require.New(t)

	require.True(slices.Contains(getValidEditResourceKinds(), LabelSyncMappingKind))

	o := DefaultEditOptions()
	o.ConfigFilePath = writeMinimalClientConfig(t)
	require.NoError(o.Validate([]string{"labelsyncmapping", "mapping-a"}))
	require.NoError(o.Validate([]string{"labelsyncmapping/mapping-a"}))
}

func TestLabelSyncMappingEditPatchDispatch(t *testing.T) {
	require := require.New(t)

	rec := &recordingHTTPClient{
		response: func() *http.Response {
			return jsonResponse(t, http.StatusOK, testLabelSyncMapping("mapping-a"))
		},
	}
	apiClient, err := apiclient.NewClientWithResponses("http://example.com", apiclient.WithHTTPClient(rec))
	require.NoError(err)

	o := DefaultEditOptions()
	patch := []byte(`[{"op":"replace","path":"/spec/expression","value":"device.metadata.owner"}]`)

	httpResponse, _, err := o.executePatchOperation(context.Background(), apiClient, LabelSyncMappingKind, "mapping-a", patch)
	require.NoError(err)
	require.NotNil(httpResponse)
	require.Equal(http.StatusOK, httpResponse.StatusCode)

	require.Len(rec.requests, 1)
	require.Equal(http.MethodPatch, rec.requests[0].Method)
	require.Equal("/labelsyncmappings/mapping-a", rec.requests[0].URL.Path)
	require.Equal("application/json-patch+json", rec.requests[0].Header.Get("Content-Type"))
	require.JSONEq(string(patch), rec.bodies[0])
}

func TestLabelSyncMappingDelete(t *testing.T) {
	require := require.New(t)

	c, rec := newRecordingClient(t, http.StatusOK, api.Status{Code: http.StatusOK, Message: "deleted"})

	o := DefaultDeleteOptions()
	response, err := o.deleteOne(context.Background(), c, LabelSyncMappingKind, "mapping-a")
	require.NoError(err)

	deleteResponse, ok := response.(*apiclient.DeleteLabelSyncMappingResponse)
	require.True(ok, "expected a DeleteLabelSyncMappingResponse, got %T", response)
	require.Equal(http.StatusOK, deleteResponse.StatusCode())
	require.NoError(processDeletionReponse(response, nil, LabelSyncMappingKind, "mapping-a"))

	require.Len(rec.requests, 1)
	require.Equal(http.MethodDelete, rec.requests[0].Method)
	require.Equal("/labelsyncmappings/mapping-a", rec.requests[0].URL.Path)
}

func TestLabelSyncMappingDeleteValidate(t *testing.T) {
	configFile := writeMinimalClientConfig(t)

	tests := []struct {
		name        string
		args        []string
		wantErr     bool
		errContains string
	}{
		{
			name: "When a name is supplied it should be accepted",
			args: []string{"labelsyncmapping", "mapping-a"},
		},
		{
			name: "When TYPE/NAME syntax is used it should be accepted",
			args: []string{"labelsyncmapping/mapping-a"},
		},
		{
			name:        "When no name is supplied it should be rejected",
			args:        []string{"labelsyncmappings"},
			wantErr:     true,
			errContains: "name must be specified",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			o := DefaultDeleteOptions()
			o.ConfigFilePath = configFile

			err := o.Validate(tt.args)
			if tt.wantErr {
				require.Error(err)
				require.Contains(err.Error(), tt.errContains)
				return
			}
			require.NoError(err)
		})
	}
}
