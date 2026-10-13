package kubeflowmodelregistrysource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	apiv1alpha1 "github.com/flightctl/flightctl/api/core/v1alpha1"
	catalogcollector "github.com/flightctl/flightctl/pkg/catalogcollector"
	"github.com/flightctl/flightctl/pkg/catalogcollector/source/pollsource"
	mrapi "github.com/kubeflow/hub/pkg/openapi"
	"github.com/sirupsen/logrus"
)

// source implements catalogcollector.Source for the Kubeflow Model Registry.
//
// The component ID is not stored here: it is already bound to the
// source-scoped logger and the polling helper, so a duplicate copy would only
// risk drifting from them. Source metrics are likewise not stored: the polling
// helper owns the OnCollect callback that records them, and it keeps the
// metrics instance alive for the source lifetime.
type source struct {
	catalog           string
	collectionTimeout time.Duration
	client            registryClient
	poller            *pollsource.Helper
	next              catalogcollector.Consumer
	log               *logrus.Entry
}

var _ catalogcollector.Source = (*source)(nil)
var _ catalogcollector.SourcePreflight = (*source)(nil)

// Preflight validates that the Model Registry is reachable and that the
// configured selection filters are accepted by the server.
func (s *source) Preflight(ctx context.Context) error {
	if err := s.client.PreflightRegisteredModels(ctx); err != nil {
		return s.wrapHTTPError(
			"preflight: validating model filter against registered models endpoint",
			err,
		)
	}

	if err := s.client.PreflightModelVersions(ctx); err != nil {
		return s.wrapHTTPError(
			"preflight: validating version filter against model versions endpoint",
			err,
		)
	}

	return nil
}

// Run blocks until the context is cancelled or the polling helper encounters a
// fatal configuration error.
//
// Normal shutdown (context cancellation by the caller) returns nil so the
// service layer does not log an expected cancellation as an error. Genuine
// poller and configuration errors are preserved.
func (s *source) Run(ctx context.Context) error {
	s.log.Info("source started")
	defer s.log.Info("source stopped")

	err := s.poller.Run(ctx, s.collect, s.next)
	if err != nil && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}
	return err
}

// collect performs one complete collection cycle:
//
//  1. Fetch every page of filtered RegisteredModels.
//  2. Fetch every page of filtered ModelVersions for each model.
//  3. Fetch every page of model-artifact resources for each version.
//  4. Select exactly one eligible immutable artifact per filtered version.
//  5. Normalize the complete result and produce a deterministic revision.
//
// Any error aborts the cycle. No partial snapshot is returned or delivered.
func (s *source) collect(
	parentCtx context.Context,
) (*catalogcollector.CatalogSnapshot, error) {
	ctx, cancel := context.WithTimeout(
		parentCtx,
		s.collectionTimeout,
	)
	defer cancel()

	models, err := s.fetchAllModels(ctx)
	if err != nil {
		return nil, err
	}

	collected := make([]collectedModel, 0, len(models))
	skippedModels := 0

	for _, model := range models {
		if model.Id == nil || *model.Id == "" {
			return nil, fmt.Errorf(
				"registered model name=%q has no id in the Model Registry response",
				model.Name,
			)
		}

		collectedModel, err := s.fetchModel(ctx, model)
		if err != nil {
			return nil, err
		}
		if collectedModel == nil {
			skippedModels++
			continue
		}

		collected = append(collected, *collectedModel)
	}

	catalogs, items, err := toSnapshot(s.catalog, collected)
	if err != nil {
		return nil, fmt.Errorf(
			"mapping Model Registry snapshot: %w",
			err,
		)
	}

	revision, err := computeRevision(catalogs, items)
	if err != nil {
		return nil, fmt.Errorf(
			"computing snapshot revision: %w",
			err,
		)
	}

	s.log.WithFields(logrus.Fields{
		"eligible_model_count": len(items),
		"skipped_model_count":  skippedModels,
		"revision":             revision,
	}).Debug("collection complete")

	return &catalogcollector.CatalogSnapshot{
		Revision:     revision,
		Catalogs:     catalogs,
		CatalogItems: items,
	}, nil
}

