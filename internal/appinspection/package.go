package appinspection

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/api/common"
	"github.com/flightctl/flightctl/internal/appspec"
	"github.com/flightctl/flightctl/internal/oci"
	"github.com/flightctl/flightctl/internal/quadlet"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/registry/remote"
)

const (
	dockerIndexMediaType     = "application/vnd.docker.distribution.manifest.list.v2+json"
	dockerManifestMediaType  = "application/vnd.docker.distribution.manifest.v2+json"
	dockerConfigMediaType    = "application/vnd.docker.container.image.v1+json"
	dockerLayerMediaType     = "application/vnd.docker.image.rootfs.diff.tar"
	dockerLayerGzipMediaType = "application/vnd.docker.image.rootfs.diff.tar.gzip"
)

type PackageResult struct {
	Result
	ParentDigest digest.Digest
	Platform     *ocispec.Platform
	Format       string
}

// InspectPackage discovers references inside an OCI Compose or Quadlet package.
// The repository must already be configured for the inspection endpoint.
func InspectPackage(ctx context.Context, appType v1beta1.AppType, repo *remote.Repository, ref string, platform *ocispec.Platform) PackageResult {
	return inspectPackage(ctx, appType, repo, ref, platform, packageDiskBudget)
}

func inspectPackage(ctx context.Context, appType v1beta1.AppType, repo *remote.Repository, ref string, platform *ocispec.Platform, budget int64) (result PackageResult) {
	md, parent, selected, err := fetchPackageManifest(ctx, repo, ref, platform)
	if err != nil {
		result.Diagnostics = []Diagnostic{DiagnosticContentUnavailable}
		return result
	}
	result.ParentDigest, result.Platform = parent, selected
	blobs, format, err := packageBlobs(md)
	if err != nil {
		result.Diagnostics = []Diagnostic{DiagnosticParseFailed}
		return result
	}
	result.Format = string(format)
	workspace, err := newPackageWorkspace(budget)
	if err != nil {
		result.Diagnostics = []Diagnostic{DiagnosticContentUnavailable}
		return result
	}
	defer func() {
		if workspace.close() != nil && !slices.Contains(result.Diagnostics, DiagnosticContentUnavailable) {
			result.Diagnostics = append(result.Diagnostics, DiagnosticContentUnavailable)
		}
	}()

	var diagnostic Diagnostic
	for _, blob := range blobs {
		diagnostic = workspace.acquire(ctx, repo, blob)
		if diagnostic != "" {
			break
		}
	}
	if ctx.Err() == nil {
		result.Result = inspectPackageFiles(ctx, workspace.files(), appType)
	}
	if ctx.Err() != nil {
		diagnostic = DiagnosticContentUnavailable
	}
	if diagnostic != "" {
		// Report the acquisition failure; the incomplete file view can itself cause parse errors.
		result.Diagnostics = []Diagnostic{diagnostic}
	}
	return result
}

func fetchPackageManifest(ctx context.Context, repo *remote.Repository, ref string, platform *ocispec.Platform) (ocispec.Manifest, digest.Digest, *ocispec.Platform, error) {
	const maxIndexDepth = 8
	const maxMetadataBytes = 4 << 20 // ORAS default metadata limit.
	desc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return ocispec.Manifest{}, "", nil, err
	}
	parent := desc.Digest
	for depth := 0; depth <= maxIndexDepth; depth++ {
		if desc.Size < 0 || desc.Size > maxMetadataBytes {
			break
		}
		switch desc.MediaType {
		case ocispec.MediaTypeImageIndex, dockerIndexMediaType:
			desc, err = oci.SelectPlatformManifest(ctx, repo, desc, platform)
			if err != nil {
				return ocispec.Manifest{}, "", nil, err
			}
		case ocispec.MediaTypeImageManifest, dockerManifestMediaType:
			data, err := content.FetchAll(ctx, repo.Manifests(), desc)
			if err != nil {
				return ocispec.Manifest{}, "", nil, err
			}
			var md ocispec.Manifest
			err = json.Unmarshal(data, &md)
			return md, parent, desc.Platform, err
		default:
			return ocispec.Manifest{}, "", nil, errInvalidArchive
		}
	}
	return ocispec.Manifest{}, "", nil, errInvalidArchive
}

type packageBlob struct {
	desc       ocispec.Descriptor
	title      string
	format     packageFormat
	imageLayer bool
}

func packageBlobs(md ocispec.Manifest) ([]packageBlob, packageFormat, error) {
	if md.SchemaVersion != 2 || md.Config.MediaType == "" || len(md.Layers) == 0 {
		return nil, "", errInvalidArchive
	}
	if md.Config.MediaType == ocispec.MediaTypeImageConfig || md.Config.MediaType == dockerConfigMediaType {
		blobs := make([]packageBlob, 0, len(md.Layers))
		for _, desc := range md.Layers {
			format, err := imageLayerFormat(desc.MediaType)
			if err != nil {
				return nil, "", err
			}
			blobs = append(blobs, packageBlob{desc: desc, format: format, imageLayer: true})
		}
		return blobs, packageTar, nil
	}

	format := packageRaw
	blobs := make([]packageBlob, 0, len(md.Layers))
	for _, desc := range md.Layers {
		title, err := packagePath(desc.Annotations[ocispec.AnnotationTitle])
		if err != nil || title == "." {
			return nil, "", errInvalidArchive
		}
		blobFormat := packageRaw
		if filepath.Base(title) == title {
			blobFormat = artifactFormat(title)
		}
		if blobFormat == packageTarGzip || (blobFormat == packageTar && format == packageRaw) {
			format = blobFormat
		}
		blobs = append(blobs, packageBlob{desc: desc, title: title, format: blobFormat})
	}
	// The device processes artifact entries in filename order.
	slices.SortStableFunc(blobs, func(a, b packageBlob) int { return strings.Compare(a.title, b.title) })
	return blobs, format, nil
}

