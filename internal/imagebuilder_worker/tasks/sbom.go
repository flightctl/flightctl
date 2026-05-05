package tasks

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flightctl/flightctl/internal/config"
	coredomain "github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/imagebuilder_api/domain"
	trustifyv2 "github.com/flightctl/flightctl/internal/trustify/v2"
	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/sirupsen/logrus"
	"oras.land/oras-go/v2"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	// CycloneDX media type for SBOM artifacts
	cycloneDXMediaType = "application/vnd.cyclonedx+json"
	// Syft always emits CycloneDX JSON; PURL transform and OCI referrer use the same shape.
	syftSBOMOutputFormat = "cyclonedx-json"
	// sbomExportTarName is written by inner podman save under the worker /output mount (host TmpOutDir).
	sbomExportTarName = "sbom-export.tar"
	// workerSBOMExportTarInner is the path inner podman uses when saving the image tarball for Syft.
	workerSBOMExportTarInner = "/output/sbom-export.tar"
	// syftWorkDir is the mount point inside the Syft container for TmpOutDir (archive + SBOM output).
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
func (c *Consumer) generateSBOM(
	ctx context.Context,
	imageRef string,
	podmanWorker *podmanWorker,
	log logrus.FieldLogger,
) (*SBOMResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Get Syft image config
	syftImage := c.cfg.ImageBuilderWorker.EffectiveSyftImage()
	skipTLS := c.cfg.ImageBuilderWorker.EffectiveSyftSkipTLSVerify()

	sbomPath := filepath.Join(podmanWorker.TmpOutDir, "sbom.json")
	exportHostPath := filepath.Join(podmanWorker.TmpOutDir, sbomExportTarName)
	defer func() {
		err := os.Remove(exportHostPath)
		if err == nil || os.IsNotExist(err) {
			return
		}
		log.WithError(err).WithField("path", exportHostPath).Debug("Failed to remove SBOM export tarball")
	}()

	_ = os.Remove(exportHostPath)
	if err := podmanWorker.runInWorker(ctx, log, "podman save for SBOM", nil, "save", "-o", workerSBOMExportTarInner, imageRef); err != nil {
		return nil, fmt.Errorf("exporting image for SBOM: %w", err)
	}

	log.WithFields(logrus.Fields{
		"syftImage": syftImage,
		"skipTLS":   skipTLS,
		"imageRef":  imageRef,
	}).Info("Running Syft to generate SBOM")

	dockerArchiveSrc := fmt.Sprintf("docker-archive:%s/%s", syftWorkDir, sbomExportTarName)
	syftOutInner := fmt.Sprintf("%s/sbom.json", syftWorkDir)
	syftSrcName, syftSrcVersion := syftSourceNameAndVersion(imageRef)

	// Syft needs a read-write mount: it writes CycloneDX JSON alongside the archive.
	args := []string{
		"run", "--rm",
		"-v", fmt.Sprintf("%s:%s:Z", podmanWorker.TmpOutDir, syftWorkDir),
	}

	if skipTLS {
		args = append(args, "--tls-verify=false")
	}

	args = append(args, syftImage, "scan", "--source-name", syftSrcName)
	if syftSrcVersion != "" {
		args = append(args, "--source-version", syftSrcVersion)
	}
	args = append(args,
		dockerArchiveSrc,
		"-o", fmt.Sprintf("%s=%s", syftSBOMOutputFormat, syftOutInner),
	)

	log.WithField("args", args).Debug("Executing Syft command")

	cmd := exec.CommandContext(ctx, "podman", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.WithError(err).WithFields(logrus.Fields{
			"stdout": stdout.String(),
			"stderr": stderr.String(),
		}).Error("Syft SBOM generation failed")
		return nil, fmt.Errorf("syft SBOM generation failed: %w (stderr: %s)", err, stderr.String())
	}

	log.Info("Syft SBOM generation completed")

	// Get image digest
	imageDigest, err := c.getImageDigest(ctx, imageRef, podmanWorker)
	if err != nil {
		return nil, fmt.Errorf("getting image digest: %w", err)
	}

	// Transform PURLs if enabled
	transformedPath, err := c.transformSBOM(ctx, sbomPath, podmanWorker.TmpOutDir, log)
	if err != nil {
		log.WithError(err).Warn("PURL transformation failed, using original SBOM")
		transformedPath = sbomPath
	}

	return &SBOMResult{
		SBOMPath:    transformedPath,
		ImageDigest: imageDigest,
	}, nil
}