// fetchAllModels retrieves every page of filtered RegisteredModels.
func (s *source) fetchAllModels(
	ctx context.Context,
) ([]mrapi.RegisteredModel, error) {
	models := make([]mrapi.RegisteredModel, 0)
	nextPageToken := ""
	seenTokens := make(map[string]struct{})

	for {
		list, err := s.client.ListRegisteredModels(
			ctx,
			nextPageToken,
		)
		if err != nil {
			return nil, s.wrapHTTPError(
				"listing registered models",
				err,
			)
		}
		if list == nil {
			return nil, fmt.Errorf(
				"listing registered models returned no response",
			)
		}
		if list.Items == nil {
			return nil, fmt.Errorf(
				"listing registered models: response missing required items field",
			)
		}

		models = append(models, list.Items...)

		next := list.NextPageToken
		if next == "" {
			return models, nil
		}
		if _, found := seenTokens[next]; found {
			return nil, fmt.Errorf(
				"pagination loop while listing registered models: " +
					"the server repeated a nextPageToken",
			)
		}

		seenTokens[next] = struct{}{}
		nextPageToken = next
	}
}

// fetchModel retrieves every LIVE version and its eligible artifact for one
// RegisteredModel.
//
// A model with no LIVE versions is omitted from the snapshot. Because versions
// are filtered server-side, this covers both models with no versions and
// models whose versions are all archived.
func (s *source) fetchModel(
	ctx context.Context,
	model mrapi.RegisteredModel,
) (*collectedModel, error) {
	modelID := safeID(model.Id)

	versions, err := s.fetchAllVersions(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if len(versions) == 0 {
		return nil, nil
	}

	collectedVersions := make(
		[]collectedVersion,
		0,
		len(versions),
	)

	for _, version := range versions {
		if version.Id == nil || *version.Id == "" {
			return nil, fmt.Errorf(
				"registered model id=%s name=%q: "+
					"model version name=%q has no id in the "+
					"Model Registry response",
				modelID,
				model.Name,
				version.Name,
			)
		}

		collectedVersion, err := s.fetchVersion(
			ctx,
			modelID,
			model.Name,
			version,
		)
		if err != nil {
			return nil, err
		}

		collectedVersions = append(
			collectedVersions,
			*collectedVersion,
		)
	}

	return &collectedModel{
		model:    model,
		versions: collectedVersions,
	}, nil
}

// fetchAllVersions retrieves every page of LIVE ModelVersions belonging to one
// RegisteredModel.
func (s *source) fetchAllVersions(
	ctx context.Context,
	modelID string,
) ([]mrapi.ModelVersion, error) {
	versions := make([]mrapi.ModelVersion, 0)
	nextPageToken := ""
	seenTokens := make(map[string]struct{})

	for {
		list, err := s.client.ListModelVersions(
			ctx,
			modelID,
			nextPageToken,
		)
		if err != nil {
			return nil, s.wrapHTTPError(
				fmt.Sprintf(
					"listing versions for registered model id=%s",
					modelID,
				),
				err,
			)
		}
		if list == nil {
			return nil, fmt.Errorf(
				"listing versions for registered model id=%s "+
					"returned no response",
				modelID,
			)
		}

		if list.Items == nil {
			return nil, fmt.Errorf(
				"listing versions for registered model id=%s: "+
					"response missing required items field",
				modelID,
			)
		}

		versions = append(versions, list.Items...)

		next := list.NextPageToken
		if next == "" {
			return versions, nil
		}
		if _, found := seenTokens[next]; found {
			return nil, fmt.Errorf(
				"pagination loop while listing versions for "+
					"registered model id=%s: the server repeated "+
					"a nextPageToken",
				modelID,
			)
		}

		seenTokens[next] = struct{}{}
		nextPageToken = next
	}
}

// fetchVersion retrieves and validates artifacts for one LIVE ModelVersion.
//
// Exactly one artifact must satisfy all eligibility requirements. No eligible
// artifact makes the version undeployable, while multiple eligible artifacts
// make the mapping ambiguous; both conditions fail the complete cycle.
//
// An artifact the SDK decoded into no oneOf variant also fails the complete
// cycle. Such a record carries no readable content, so it cannot be
// classified as a non-model artifact and excluded: treating it as one would
// publish a snapshot that silently omits registry data. Recognized non-model
// artifacts continue to be excluded normally.
func (s *source) fetchVersion(
	ctx context.Context,
	modelID string,
	modelName string,
	version mrapi.ModelVersion,
) (*collectedVersion, error) {
	versionID := safeID(version.Id)

	artifacts, err := s.fetchAllArtifacts(ctx, versionID)
	if err != nil {
		return nil, err
	}

	type artifactCandidate struct {
		repository string
		digest     string
	}

	candidates := make([]artifactCandidate, 0, 1)

	for i := range artifacts {
		modelArtifact, ok := extractModelArtifact(artifacts[i])
		if !ok {
			if !hasDecodedVariant(artifacts[i]) {
				return nil, fmt.Errorf(
					"registered model id=%s name=%q, "+
						"version id=%s name=%q: artifact at index %d "+
						"decoded into no known artifact type; its "+
						"\"artifactType\" discriminator is missing or "+
						"unrecognized, so the record cannot be read or "+
						"safely excluded",
					modelID,
					modelName,
					versionID,
					version.Name,
					i,
				)
			}
			continue
		}

		repository, digest, eligible, err := isEligibleArtifact(
			modelArtifact,
		)
		if err != nil {
			return nil, fmt.Errorf(
				"registered model id=%s name=%q, "+
					"version id=%s name=%q: %w",
				modelID,
				modelName,
				versionID,
				version.Name,
				err,
			)
		}

		if eligible {
			candidates = append(candidates, artifactCandidate{
				repository: repository,
				digest:     digest,
			})
			continue
		}

		if modelArtifact.State != nil {
			s.log.WithFields(logrus.Fields{
				"model_id":       modelID,
				"version_id":     versionID,
				"artifact_state": string(*modelArtifact.State),
			}).Debug("artifact excluded by lifecycle policy")
		}
	}

	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf(
			"registered model id=%s name=%q, "+
				"version id=%s name=%q: no eligible model-artifact "+
				"found; exactly one model-artifact with an immutable "+
				"OCI sha256 digest and lifecycle state LIVE, UNKNOWN, "+
				"or absent is required",
			modelID,
			modelName,
			versionID,
			version.Name,
		)

	case 1:
		return &collectedVersion{
			version:    version,
			repository: candidates[0].repository,
			digest:     candidates[0].digest,
		}, nil

	default:
		return nil, fmt.Errorf(
			"registered model id=%s name=%q, "+
				"version id=%s name=%q: found %d eligible "+
				"model-artifacts but exactly one is required; "+
				"resolve the ambiguity in the Model Registry",
			modelID,
			modelName,
			versionID,
			version.Name,
			len(candidates),
		)
	}
}

