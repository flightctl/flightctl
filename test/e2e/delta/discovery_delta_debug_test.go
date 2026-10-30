package delta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

const discoveredDeltaDebugPrefix = "[DEBUG-delta-discovery]"

type discoveredDeltaDebugManifest struct {
	ocispec.Manifest
	Manifests []ocispec.Descriptor `json:"manifests,omitempty"`
}

func logDiscoveredDeltaDebug(format string, args ...any) {
	GinkgoWriter.Printf(discoveredDeltaDebugPrefix+" "+format+"\n", args...)
}

// logDiscoveredDeltaPublication captures registry metadata before test cleanup.
// Diagnostic failures are logged without changing the test's assertions.
func logDiscoveredDeltaPublication(registry, repository, sourceRef, targetRef string, artifact discoveredDeltaArtifact) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := e2eRegistryHTTPClient()
	if err != nil {
		logDiscoveredDeltaDebug("registry client: %v", err)
		return
	}

	var sourceDigest, targetDigest, subjectDigest string
	for _, image := range []struct{ role, reference string }{
		{role: "source", reference: sourceRef},
		{role: "target", reference: targetRef},
		{role: "delta", reference: artifact.reference},
	} {
		parts, err := parseApplicationImageReference(image.reference)
		if err != nil {
			logDiscoveredDeltaDebug("%s reference: %v", image.role, err)
			continue
		}
		if image.role == "source" {
			sourceDigest = parts.digest
		}
		if image.role == "target" {
			targetDigest = parts.digest
		}
		path := registryRepositoryPath(registry, parts.repository) + "/manifests/" + parts.digest
		manifest := logDiscoveredDeltaRegistryResponse(ctx, client, image.role, path)
		if image.role == "delta" && manifest != nil {
			logDiscoveredDeltaDebug("delta expected source=%s target=%s", sourceRef, targetRef)
			if manifest.Subject != nil {
				subjectDigest = manifest.Subject.Digest.String()
				logDiscoveredDeltaDebug("delta subjectMatchesExpectedTarget=%t", subjectDigest == targetDigest)
			}
			logDiscoveredDeltaDebug("delta sourceMatchesExpectedSource=%t", manifest.Annotations[discoveredDeltaSourceAnnotation] == sourceDigest)
		}
	}

	for i, subject := range []string{targetDigest, subjectDigest} {
		if subject == "" || (i == 1 && subject == targetDigest) {
			continue
		}
		if _, err := digest.Parse(subject); err != nil {
			logDiscoveredDeltaDebug("invalid referrer subject=%q: %v", subject, err)
			continue
		}
		logDiscoveredDeltaRegistryResponse(ctx, client, "referrers API", repository+"/referrers/"+subject)
		logDiscoveredDeltaRegistryResponse(ctx, client, "referrers tag", repository+"/manifests/"+strings.Replace(subject, ":", "-", 1))
	}
}

func logDiscoveredDeltaRegistryResponse(ctx context.Context, client *http.Client, role, path string) *discoveredDeltaDebugManifest {
	url := "https://" + auxSvcs.Registry.URL + "/v2/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logDiscoveredDeltaDebug("%s request path=%s: %v", role, path, err)
		return nil
	}
	req.Header.Set("Accept", registryManifestAccept)
	resp, err := client.Do(req)
	if err != nil {
		logDiscoveredDeltaDebug("%s GET path=%s: %v", role, path, err)
		return nil
	}
	defer resp.Body.Close()
	logDiscoveredDeltaDebug("%s GET path=%s status=%s contentType=%s registryDigest=%s", role, path,
		resp.Status, resp.Header.Get("Content-Type"), resp.Header.Get("Docker-Content-Digest"))
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	const maxManifestBytes = 2 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes+1))
	if err != nil {
		logDiscoveredDeltaDebug("%s read body: %v", role, err)
		return nil
	}
	if len(body) > maxManifestBytes {
		logDiscoveredDeltaDebug("%s body exceeds diagnostic limit of %d bytes", role, maxManifestBytes)
		return nil
	}
	var manifest discoveredDeltaDebugManifest
	if err := json.Unmarshal(body, &manifest); err != nil {
		logDiscoveredDeltaDebug("%s decode manifest: %v", role, err)
		return nil
	}
	logDiscoveredDeltaDebug("%s bodyDigest=%s mediaType=%s artifactType=%s source=%s", role,
		digest.FromBytes(body), manifest.MediaType, manifest.ArtifactType, manifest.Annotations[discoveredDeltaSourceAnnotation])
	if manifest.Subject != nil {
		logDiscoveredDeltaDebug("%s subject digest=%s mediaType=%s size=%d", role,
			manifest.Subject.Digest, manifest.Subject.MediaType, manifest.Subject.Size)
	}
	for _, descriptor := range manifest.Manifests {
		platform := "unspecified"
		if descriptor.Platform != nil {
			platform = fmt.Sprintf("%s/%s/%s", descriptor.Platform.OS, descriptor.Platform.Architecture, descriptor.Platform.Variant)
		}
		logDiscoveredDeltaDebug("%s entry digest=%s mediaType=%s platform=%s artifactType=%s source=%s", role,
			descriptor.Digest, descriptor.MediaType, platform, descriptor.ArtifactType, descriptor.Annotations[discoveredDeltaSourceAnnotation])
	}
	return &manifest
}