// getImageDigest gets the digest of an image from the worker's podman storage.
func (c *Consumer) getImageDigest(ctx context.Context, imageRef string, podmanWorker *podmanWorker) (string, error) {
	// Use podman inspect to get the image digest
	args := []string{
		"exec", podmanWorker.ContainerName,
		"podman", "inspect", "--format", "{{.Digest}}", imageRef,
	}

	cmd := exec.CommandContext(ctx, "podman", args...)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("getting image digest: %w", err)
	}

	digestStr := strings.TrimSpace(string(output))
	if digestStr == "" || digestStr == "<no value>" {
		// Try getting RepoDigests instead
		args = []string{
			"exec", podmanWorker.ContainerName,
			"podman", "inspect", "--format", "{{index .RepoDigests 0}}", imageRef,
		}
		cmd = exec.CommandContext(ctx, "podman", args...)
		output, err = cmd.Output()
		if err != nil {
			return "", fmt.Errorf("getting image repo digest: %w", err)
		}
		digestStr = strings.TrimSpace(string(output))
		// Extract just the digest part from repo@digest format
		if idx := strings.Index(digestStr, "@"); idx >= 0 {
			digestStr = digestStr[idx+1:]
		}
	}

	return digestStr, nil
}

// transformSBOM applies PURL transformations to the SBOM.
func (c *Consumer) transformSBOM(_ context.Context, sbomPath, outDir string, log logrus.FieldLogger) (string, error) {
	var userPurl *config.PurlTransformConfig
	if c.cfg.ImageBuilderWorker != nil && c.cfg.ImageBuilderWorker.SBOM != nil {
		userPurl = c.cfg.ImageBuilderWorker.SBOM.PurlTransform
	}
	transformCfg := GetEffectivePurlTransformConfig(userPurl)
	if !transformCfg.EffectivePurlTransformEnabled() {
		return sbomPath, nil
	}

	sbomData, err := os.ReadFile(sbomPath)
	if err != nil {
		return "", fmt.Errorf("reading SBOM: %w", err)
	}

	transformedData, err := TransformSBOMPurls(sbomData, transformCfg)
	if err != nil {
		return "", fmt.Errorf("transforming SBOM: %w", err)
	}

	transformedPath := filepath.Join(outDir, "sbom-transformed.json")
	if err := os.WriteFile(transformedPath, transformedData, 0600); err != nil {
		return "", fmt.Errorf("writing transformed SBOM: %w", err)
	}

	log.Info("SBOM PURL transformation completed")
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

	repoRef, err := remote.NewRepository(destRef)
	if err != nil {
		return fmt.Errorf("failed to create repository reference: %w", err)
	}

	if ociSpec.Scheme != nil && *ociSpec.Scheme == coredomain.OciRepoSchemeHttp {
		repoRef.PlainHTTP = true
		log.Debug("Using PlainHTTP for HTTP registry")
	}

	// Skip referrers GC to avoid authentication issues when pushing multiple artifacts
	repoRef.SkipReferrersGC = true

	authClient, err := newOCIAuthClient(ociSpec, destRegistryHostname, log)
	if err != nil {
		return fmt.Errorf("failed to configure OCI auth client: %w", err)
	}
	if authClient.Credential != nil {
		log.Info("Successfully configured authentication for destination registry")
		report([]byte("Authenticated with destination registry\n"))
	}
	repoRef.Client = authClient

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

	blobDesc := ocispec.Descriptor{
		MediaType: cycloneDXMediaType,
		Digest:    computedDigest,
		Size:      fileSize,
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
			ocispec.AnnotationTitle: filepath.Base(sbomResult.SBOMPath),
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
	return v != nil && v.Enabled && v.Trustify != nil
}