// fetchAllArtifacts retrieves every page of model-artifact resources belonging
// to one ModelVersion.
func (s *source) fetchAllArtifacts(
	ctx context.Context,
	versionID string,
) ([]mrapi.Artifact, error) {
	artifacts := make([]mrapi.Artifact, 0)
	nextPageToken := ""
	seenTokens := make(map[string]struct{})

	for {
		list, err := s.client.ListModelArtifacts(
			ctx,
			versionID,
			nextPageToken,
		)
		if err != nil {
			return nil, s.wrapHTTPError(
				fmt.Sprintf(
					"listing artifacts for model version id=%s",
					versionID,
				),
				err,
			)
		}
		if list == nil {
			return nil, fmt.Errorf(
				"listing artifacts for model version id=%s "+
					"returned no response",
				versionID,
			)
		}

		if list.Items == nil {
			return nil, fmt.Errorf(
				"listing artifacts for model version id=%s: "+
					"response missing required items field",
				versionID,
			)
		}

		artifacts = append(artifacts, list.Items...)

		next := list.NextPageToken
		if next == "" {
			return artifacts, nil
		}
		if _, found := seenTokens[next]; found {
			return nil, fmt.Errorf(
				"pagination loop while listing artifacts for "+
					"model version id=%s: the server repeated "+
					"a nextPageToken",
				versionID,
			)
		}

		seenTokens[next] = struct{}{}
		nextPageToken = next
	}
}

