package kubeflowmodelregistrysource

import (
	"context"
	"fmt"

	mrapi "github.com/kubeflow/hub/pkg/openapi"
)

const liveStateFilter = "state='LIVE'"

// httpError wraps an upstream error with the HTTP status code returned by the
// Model Registry SDK. This lets wrapHTTPError include the status code in
// sanitized log messages without changing the registryClient interface.
type httpError struct {
	statusCode int
	err        error
}

// Error returns a status-only message as defense in depth. The wrapped SDK
// error (available through Unwrap) may contain upstream response body text
// that should not appear in operator-facing logs or metrics.
func (e *httpError) Error() string {
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
	// ListRegisteredModels returns one page of LIVE RegisteredModels ordered
	// by ID ascending.
	ListRegisteredModels(
		ctx context.Context,
		nextPageToken string,
	) (*mrapi.RegisteredModelList, error)

	// ListModelVersions returns one page of LIVE ModelVersions belonging to
	// the specified RegisteredModel, ordered by ID ascending.
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
}

// openapiClient adapts the generated Kubeflow Hub Model Registry client to
// registryClient.
//
// It applies the configured page size, deterministic ID ordering, LIVE
// model/version filters, and model-artifact type filter on every page.
type openapiClient struct {
	api      *mrapi.ModelRegistryServiceAPIService
	pageSize string
}

var _ registryClient = (*openapiClient)(nil)

func (c *openapiClient) ListRegisteredModels(
	ctx context.Context,
	nextPageToken string,
) (*mrapi.RegisteredModelList, error) {
	request := c.api.GetRegisteredModels(ctx).
		PageSize(c.pageSize).
		OrderBy(mrapi.ORDERBYFIELD_ID).
		SortOrder(mrapi.SORTORDER_ASC).
		FilterQuery(liveStateFilter)

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
		SortOrder(mrapi.SORTORDER_ASC).
		FilterQuery(liveStateFilter)

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
