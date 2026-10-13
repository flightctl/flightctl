// Package delta contains storage-independent OCI delta candidate decisions.
package delta

import (
	"fmt"
	"math"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/util/validation"
	"github.com/opencontainers/go-digest"
)

const (
	ArtifactType     = "application/vnd.io.github.containers.oci-delta.v1"
	SourceAnnotation = "io.github.containers.delta.source"
)

// Candidate describes a validated artifact and availability in the target's store.
type Candidate struct {
	Reference       string
	SourceDigest    string
	TargetDigest    string
	DownloadSize    int64
	SourceAvailable bool
}

// IsDeltaReferrer reports whether the descriptor identifies a delta artifact.
func IsDeltaReferrer(ref client.OCIReferrer) bool {
	if ref.ArtifactType != ArtifactType {
		return false
	}
	_, err := digest.Parse(ref.Digest)
	return err == nil
}

// Repository returns the repository portion of an OCI image reference.
func Repository(image string) string {
	matches := validation.OciImageReferenceRegexp.FindStringSubmatch(image)
	if len(matches) == 0 {
		return ""
	}
	return matches[1]
}

// FromReferrerManifest validates a manifest discovered through an OCI referrer
// descriptor and sums its downloadable blob sizes.
func FromReferrerManifest(reference string, manifest *client.OCIManifest, manifestSize int64) (Candidate, error) {
	if manifestSize <= 0 {
		return Candidate{}, fmt.Errorf("delta referrer manifest size is missing or invalid")
	}
	return fromManifest(reference, manifest, manifestSize)
}

// FromHintManifest validates an explicitly hinted manifest. Its descriptor size
// is not known before the manifest is fetched.
func FromHintManifest(reference string, manifest *client.OCIManifest) (Candidate, error) {
	return fromManifest(reference, manifest, 0)
}

func fromManifest(reference string, manifest *client.OCIManifest, manifestSize int64) (Candidate, error) {
	if manifest == nil || manifest.ArtifactType != ArtifactType {
		return Candidate{}, fmt.Errorf("candidate is not an OCI image delta")
	}
	source := manifest.Annotations[SourceAnnotation]
	if _, err := digest.Parse(source); err != nil {
		return Candidate{}, fmt.Errorf("delta source digest is missing or invalid")
	}
	if manifest.Subject == nil || manifest.Subject.Digest == "" {
		return Candidate{}, fmt.Errorf("delta subject digest is missing")
	}
	target := manifest.Subject.Digest
	if _, err := digest.Parse(target); err != nil {
		return Candidate{}, fmt.Errorf("delta subject digest is invalid")
	}
	if manifest.Subject.MediaType == "" || manifest.Subject.Size <= 0 {
		return Candidate{}, fmt.Errorf("delta subject descriptor is missing or invalid")
	}
	if manifest.Config == nil || len(manifest.Layers) == 0 || manifestSize < 0 {
		return Candidate{}, fmt.Errorf("delta artifact descriptors are missing or invalid")
	}
	size := manifestSize
	descriptors := append([]client.OCIDescriptor{*manifest.Config}, manifest.Layers...)
	for _, descriptor := range descriptors {
		if descriptor.MediaType == "" {
			return Candidate{}, fmt.Errorf("delta artifact descriptor media type is missing")
		}
		if _, err := digest.Parse(descriptor.Digest); err != nil || descriptor.Size < 0 || descriptor.Size > math.MaxInt64-size {
			return Candidate{}, fmt.Errorf("delta artifact descriptor digest or size is invalid")
		}
		size += descriptor.Size
	}
	return Candidate{Reference: reference, SourceDigest: source, TargetDigest: target, DownloadSize: size}, nil
}

// Select chooses the smallest locally usable delta, with a stable reference tie-break.
func Select(candidates []Candidate) Candidate {
	var selected *Candidate
	for i := range candidates {
		candidate := &candidates[i]
		if !candidate.SourceAvailable {
			continue
		}
		if selected == nil || candidate.DownloadSize < selected.DownloadSize ||
			(candidate.DownloadSize == selected.DownloadSize && candidate.Reference < selected.Reference) {
			selected = candidate
		}
	}
	if selected == nil {
		return Candidate{}
	}
	return *selected
}