// extractModelArtifact unwraps the generated polymorphic Artifact value.
func extractModelArtifact(
	artifact mrapi.Artifact,
) (*mrapi.ModelArtifact, bool) {
	if artifact.ModelArtifact == nil {
		return nil, false
	}

	return artifact.ModelArtifact, true
}

// hasDecodedVariant reports whether the generated Artifact.UnmarshalJSON
// matched one of the oneOf variants.
//
// Artifact is keyed on the "artifactType" discriminator. When that field is
// absent or carries a value the SDK does not know, UnmarshalJSON matches no
// variant, returns a nil error, and leaves every variant pointer nil. The
// resulting value is indistinguishable from an empty Artifact, so the caller
// must treat it as undecodable rather than as a recognized non-model
// artifact.
func hasDecodedVariant(artifact mrapi.Artifact) bool {
	return artifact.ModelArtifact != nil ||
		artifact.DocArtifact != nil ||
		artifact.DataSet != nil ||
		artifact.Metric != nil ||
		artifact.Parameter != nil
}

// wrapHTTPError adds operation context and includes the Model Registry response
// body when supplied by the SDK. Wrapped errors retain their identity.
func (s *source) wrapHTTPError(
	operation string,
	err error,
) error {
	if err == nil {
		return nil
	}

	var openAPIErr *mrapi.GenericOpenAPIError
	if errors.As(err, &openAPIErr) && len(openAPIErr.Body()) > 0 {
		return fmt.Errorf(
			"%s: %w; response body: %s",
			operation,
			err,
			openAPIErr.Body(),
		)
	}

	return fmt.Errorf("%s: %w", operation, err)
}

// computeRevision returns a stable, abbreviated SHA-256 hash of the complete
// desired snapshot.
//
// Copies are sorted before marshaling so hashing never mutates the snapshot
// returned to the pipeline. Complete resources, including Catalog identity,
// are hashed. Including Catalog identity matters when the registry contains no
// CatalogItems.
func computeRevision(
	catalogs []apiv1alpha1.Catalog,
	items []apiv1alpha1.CatalogItem,
) (string, error) {
	sortedCatalogs := append(
		[]apiv1alpha1.Catalog(nil),
		catalogs...,
	)
	sortedItems := append(
		[]apiv1alpha1.CatalogItem(nil),
		items...,
	)

	sort.Slice(sortedCatalogs, func(i, j int) bool {
		return ptrStr(sortedCatalogs[i].Metadata.Name) <
			ptrStr(sortedCatalogs[j].Metadata.Name)
	})

	sort.Slice(sortedItems, func(i, j int) bool {
		left := sortedItems[i].Metadata.Catalog +
			"/" +
			ptrStr(sortedItems[i].Metadata.Name)
		right := sortedItems[j].Metadata.Catalog +
			"/" +
			ptrStr(sortedItems[j].Metadata.Name)

		return left < right
	})

	canonical := struct {
		Catalogs     []apiv1alpha1.Catalog     `json:"catalogs"`
		CatalogItems []apiv1alpha1.CatalogItem `json:"catalogItems"`
	}{
		Catalogs:     sortedCatalogs,
		CatalogItems: sortedItems,
	}

	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf(
			"marshaling canonical snapshot: %w",
			err,
		)
	}

	sum := sha256.Sum256(data)

	// Preserve the existing 16-character revision representation while deriving
	// it from the complete canonical snapshot.
	return hex.EncodeToString(sum[:])[:16], nil
}
