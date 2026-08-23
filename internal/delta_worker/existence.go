package delta_worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const (
	ociDeltaArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	ociDeltaSourceAnnotation = "io.github.containers.delta.source"
)

type existenceStatus int

const (
	existenceFound existenceStatus = iota
	existenceNotFound
	existenceInconclusive
)

type existenceResult struct {
	Status    existenceStatus
	SizeBytes int64
}

type existenceConfig struct {
	Client   *http.Client
	Scheme   string
	Username string
	Password string
}

func checkExistingDelta(ctx context.Context, imageRepository, sourceDigest, targetDigest string, cfg existenceConfig) (existenceResult, error) {
	host, repo, err := splitRegistryRepository(imageRepository)
	if err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	scheme := cfg.Scheme
	if scheme == "" {
		scheme = "https"
	}

	status, body, err := registryGet(ctx, client, cfg, fmt.Sprintf("%s://%s/v2/%s/referrers/%s", scheme, host, repo, targetDigest))
	if err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if isInconclusiveStatus(status) {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if status == http.StatusNotFound {
		return checkTagSchema(ctx, client, cfg, scheme, host, repo, sourceDigest, targetDigest)
	}
	if status != http.StatusOK {
		return existenceResult{Status: existenceInconclusive}, nil
	}

	desc, ok, err := matchingDeltaDescriptor(body, sourceDigest)
	if err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if !ok {
		return existenceResult{Status: existenceNotFound}, nil
	}
	return fetchDeltaSize(ctx, client, cfg, scheme, host, repo, desc.Digest.String())
}

func checkTagSchema(ctx context.Context, client *http.Client, cfg existenceConfig, scheme, host, repo, sourceDigest, targetDigest string) (existenceResult, error) {
	status, body, err := registryGet(ctx, client, cfg, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, tagSchemaRef(targetDigest)))
	if err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if isInconclusiveStatus(status) {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if status == http.StatusNotFound {
		return existenceResult{Status: existenceNotFound}, nil
	}
	if status != http.StatusOK {
		return existenceResult{Status: existenceInconclusive}, nil
	}

	desc, ok, err := matchingDeltaDescriptor(body, sourceDigest)
	if err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if !ok {
		return existenceResult{Status: existenceNotFound}, nil
	}
	return fetchDeltaSize(ctx, client, cfg, scheme, host, repo, desc.Digest.String())
}

func fetchDeltaSize(ctx context.Context, client *http.Client, cfg existenceConfig, scheme, host, repo, digest string) (existenceResult, error) {
	status, body, err := registryGet(ctx, client, cfg, fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, digest))
	if err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	if status != http.StatusOK {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		return existenceResult{Status: existenceInconclusive}, nil
	}
	size := manifest.Config.Size
	for _, layer := range manifest.Layers {
		size += layer.Size
	}
	return existenceResult{Status: existenceFound, SizeBytes: size}, nil
}

func matchingDeltaDescriptor(indexBody []byte, sourceDigest string) (ocispec.Descriptor, bool, error) {
	var index ocispec.Index
	if err := json.Unmarshal(indexBody, &index); err != nil {
		return ocispec.Descriptor{}, false, err
	}
	for _, desc := range index.Manifests {
		if desc.ArtifactType != ociDeltaArtifactType {
			continue
		}
		if desc.Annotations[ociDeltaSourceAnnotation] != sourceDigest {
			continue
		}
		if desc.Digest == "" {
			continue
		}
		return desc, true, nil
	}
	return ocispec.Descriptor{}, false, nil
}

func registryGet(ctx context.Context, client *http.Client, cfg existenceConfig, rawURL string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, nil, err
	}
	if cfg.Username != "" {
		req.SetBasicAuth(cfg.Username, cfg.Password)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}

func isInconclusiveStatus(status int) bool {
	return status == http.StatusUnauthorized || status == http.StatusForbidden || status >= http.StatusInternalServerError
}

func tagSchemaRef(digest string) string {
	return strings.Replace(digest, ":", "-", 1)
}

func splitRegistryRepository(imageRepository string) (host, repo string, err error) {
	host, repo, ok := strings.Cut(imageRepository, "/")
	if !ok || host == "" || repo == "" {
		return "", "", fmt.Errorf("unparseable image repository %q", imageRepository)
	}
	return host, repo, nil
}
