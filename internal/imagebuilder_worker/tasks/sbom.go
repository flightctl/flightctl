package tasks

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	"github.com/flightctl/flightctl/internal/oci"
	trustifyv2 "github.com/flightctl/flightctl/internal/trustify/v2"
	"github.com/flightctl/flightctl/internal/vulnerability"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
)

const (
	// CycloneDX media type for SBOM artifacts
	cycloneDXMediaType = "application/vnd.cyclonedx+json"
	// Syft always emits CycloneDX JSON; PURL transform and OCI referrer use the same shape.
	syftSBOMOutputFormat = "cyclonedx-json"
	// sbomExportTarName stays on guest-local storage while Syft scans it.
	sbomExportTarName = "sbom-export.tar"
	// syftWorkDir holds the guest-local image archive and scan output inside Syft.
	syftWorkDir = "/work"
)

// SBOMResult contains the result of SBOM generation
type SBOMResult struct {
	SBOMPath    string // Path to the transformed SBOM file
	ImageDigest string // Digest of the image the SBOM was generated for
}

// generateSBOM generates an SBOM for the built image using Syft.
// It runs Syft as a container, generates a CycloneDX SBOM, transforms PURLs,
// and returns the path to the transformed SBOM file.
// The manifestDigest parameter is the registry manifest digest captured during push.
func (c *Consumer) generateSBOM(ctx context.Context, imageRef, manifestDigest string, worker *podmanWorker, log logrus.FieldLogger) (*SBOMResult, error) {
	guestDir := "/var/lib/containers/output"
	if err := worker.Session.Run(ctx, []string{"mkdir", "-p", guestDir}, nil, nil, io.Discard); err != nil {
		return nil, err
	}
	if err := worker.runInWorker(ctx, log, "save image for SBOM", nil, "save", "-o", guestDir+"/"+sbomExportTarName, imageRef); err != nil {
		return nil, err
	}
	syftImage := c.cfg.ImageBuilderWorker.EffectiveSyftImage()
	pull := []string{"pull", "--authfile", serviceAuthFile}
	if c.cfg.ImageBuilderWorker.EffectiveSyftSkipTLSVerify() {
		pull = append(pull, "--tls-verify=false")
	}
	if err := worker.runInWorker(ctx, log, "pull Syft", nil, append(pull, syftImage)...); err != nil {
		return nil, err
	}
	sourceName, sourceVersion := syftSourceNameAndVersion(imageRef)
	args := []string{"run", "--rm", "-v", guestDir + ":" + syftWorkDir + ":Z", syftImage, "scan", "-v", "--source-name", sourceName}
	if sourceVersion != "" {
		args = append(args, "--source-version", sourceVersion)
	}
	args = append(args, "docker-archive:"+syftWorkDir+"/"+sbomExportTarName, "-o", syftSBOMOutputFormat+"="+syftWorkDir+"/sbom.json")
	if err := worker.runInWorker(ctx, log, "Syft scan", nil, args...); err != nil {
		return nil, err
	}
	if err := worker.Session.Run(ctx, []string{"cp", guestDir + "/sbom.json", "/output/sbom.json"}, nil, nil, io.Discard); err != nil {
		return nil, err
	}
	path, err := worker.Session.VerifyArtifact(ctx, "sbom.json")
	if err != nil {
		return nil, err
	}
	if err := worker.Session.Close(); err != nil {
		return nil, err
	}
	if err := validateGuestOutput(ctx, worker.TmpOutDir); err != nil {
		return nil, err
	}
	transformed, err := c.transformSBOM(ctx, path, worker.TmpOutDir, imageRef, manifestDigest, log)
	if err != nil {
		log.WithError(err).Warn("SBOM transformation failed, using original SBOM")
		transformed = path
	}
	return &SBOMResult{SBOMPath: transformed, ImageDigest: manifestDigest}, nil
}

// transformSBOM applies PURL transformations and enriches metadata in the SBOM.
func (c *Consumer) transformSBOM(_ context.Context, sbomPath, outDir, imageRef, imageDigest string, log logrus.FieldLogger) (string, error) {
	sbomData, err := os.ReadFile(sbomPath)
	if err != nil {
		return "", fmt.Errorf("reading SBOM: %w", err)
	}

	// Enrich SBOM metadata with image digest
	sbomData, err = EnrichSBOMMetadata(sbomData, imageRef, imageDigest)
	if err != nil {
		return "", fmt.Errorf("enriching SBOM metadata: %w", err)
	}
	log.WithFields(logrus.Fields{
		"imageRef":    imageRef,
		"imageDigest": imageDigest,
	}).Info("SBOM metadata enriched with image digest")

	// Transform PURLs if enabled
	var userPurl *config.PurlTransformConfig
	if c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.SBOM != nil {
		userPurl = c.cfg.ImageBuilderWorker.SBOM.PurlTransform
	}
	transformCfg := GetEffectivePurlTransformConfig(userPurl)
	if transformCfg.EffectivePurlTransformEnabled() {
		sbomData, err = TransformSBOMPurls(sbomData, transformCfg)
		if err != nil {
			return "", fmt.Errorf("transforming SBOM PURLs: %w", err)
		}
		log.Info("SBOM PURL transformation completed")
	}

	transformedPath := filepath.Join(outDir, "sbom-transformed.json")
	if err := os.WriteFile(transformedPath, sbomData, 0600); err != nil {
		return "", fmt.Errorf("writing transformed SBOM: %w", err)
	}

	return transformedPath, nil
}

