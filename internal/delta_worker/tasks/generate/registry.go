package generate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	ociDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	ociDeltaSourceAnnotation = "io.github.containers.delta.source"
	errCodeManifestInvalid   = "MANIFEST_INVALID"
	errCodeManifestUnknown   = "MANIFEST_UNKNOWN"
	maxRegistryResponseBytes = 10 << 20
	maxReferrerPages         = 100
)

func checkExistingDelta(ctx context.Context, deltaRepository, sourceDigest, targetDigest string, spec *domain.OciRepoSpec) (*existingDelta, error) {
	if spec == nil {
		return nil, fmt.Errorf("OCI write target is required for existence check")
	}
	target, err := digest.Parse(targetDigest)
	if err != nil {
		return nil, fmt.Errorf("existence check: invalid target digest: %w", err)
	}
	source, err := digest.Parse(sourceDigest)
	if err != nil {
		return nil, fmt.Errorf("existence check: invalid source digest: %w", err)
	}
	repo, err := oci.BuildOciRepoRef(ctx, spec, deltaRepository)
	if err != nil {
		return nil, fmt.Errorf("existence check: configure repository: %w", err)
	}
	repo.MaxMetadataBytes = maxRegistryResponseBytes
	repo.ReferrerListMaxPages = maxReferrerPages
	repo.Client = referrersMissingSubjectClient{base: repo.Client}
	existence, err := checkDeltaExists(ctx, repo, deltaRepository, source, target)
	if err != nil {
		return nil, fmt.Errorf("existence check: %w", err)
	}
	return existence, nil
}

func checkDeltaExists(ctx context.Context, repo registry.Repository, deltaRepository string, sourceDigest, targetDigest digest.Digest) (*existingDelta, error) {
	var match *ocispec.Descriptor
	err := repo.Referrers(ctx, ocispec.Descriptor{Digest: targetDigest}, ociDeltaArtifactType, func(referrers []ocispec.Descriptor) error {
		if match != nil {
			return nil
		}
		if referrer, ok := matchingDeltaDescriptor(referrers, sourceDigest.String()); ok {
			match = &referrer
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list referrers: %w", err)
	}
	if match == nil {
		return nil, nil
	}
	if match.Size < 0 || match.Size > maxRegistryResponseBytes {
		return nil, fmt.Errorf("delta manifest size %d exceeds %d bytes", match.Size, maxRegistryResponseBytes)
	}
	manifestBytes, err := content.FetchAll(ctx, repo, *match)
	if err != nil {
		return nil, fmt.Errorf("fetch delta manifest: %w", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("invalid delta manifest: %w", err)
	}
	if err := validateDeltaManifest(manifest); err != nil {
		return nil, err
	}
	if manifest.Subject == nil || manifest.Subject.Digest != targetDigest {
		return nil, fmt.Errorf("delta subject does not match target %s", targetDigest)
	}
	if manifest.Annotations[ociDeltaSourceAnnotation] != sourceDigest.String() {
		return nil, fmt.Errorf("delta source does not match source %s", sourceDigest)
	}
	return &existingDelta{
		Ref:       deltaRepository + "@" + match.Digest.String(),
		SizeBytes: deltaPayloadSize(manifest),
	}, nil
}

func deltaPayloadSize(manifest ocispec.Manifest) int64 {
	size := manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return size
}

func validateDeltaManifest(manifest ocispec.Manifest) error {
	if manifest.ArtifactType != ociDeltaArtifactType {
		return fmt.Errorf("unexpected artifact type %q", manifest.ArtifactType)
	}
	if manifest.Subject == nil || manifest.Subject.Digest == "" {
		return fmt.Errorf("delta manifest missing subject")
	}
	if manifest.Config.Digest == "" || manifest.Config.Size < 0 {
		return fmt.Errorf("delta manifest has invalid config descriptor")
	}
	for i, layer := range manifest.Layers {
		if layer.Digest == "" || layer.Size < 0 {
			return fmt.Errorf("delta manifest has invalid layer descriptor %d", i)
		}
	}
	if manifest.Annotations[ociDeltaSourceAnnotation] == "" {
		return fmt.Errorf("delta manifest missing %s annotation", ociDeltaSourceAnnotation)
	}
	return nil
}

func matchingDeltaDescriptor(referrers []ocispec.Descriptor, sourceDigest string) (ocispec.Descriptor, bool) {
	for _, referrer := range referrers {
		if referrer.ArtifactType != ociDeltaArtifactType {
			continue
		}
		if referrer.Annotations[ociDeltaSourceAnnotation] != sourceDigest || referrer.Digest == "" {
			continue
		}
		return referrer, true
	}
	return ocispec.Descriptor{}, false
}

// referrersMissingSubjectClient rewrites a referrers GET that reports
// MANIFEST_INVALID into MANIFEST_UNKNOWN. Quay returns 400 for that lookup
// when the subject manifest is not stored in the repository. oras-go falls
// back to the referrers tag schema only after a not-found response, so a new
// delta repository otherwise retries the existence check indefinitely.
type referrersMissingSubjectClient struct {
	base remote.Client
}

func (c referrersMissingSubjectClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.base.Do(req)
	if err != nil || resp == nil || resp.Body == nil || !isReferrersGet(req) || resp.StatusCode != http.StatusBadRequest {
		return resp, err
	}
	prefix, readErr := io.ReadAll(io.LimitReader(resp.Body, maxRegistryResponseBytes+1))
	if readErr != nil || len(prefix) > maxRegistryResponseBytes || !manifestInvalidOnly(prefix) {
		original := resp.Body
		resp.Body = prefixedBody{
			Reader: io.MultiReader(bytes.NewReader(prefix), original),
			Closer: original,
		}
		return resp, nil
	}
	if err := resp.Body.Close(); err != nil {
		return nil, err
	}
	unknown := []byte(`{"errors":[{"code":"` + errCodeManifestUnknown + `","message":"manifest unknown"}]}`)
	resp.StatusCode = http.StatusNotFound
	resp.Status = fmt.Sprintf("%d %s", http.StatusNotFound, http.StatusText(http.StatusNotFound))
	resp.ContentLength = int64(len(unknown))
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(unknown)))
	resp.Body = io.NopCloser(bytes.NewReader(unknown))
	return resp, nil
}

// prefixedBody returns bytes already read from an HTTP response, then the unread remainder.
type prefixedBody struct {
	io.Reader
	io.Closer
}

// isReferrersGet reports whether req is a GET of the OCI Referrers API,
// /v2/<repository>/referrers/<digest>. A repository name may itself contain a
// referrers segment, so the match is the final referrers/<digest> pair.
func isReferrersGet(req *http.Request) bool {
	if req == nil || req.Method != http.MethodGet || req.URL == nil {
		return false
	}
	parts := strings.Split(req.URL.Path, "/")
	if len(parts) < 5 || parts[1] != "v2" || parts[len(parts)-2] != "referrers" {
		return false
	}
	_, err := digest.Parse(parts[len(parts)-1])
	return err == nil
}

func manifestInvalidOnly(body []byte) bool {
	var parsed struct {
		Errors []struct {
			Code string `json:"code"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || len(parsed.Errors) == 0 {
		return false
	}
	for _, item := range parsed.Errors {
		if item.Code != errCodeManifestInvalid {
			return false
		}
	}
	return true
}

func exactRepository(ctx context.Context, spec *domain.OciRepoSpec, repository string) (*remote.Repository, error) {
	if spec == nil {
		return nil, fmt.Errorf("OCI repository spec is required")
	}
	return oci.BuildOciRepoRef(ctx, spec, repository)
}
