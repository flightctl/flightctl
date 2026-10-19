package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	deltagenerate "github.com/flightctl/flightctl/internal/delta_worker/tasks/generate"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

type ociDeltaArtifactChecker struct {
	timeout      time.Duration
	repositories repositoryservice.Service
	cfg          *deltaconfig.DeltaGenerationConfig
}

func newOCIDeltaArtifactChecker(
	timeout time.Duration,
	repositories repositoryservice.Service,
	cfg *deltaconfig.DeltaGenerationConfig,
) (*ociDeltaArtifactChecker, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("delta artifact check timeout must be greater than zero")
	}
	return &ociDeltaArtifactChecker{
		timeout:      timeout,
		repositories: repositories,
		cfg:          cfg,
	}, nil
}

func (c *ociDeltaArtifactChecker) Check(ctx context.Context, generation model.DeltaGeneration) (artifactAvailability, error) {
	if generation.DeltaRef == nil || *generation.DeltaRef == "" {
		return artifactMissing, nil
	}
	artifactRef, err := parseDeltaArtifactRef(*generation.DeltaRef)
	if err != nil {
		return artifactMissing, nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	spec, err := deltagenerate.ResolveDeltaTargetRepo(checkCtx, c.repositories, c.cfg, generation.OrgID)
	if err != nil {
		return artifactUnknown, fmt.Errorf("resolve delta artifact repository: %w", err)
	}
	artifactPath := artifactRef.Registry + "/" + artifactRef.Repository
	repository, err := oci.BuildOciRepoRef(checkCtx, oci.SpecForRegistry(artifactRef.Registry, spec), artifactPath)
	if err != nil {
		return artifactUnknown, fmt.Errorf("configure delta artifact repository: %w", err)
	}
	deltagenerate.ConfigureDeltaRepository(repository)

	_, manifestReader, err := repository.FetchReference(checkCtx, artifactRef.Reference)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) {
			return artifactMissing, nil
		}
		if availability := classifyArtifactError(err, spec, artifactRef.Registry); availability != artifactUnknown {
			return availability, nil
		}
		return artifactUnknown, fmt.Errorf("fetch delta artifact %s: %w", *generation.DeltaRef, err)
	}
	manifestBytes, readErr := io.ReadAll(io.LimitReader(manifestReader, deltagenerate.MaxRegistryResponseBytes+1))
	closeErr := manifestReader.Close()
	if readErr != nil {
		return artifactUnknown, fmt.Errorf("read delta artifact %s: %w", *generation.DeltaRef, readErr)
	}
	if closeErr != nil {
		return artifactUnknown, fmt.Errorf("close delta artifact response: %w", closeErr)
	}
	if len(manifestBytes) > deltagenerate.MaxRegistryResponseBytes {
		return artifactMissing, nil
	}

	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return artifactMissing, nil
	}
	sourceDigest, err := digest.Parse(generation.SourceDigest)
	if err != nil {
		return artifactMissing, nil
	}
	targetDigest, err := digest.Parse(generation.TargetDigest)
	if err != nil {
		return artifactMissing, nil
	}
	storedDigest, err := artifactRef.Digest()
	if err != nil {
		return artifactUnknown, fmt.Errorf("parse delta artifact digest: %w", err)
	}
	if err := deltagenerate.ValidateDeltaManifestPair(manifest, sourceDigest, targetDigest); err != nil {
		return artifactMissing, nil
	}
	referrer, err := deltagenerate.FindExactDeltaReferrer(checkCtx, repository, sourceDigest, targetDigest, storedDigest)
	if err != nil {
		if availability := classifyArtifactError(err, spec, artifactRef.Registry); availability != artifactUnknown {
			return availability, nil
		}
		return artifactUnknown, fmt.Errorf("list delta artifact referrers for %s: %w", generation.TargetDigest, err)
	}
	if referrer == nil {
		return artifactMissing, nil
	}
	return artifactAvailable, nil
}

func classifyArtifactError(err error, spec *domain.OciRepoSpec, registry string) artifactAvailability {
	var response *errcode.ErrorResponse
	if !errors.As(err, &response) {
		return artifactUnknown
	}
	switch response.StatusCode {
	case http.StatusNotFound:
		return artifactMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		if oci.SpecHasRegistryCredentials(spec, registry) {
			return artifactAccessDenied
		}
	}
	return artifactUnknown
}

func parseDeltaArtifactRef(ref string) (registry.Reference, error) {
	parsed, err := registry.ParseReference(ref)
	if err != nil {
		return registry.Reference{}, fmt.Errorf("parse delta artifact reference: %w", err)
	}
	if parsed.Reference == "" {
		return registry.Reference{}, fmt.Errorf("delta artifact reference %q has no digest", ref)
	}
	if _, err := digest.Parse(parsed.Reference); err != nil {
		return registry.Reference{}, fmt.Errorf("parse delta artifact digest: %w", err)
	}
	return parsed, nil
}