// pushSBOMAsReferrer pushes the SBOM CycloneDX document to the destination registry using oras-go/v2
// as a referrer artifact that references the built image. It follows the same steps as pushArtifact
// (image export): resolve subject manifest, stream-compute blob digest, Repository.Push, PackManifest 1.1.
func (c *Consumer) pushSBOMAsReferrer(
	ctx context.Context,
	orgID uuid.UUID,
	imageBuild *domain.ImageBuild,
	sbomResult *SBOMResult,
	statusUpdater *statusUpdater,
	log logrus.FieldLogger,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	spec := imageBuild.Spec

	ociSpec, err := c.getOciRepoSpec(ctx, orgID, spec.Destination.Repository, "destination")
	if err != nil {
		return fmt.Errorf("getting destination OCI spec: %w", err)
	}

	destRegistryHostname := ociSpec.Registry
	destRef := fmt.Sprintf("%s/%s", destRegistryHostname, spec.Destination.ImageName)
	destTag := spec.Destination.ImageTag

	log.WithFields(logrus.Fields{
		"destination": destRef,
		"subject":     fmt.Sprintf("%s:%s", destRef, destTag),
		"sbomPath":    sbomResult.SBOMPath,
		"imageDigest": sbomResult.ImageDigest,
	}).Info("Pushing SBOM as referrer to destination")

	report := func(b []byte) {
		if statusUpdater == nil {
			return
		}
		statusUpdater.ReportOutput(b)
	}
	report([]byte("Starting SBOM push to destination registry\n"))

	repoRef, err := oci.BuildOciRepoRef(ctx, ociSpec, destRef)
	if err != nil {
		return fmt.Errorf("failed to configure OCI repository reference: %w", err)
	}

	// Skip referrers GC to avoid authentication issues when pushing multiple artifacts
	repoRef.SkipReferrersGC = true

	if repoRef.PlainHTTP {
		log.Debug("Using PlainHTTP for HTTP registry")
	}
	if ociSpec.OciAuth != nil {
		log.Info("Successfully configured authentication for destination registry")
		report([]byte("Authenticated with destination registry\n"))
	}

	var onRegistryOutput func([]byte)
	if statusUpdater != nil {
		onRegistryOutput = statusUpdater.ReportOutput
	}
	destManifestDesc, err := resolveReferencedDigest(ctx, repoRef, destTag, destRef, onRegistryOutput, log)
	if err != nil {
		return err
	}

	report([]byte(fmt.Sprintf("Opening SBOM file: %s\n", filepath.Base(sbomResult.SBOMPath))))
	sbomFile, err := os.Open(sbomResult.SBOMPath)
	if err != nil {
		return fmt.Errorf("failed to open SBOM file: %w", err)
	}
	defer sbomFile.Close()

	fileInfo, err := sbomFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat SBOM file: %w", err)
	}
	fileSize := fileInfo.Size()

	report([]byte(fmt.Sprintf("Computing digest for SBOM file (%d bytes)\n", fileSize)))
	digester := digest.Canonical.Digester()
	if _, err := io.Copy(digester.Hash(), sbomFile); err != nil {
		return fmt.Errorf("failed to compute digest: %w", err)
	}
	computedDigest := digester.Digest()

	if _, err := sbomFile.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek SBOM file to start: %w", err)
	}

	sbomFileName := filepath.Base(sbomResult.SBOMPath)
	blobDesc := ocispec.Descriptor{
		MediaType: cycloneDXMediaType,
		Digest:    computedDigest,
		Size:      fileSize,
		Annotations: map[string]string{
			ocispec.AnnotationTitle: sbomFileName,
		},
	}

	progressReader := newProgressReader(sbomFile, fileSize, func(bytesRead int64, totalBytes int64) {
		percent := float64(bytesRead) / float64(totalBytes) * 100
		report([]byte(fmt.Sprintf("Pushing SBOM blob: %d/%d bytes (%.1f%%)\n", bytesRead, totalBytes, percent)))
	})

	report([]byte(fmt.Sprintf("Starting push of SBOM blob (%d bytes) to repository\n", fileSize)))
	if err := repoRef.Push(ctx, blobDesc, progressReader); err != nil {
		return fmt.Errorf("failed to push SBOM blob: %w", err)
	}

	report([]byte(fmt.Sprintf("Successfully pushed blob: %s\n", blobDesc.Digest.String())))

	report([]byte("Packing SBOM as referrer manifest\n"))
	packOpts := oras.PackManifestOptions{
		Subject: &destManifestDesc,
		Layers:  []ocispec.Descriptor{blobDesc},
		ManifestAnnotations: map[string]string{
			ocispec.AnnotationTitle: sbomFileName,
		},
	}
	manifestDesc, err := oras.PackManifest(ctx, repoRef, oras.PackManifestVersion1_1, cycloneDXMediaType, packOpts)
	if err != nil {
		return fmt.Errorf("failed to pack SBOM manifest: %w", err)
	}

	referrerManifestDigest := manifestDesc.Digest.String()
	artifactBlobDigest := blobDesc.Digest.String()

	log.WithFields(logrus.Fields{
		"destination":    destRef,
		"subject":        fmt.Sprintf("%s:%s", destRef, destTag),
		"mediaType":      cycloneDXMediaType,
		"manifestDigest": referrerManifestDigest,
		"artifactDigest": artifactBlobDigest,
		"subjectDigest":  destManifestDesc.Digest.String(),
	}).Info("Successfully pushed SBOM as referrer (discoverable via referrers API 1.1)")
	report([]byte("Successfully pushed referrer artifact (discoverable via referrers API 1.1)\n"))
	report([]byte(fmt.Sprintf("Referrer manifest digest: %s\n", referrerManifestDigest)))
	report([]byte(fmt.Sprintf("Artifact blob digest: %s\n", artifactBlobDigest)))
	report([]byte(fmt.Sprintf("Subject digest: %s\n", destManifestDesc.Digest.String())))

	return nil
}

