package kubeflowmodelregistrysource

import (
	"context"
	"fmt"

	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

// httpError wraps an upstream error with the HTTP status code returned by the
// Model Registry SDK. This lets wrapHTTPError include the status code in
// diagnostic log messages without changing the registryClient interface.
type httpError struct {
	statusCode int
	err        error
}

// Error returns the HTTP status code and the wrapped error's diagnostic text.
// Registry URLs, filter expressions, and upstream response bodies are useful
// operator diagnostics; only configured authentication credentials (tokens,
// passwords) must be excluded — and those are never present in this error chain.
func (e *httpError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("model registry HTTP %d: %s", e.statusCode, e.err.Error())
	}
	return fmt.Sprintf("model registry HTTP %d", e.statusCode)
}

func (e *httpError) Unwrap() error {
	return e.err
}

// registryClient abstracts the Model Registry v1alpha3 operations required by
// the source.
//
// Keeping the generated SDK behind this interface isolates collection and
// mapping logic from generated request builders and upstream package changes.
//
// Each method retrieves exactly one page. The source owns pagination,
// repeated-token detection, complete-cycle failure handling, and sanitized
// error reporting.
type registryClient interface {
	// ListRegisteredModels returns one page of filtered RegisteredModels
	// ordered by ID ascending.
	ListRegisteredModels(
		ctx context.Context,
		nextPageToken string,
	) (*mrapi.RegisteredModelList, error)

	// ListModelVersions returns one page of filtered ModelVersions belonging
	// to the specified RegisteredModel, ordered by ID ascending.
	ListModelVersions(
		ctx context.Context,
		modelID string,
		nextPageToken string,
	) (*mrapi.ModelVersionList, error)

	// ListModelArtifacts returns one page of model-artifact resources belonging
	// to the specified ModelVersion, ordered by ID ascending.
	//
	// Artifact lifecycle state is intentionally not filtered server-side.
	// Mapping applies the validated LIVE, UNKNOWN, and absent-state policy.
	ListModelArtifacts(
		ctx context.Context,
		versionID string,
		nextPageToken string,
	) (*mrapi.ArtifactList, error)

	// PreflightRegisteredModels validates that the configured model filter
	// is accepted by the registered-models endpoint.
	PreflightRegisteredModels(ctx context.Context) error

	// PreflightModelVersions validates that the configured version filter
	// is accepted by the model-versions endpoint.
	PreflightModelVersions(ctx context.Context) error
}

// openapiClient adapts the generated Kubeflow Hub Model Registry client to
// registryClient.
//
// It applies the configured page size, deterministic ID ordering, configurable
// model/version filters, and model-artifact type filter on every page.
type openapiClient struct {
	api           *mrapi.ModelRegistryServiceAPIService
	pageSize      string
	modelFilter   string // effective model filter; "" means omit filterQuery
	versionFilter string // effective version filter; "" means omit filterQuery
}

var _ registryClient = (*openapiClient)(nil)

func (c *openapiClient) ListRegisteredModels(
	ctx context.Context,
	nextPageToken string,
) (*mrapi.RegisteredModelList, error) {
	request := c.api.GetRegisteredModels(ctx).
		PageSize(c.pageSize).
		OrderBy(mrapi.ORDERBYFIELD_ID).
		SortOrder(mrapi.SORTORDER_ASC)

	if c.modelFilter != "" {
		request = request.FilterQuery(c.modelFilter)
	}

	if nextPageToken != "" {
		request = request.NextPageToken(nextPageToken)
	}

	list, resp, err := request.Execute()
	if err != nil && resp != nil {
		return list, &httpError{statusCode: resp.StatusCode, err: err}
	}
	return list, err
}

func (c *openapiClient) ListModelVersions(
	ctx context.Context,
	modelID string,
	nextPageToken string,
) (*mrapi.ModelVersionList, error) {
	request := c.api.GetRegisteredModelVersions(ctx, modelID).
		PageSize(c.pageSize).
		OrderBy(mrapi.ORDERBYFIELD_ID).
		SortOrder(mrapi.SORTORDER_ASC)

	if c.versionFilter != "" {
		request = request.FilterQuery(c.versionFilter)
	}

	if nextPageToken != "" {
		request = request.NextPageToken(nextPageToken)
	}

	list, resp, err := request.Execute()
	if err != nil && resp != nil {
		return list, &httpError{statusCode: resp.StatusCode, err: err}
	}
	return list, err
}

func (c *openapiClient) ListModelArtifacts(
	ctx context.Context,
	versionID string,
	nextPageToken string,
) (*mrapi.ArtifactList, error) {
	request := c.api.GetModelVersionArtifacts(ctx, versionID).
		PageSize(c.pageSize).
		OrderBy(mrapi.ORDERBYFIELD_ID).
		SortOrder(mrapi.SORTORDER_ASC).
		ArtifactType(
			mrapi.ARTIFACTTYPEQUERYPARAM_MODEL_ARTIFACT,
		)

	if nextPageToken != "" {
		request = request.NextPageToken(nextPageToken)
	}

	list, resp, err := request.Execute()
	if err != nil && resp != nil {
		return list, &httpError{statusCode: resp.StatusCode, err: err}
	}
	return list, err
}

func (c *openapiClient) PreflightRegisteredModels(ctx context.Context) error {
	request := c.api.GetRegisteredModels(ctx).
		PageSize("1").
		OrderBy(mrapi.ORDERBYFIELD_ID).
		SortOrder(mrapi.SORTORDER_ASC)

	if c.modelFilter != "" {
		request = request.FilterQuery(c.modelFilter)
	}

	list, resp, err := request.Execute()
	if err != nil && resp != nil {
		return &httpError{statusCode: resp.StatusCode, err: err}
	}
	if err != nil {
		return err
	}
	if list == nil || list.Items == nil {
		return fmt.Errorf("preflight registered models: response missing required items field")
	}
	return nil
}

func (c *openapiClient) PreflightModelVersions(ctx context.Context) error {
	request := c.api.GetModelVersions(ctx).
		PageSize("1").
		OrderBy(mrapi.ORDERBYFIELD_ID).
		SortOrder(mrapi.SORTORDER_ASC)

	if c.versionFilter != "" {
		request = request.FilterQuery(c.versionFilter)
	}

	list, resp, err := request.Execute()
	if err != nil && resp != nil {
		return &httpError{statusCode: resp.StatusCode, err: err}
	}
	if err != nil {
		return err
	}
	if list == nil || list.Items == nil {
		return fmt.Errorf("preflight model versions: response missing required items field")
	}
	return nil
}