func imageLayerFormat(mediaType string) (packageFormat, error) {
	switch mediaType {
	case ocispec.MediaTypeImageLayer, dockerLayerMediaType:
		return packageTar, nil
	case ocispec.MediaTypeImageLayerGzip, dockerLayerGzipMediaType:
		return packageTarGzip, nil
	case ocispec.MediaTypeImageLayerZstd:
		return packageTarZstd, nil
	default:
		return "", errInvalidArchive
	}
}

func (w *packageWorkspace) acquire(ctx context.Context, repo *remote.Repository, blob packageBlob) Diagnostic {
	const download = "download"
	if err := w.download(ctx, repo, blob.desc, download); err != nil {
		return packageDiagnostic(err, DiagnosticContentUnavailable)
	}
	var err error
	if blob.format == packageRaw {
		err = w.publish(ctx, download, filepath.Join("files", blob.title))
	} else {
		err = w.extract(ctx, download, blob.format, blob.imageLayer)
	}
	if err != nil {
		return packageDiagnostic(err, DiagnosticParseFailed)
	}
	if err := w.remove(download); err != nil {
		return DiagnosticContentUnavailable
	}
	return ""
}

func (w *packageWorkspace) download(ctx context.Context, repo *remote.Repository, desc ocispec.Descriptor, name string) error {
	if desc.Size < 0 {
		return content.ErrInvalidDescriptorSize
	}
	rc, err := repo.Blobs().Fetch(ctx, desc)
	if err != nil {
		return err
	}
	verified := content.NewVerifyReader(packageReader{ctx: ctx, src: rc}, desc)
	err = w.put(ctx, name, verified)
	if err == nil {
		err = verified.Verify()
	}
	return errors.Join(err, rc.Close())
}

func packageDiagnostic(err error, fallback Diagnostic) Diagnostic {
	switch {
	case errors.Is(err, errBudgetExceeded):
		return DiagnosticBudgetExceeded
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return DiagnosticContentUnavailable
	default:
		return fallback
	}
}

func inspectPackageFiles(ctx context.Context, files fs.FS, appType v1beta1.AppType) Result {
	switch appType {
	case v1beta1.AppTypeCompose:
		base, err := firstPackageFile(files, appspec.BaseComposeFiles)
		if err != nil || base == nil {
			return Result{Diagnostics: []Diagnostic{DiagnosticParseFailed}}
		}
		contents := []v1beta1.ApplicationContent{*base}
		override, err := firstPackageFile(files, appspec.OverrideComposeFiles)
		if override != nil {
			contents = append(contents, *override)
		}
		result := InspectCompose(contents)
		if err != nil && result.Complete() {
			result.Diagnostics = []Diagnostic{DiagnosticParseFailed}
		}
		return result
	case v1beta1.AppTypeQuadlet:
		return inspectPackageQuadlets(ctx, files)
	default:
		return Result{Diagnostics: []Diagnostic{DiagnosticParseFailed}}
	}
}

func firstPackageFile(files fs.FS, names []string) (*v1beta1.ApplicationContent, error) {
	for _, name := range names {
		data, err := fs.ReadFile(files, name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		text := string(data)
		return &v1beta1.ApplicationContent{Path: name, Content: &text}, nil
	}
	return nil, nil
}

func inspectPackageQuadlets(ctx context.Context, files fs.FS) Result {
	units, err := packageQuadletFiles(ctx, files)
	result := quadletResult(appspec.ParsePartialQuadletImageReferencesWithPodFiles(units, files))
	if err != nil && result.Complete() {
		result.Diagnostics = []Diagnostic{packageDiagnostic(err, DiagnosticParseFailed)}
	}
	return result
}

func packageQuadletFiles(ctx context.Context, files fs.FS) ([]v1beta1.ApplicationContent, error) {
	units, err := readPackageFiles(ctx, files, ".", func(name string) bool {
		_, supported := common.SupportedQuadletExtensions[filepath.Ext(name)]
		return supported
	})
	if err != nil {
		return units, err
	}
	dirs := make(map[string]struct{})
	for _, unit := range units {
		for _, dir := range quadlet.DropinDirectories(unit.Path) {
			dirs[dir] = struct{}{}
		}
	}
	for dir := range dirs {
		dropins, err := readPackageFiles(ctx, files, dir, func(name string) bool { return filepath.Ext(name) == ".conf" })
		units = append(units, dropins...)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return units, err
		}
	}
	return units, nil
}

func readPackageFiles(ctx context.Context, files fs.FS, dir string, match func(string) bool) ([]v1beta1.ApplicationContent, error) {
	entries, err := fs.ReadDir(files, dir)
	if err != nil {
		return nil, err
	}
	var contents []v1beta1.ApplicationContent
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return contents, err
		}
		if entry.IsDir() || !match(entry.Name()) {
			continue
		}
		name := filepath.Join(dir, entry.Name())
		data, err := fs.ReadFile(files, name)
		if err != nil {
			return contents, err
		}
		text := string(data)
		contents = append(contents, v1beta1.ApplicationContent{Path: name, Content: &text})
	}
	return contents, nil
}