// syftSourceNameAndVersion maps the pushed OCI reference (e.g. quay.io/ns/img:tag or img@sha256:…)
// to Syft --source-name / --source-version so CycloneDX metadata.component is the image, not the
// docker-archive path (which Trustify shows as the SBOM product name/version).
func syftSourceNameAndVersion(imageRef string) (name, version string) {
	if imageRef == "" {
		return "", ""
	}
	if i := strings.LastIndex(imageRef, "@"); i != -1 {
		return imageRef[:i], imageRef[i+1:]
	}
	i := strings.LastIndex(imageRef, ":")
	if i == -1 {
		return imageRef, ""
	}
	prefix := imageRef[:i]
	suffix := imageRef[i+1:]
	if !strings.Contains(prefix, "/") {
		return imageRef, ""
	}
	return prefix, suffix
}

// uploadSBOMToTrustify uploads the SBOM to Trustify for vulnerability scanning.
func (c *Consumer) uploadSBOMToTrustify(
	ctx context.Context,
	trustifyClient trustifyv2.VulnerabilityClient,
	sbomResult *SBOMResult,
	log logrus.FieldLogger,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	sbomData, err := os.ReadFile(sbomResult.SBOMPath)
	if err != nil {
		return fmt.Errorf("reading SBOM file: %w", err)
	}

	log.WithField("imageDigest", sbomResult.ImageDigest).Info("Uploading SBOM to Trustify")

	if err := trustifyClient.UploadSBOM(ctx, sbomData, sbomResult.ImageDigest); err != nil {
		return fmt.Errorf("uploading SBOM to Trustify: %w", err)
	}

	log.Info("Successfully uploaded SBOM to Trustify")
	return nil
}

// isSBOMEnabled returns whether SBOM generation is enabled.
func (c *Consumer) isSBOMEnabled() bool {
	return c.cfg.ImageBuilderWorker.IsSBOMEnabled()
}

// shouldPushSBOMToRegistry returns whether to push SBOM to OCI registry.
func (c *Consumer) shouldPushSBOMToRegistry() bool {
	return c.cfg.ImageBuilderWorker.SBOMPushToRegistry()
}

// shouldUploadSBOMToTrustify returns whether to upload SBOM to Trustify.
func (c *Consumer) shouldUploadSBOMToTrustify() bool {
	return c.cfg.ImageBuilderWorker.SBOMUploadToTrustify()
}

// shouldRunSBOMPipeline reports whether SBOM generation should run. Syft runs only when
// at least one distribution path is in effect (OCI referrer push or Trustify upload).
func (c *Consumer) shouldRunSBOMPipeline() bool {
	if !c.isSBOMEnabled() {
		return false
	}
	if c.shouldPushSBOMToRegistry() {
		return true
	}
	if !c.shouldUploadSBOMToTrustify() {
		return false
	}
	v := c.cfg.VulnerabilityReporting
	return v != nil && v.Enabled && vulnerability.RequiresSBOMUpload(v.EffectiveBackend())
}
