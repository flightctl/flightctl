# Managing Image Builds and Exports

> [!NOTE]
> The ImageBuild and ImageExport APIs use version `v1alpha1`. While no breaking changes are currently anticipated, the alpha designation indicates these APIs may evolve as the feature matures.

Flight Control provides API resources that automate the image building and export process. Instead of manually building images with `podman` and `bootc-image-builder`, you can use the `ImageBuild` and `ImageExport` resources to build and export images through the Flight Control service.

## Prerequisites

Before using the API-based image building, ensure:

* The Flight Control service is installed and running with the ImageBuilder API enabled
* You have OCI Repository resources configured for your source and destination registries (see [Managing Repositories](managing-repositories.md#oci-repositories))
* You have appropriate permissions to create ImageBuild and ImageExport resources

> [!NOTE]
> Only OCI repositories can be used with ImageBuild and ImageExport resources. Git, HTTP, and SSH repositories are not supported for image building operations.

## ImageBuild Resource

The `ImageBuild` resource automates the process of building bootc container images with the Flight Control agent embedded. It handles:

* Generating a Containerfile that includes the Flight Control agent
* Building the container image using podman
* Pushing the built image to your destination registry
* Optionally generating an SBOM (Syft, CycloneDX JSON), normalizing PURLs, pushing the SBOM as an OCI referrer, and uploading to Trustify when you enable those options in the ImageBuilder Worker configuration (see [Configuring the ImageBuilder Worker](../installing/configuring-imagebuilder.md#sbom-generation))
* Managing enrollment certificate binding (early or late)

### ImageBuild Specification

An `ImageBuild` resource has the following structure:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImageBuild
metadata:
  name: my-image-build
  labels:
    environment: production
spec:
  source:
    repository: quay-io                    # Name of Repository resource
    imageName: centos-bootc/centos-bootc   # Source image name
    imageTag: stream9                       # Source image tag
  destination:
    repository: my-registry                 # Name of Repository resource
    imageName: my-user/centos-bootc-custom  # Destination image name
    imageTag: v1.0.0                        # Destination image tag
  binding:
    type: late                              # or "early"
```

**Source Configuration:**

* `repository`: The name of a Repository resource of type `oci` that references your source container registry
* `imageName`: The container image name (path) in the registry
* `imageTag`: The tag of the source image to build from

**Destination Configuration:**

* `repository`: The name of a Repository resource of type `oci` with ReadWrite access
* `imageName`: The container image name (path) where the built image will be pushed
* `imageTag`: The tag to apply to the built image

**Binding Configuration:**

* `type: early`: Embeds enrollment certificate and configuration directly in the image. Devices using this image can automatically connect to Flight Control without additional provisioning.
* `type: late`: Builds the image without enrollment certificate. The certificate must be injected at provisioning time using cloud-init, Ignition, or similar mechanisms.

### Creating an ImageBuild

Create an ImageBuild resource using the Flight Control CLI:

```console
flightctl apply -f imagebuild.yaml
```

Or using the API directly:

```console
curl -X POST https://api.flightctl.example.com/api/v1/imagebuilds \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d @imagebuild.json
```

### Monitoring ImageBuild Status

The ImageBuild resource includes a `status` field that tracks the build progress:

* **Pending**: The build is queued and waiting to start
* **Building**: The Containerfile is being generated and the image is being built
* **Pushing**: The built image is being pushed to the destination registry
* **Completed**: The build completed successfully
* **Failed**: The build failed or timed out (check the message for details)
* **Canceling**: A cancellation has been requested and the build is being stopped
* **Canceled**: The build was canceled by user request

Query the status:

```console
flightctl get imagebuild my-image-build
```

The status includes:

* `conditions`: Array of condition objects showing the current state
* `imageReference`: The full image reference of the built image (populated on completion)

### Viewing ImageBuild Logs

View the logs for an ImageBuild resource:

```console
flightctl logs imagebuild/my-image-build
```

To follow logs in real-time while the build is running:

```console
flightctl logs imagebuild/my-image-build -f
```

### Canceling an ImageBuild

Cancel a running ImageBuild using the `cancel` command:

```console
flightctl cancel imagebuild/my-image-build
```

This initiates a graceful cancellation of the build. The status will transition to `Canceling` and then to `Canceled` once the build process has stopped.

> [!NOTE]
> Only builds in `Pending`, `Building`, or `Pushing` status can be canceled. Builds that have already completed, failed, or been canceled cannot be canceled again.

### Deleting an ImageBuild

Delete an ImageBuild using the `delete` command:

```console
flightctl delete imagebuild/my-image-build
```

When deleting an ImageBuild:

1. **Related ImageExports are deleted first**: Any ImageExport resources that reference this ImageBuild will be automatically deleted. Each related ImageExport will be canceled (if in progress) before deletion.
2. **The ImageBuild is canceled if in progress**: If the ImageBuild is currently running (Pending, Building, or Pushing), it will be canceled and the delete operation will wait for the cancellation to complete.
3. **The ImageBuild is deleted**: After all related resources are cleaned up, the ImageBuild itself is deleted.

> [!NOTE]
> Deleting an in-progress ImageBuild may take up to 30 seconds while waiting for the build to be canceled. If the cancellation does not complete within this timeout, the resource will still be deleted.

### Example: Early Binding ImageBuild

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImageBuild
metadata:
  name: centos-bootc-stream9-build-early
spec:
  source:
    repository: quay-io
    imageName: centos-bootc/centos-bootc
    imageTag: stream9
  destination:
    repository: my-registry
    imageName: my-user/centos-bootc-custom
    imageTag: v1.0.0-early
  binding:
    type: early
```

### Example: Late Binding ImageBuild

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImageBuild
metadata:
  name: centos-bootc-stream9-build
spec:
  source:
    repository: quay-io
    imageName: centos-bootc/centos-bootc
    imageTag: stream9
  destination:
    repository: my-registry
    imageName: my-user/centos-bootc-custom
    imageTag: v1.0.0
  binding:
    type: late
```

## ImageExport Resource

The `ImageExport` resource converts bootc container images into disk image formats (qcow2, vmdk, iso, etc.) suitable for provisioning physical or virtual devices. It uses `bootc-image-builder` under the hood to perform the conversion.

### ImageExport Specification

An `ImageExport` resource has the following structure:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImageExport
metadata:
  name: my-image-export
spec:
  source:
    type: imageBuild                    # Only imageBuild is supported
    imageBuildRef: my-image-build        # Name of the ImageBuild resource to export
  format: qcow2                          # Export format: qcow2, vmdk, iso, etc.
```

**Source Configuration:**

The source must reference an existing ImageBuild resource:

* `type: imageBuild` (required)
* `imageBuildRef`: The name of the ImageBuild resource to export
* If the referenced ImageBuild is not yet completed, the ImageExport will wait until the ImageBuild reaches `Completed` status before starting the export
* The destination for the exported image is automatically taken from the referenced ImageBuild's destination configuration

**Destination Configuration:**

The destination is automatically inherited from the referenced ImageBuild resource. You do not need to specify a destination in the ImageExport spec. The exported disk image will be pushed to the same repository, image name, and tag as specified in the ImageBuild destination.

The exported disk image artifact is pushed to the destination repository (from the referenced ImageBuild) as an [ORAS artifact](https://oras.land/) with a reference to the destination image.

**Format Configuration:**

* `format`: The disk image format to export. Supported formats include:
  * `qcow2`: QEMU disk image format (for OpenShift Virtualization, KVM, etc.)
  * `vmdk`: VMware disk image format
  * `iso`: ISO disk image format (for bare metal provisioning)
  * Other formats supported by `bootc-image-builder`

### Creating an ImageExport

Create an ImageExport resource using the Flight Control CLI:

```console
flightctl apply -f imageexport.yaml
```

Or using the API directly:

```console
curl -X POST https://api.flightctl.example.com/api/v1/imageexports \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d @imageexport.json
```

### Monitoring ImageExport Status

The ImageExport resource includes a `status` field that tracks the export progress:

* **Pending**: The export is queued and waiting to start
* **Converting**: The image is being converted to the target format
* **Pushing**: The exported image is being pushed to the destination registry
* **Completed**: The export completed successfully
* **Failed**: The export failed or timed out (check the message for details)
* **Canceling**: A cancellation has been requested and the export is being stopped
* **Canceled**: The export was canceled by user request

Query the status:

```console
flightctl get imageexport my-image-export
```

### Viewing ImageExport Logs

View the logs for an ImageExport resource:

```console
flightctl logs imageexport/my-image-export
```

To follow logs in real-time while the export is running:

```console
flightctl logs imageexport/my-image-export -f
```

### Canceling an ImageExport

Cancel a running ImageExport using the `cancel` command:

```console
flightctl cancel imageexport/my-image-export
```

This initiates a graceful cancellation of the export. The status will transition to `Canceling` and then to `Canceled` once the export process has stopped.

> [!NOTE]
> Only exports in `Pending`, `Converting`, or `Pushing` status can be canceled. Exports that have already completed, failed, or been canceled cannot be canceled again.

### Deleting an ImageExport

Delete an ImageExport using the `delete` command:

```console
flightctl delete imageexport/my-image-export
```

If the ImageExport is currently in progress (Pending, Converting, or Pushing), the delete operation will automatically cancel the export first and wait for the cancellation to complete before deleting the resource. This ensures clean shutdown of any running export processes.

> [!NOTE]
> Deleting an in-progress ImageExport may take up to 30 seconds while waiting for the export to be canceled. If the cancellation does not complete within this timeout, the resource will still be deleted.

### Downloading the Exported Image

Once the ImageExport reaches `Completed` status, download the disk image artifact using the `flightctl download` command:

```console
flightctl download imageexport/my-image-export ./my-image.qcow2
```

This downloads the exported disk image directly to a local file with progress indication. The command supports all export formats (qcow2, vmdk, iso, etc.).

### Example: Export from ImageBuild

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImageExport
metadata:
  name: my-edge-export-from-build
spec:
  source:
    type: imageBuild
    imageBuildRef: my-edge-image-build-2
  format: qcow2
```

> [!NOTE]
> The destination is automatically taken from the referenced ImageBuild resource. You do not need to specify a destination in the ImageExport spec.

## ImagePromotion Resource

> [!NOTE]
> The ImagePromotion API uses version `v1alpha1`. While no breaking changes are currently anticipated, the alpha designation indicates this API may evolve as the feature matures.

The `ImagePromotion` resource publishes a completed image build to the Flight Control [software catalog](managing-catalogs.md). Creating an `ImagePromotion` links the container image produced by an `ImageBuild` (and any requested disk image export formats) to a catalog item version, making it available for devices to consume.

When an `ImagePromotion` is created, Flight Control:

1. Waits for the source `ImageBuild` to complete, along with any requested `ImageExport` artifacts.
2. Publishes the artifacts to the target catalog item.

A promotion can either create a new catalog item or add a version to an existing one. When adding a version, you can provide upgrade metadata such as the version this release replaces and any intermediate versions that can be skipped.

### ImagePromotion specification

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImagePromotion
metadata:
  name: my-image-promotion
spec:
  source:
    imageBuildRef: my-image-build      # Name of the ImageBuild resource to promote
    exportFormats:                     # Optional: disk image formats to include
      - qcow2
      - iso
  target:
    type: ExistingCatalogItem          # NewCatalogItem or ExistingCatalogItem
    catalogName: platform-images       # Name of the Catalog resource
    catalogItemName: centos-bootc      # Name of the catalog item
    version: 1.2.1                     # Semantic version string
    replaces: 1.2.0                    # Optional: version this release supersedes
    skips:                             # Optional: individual versions to skip
      - 1.1.5
    skipRange: ">=1.0.0 <1.2.0"       # Optional: semver range of skippable versions
    readme: |                          # Optional: markdown documentation
      This release updates the base image to the latest packages.
```

**Source fields:**

| Field | Required | Description |
| --------------- | -------- | ------------------------------------------------------------------------------------------------------------------------- |
| `imageBuildRef` | Yes | Name of the `ImageBuild` resource whose container image is promoted. The container artifact is always included. |
| `exportFormats` | No | List of disk image formats to include (for example, `qcow2`, `iso`, `vmdk`). The promotion waits for a successful `ImageExport` resource for each requested format before publishing. |

**Target fields:**

| Field | Required | Description |
| ---------------- | -------- | ---------------------------------------------------------------------------------------------------------------------- |
| `type` | Yes | `NewCatalogItem` to create a new catalog item, or `ExistingCatalogItem` to add a version to an existing item. |
| `catalogName` | Yes | Name of the `Catalog` resource to publish to. |
| `catalogItemName`| Yes | Name of the catalog item to create or update. Must be a valid DNS subdomain. |
| `version` | Yes | Semantic version string for this release (for example, `1.2.1`). |
| `replaces` | No | The version this release supersedes, defining the primary upgrade edge. Applies only to `ExistingCatalogItem`. |
| `skips` | No | List of specific versions that can upgrade directly to this one without passing through intermediate versions. Applies only to `ExistingCatalogItem`. |
| `skipRange` | No | Semver range expression of versions that can upgrade directly to this version (for example, `">=1.0.0 <1.2.0"`). Applies only to `ExistingCatalogItem`. |
| `readme` | No | Markdown-formatted release notes or documentation shown in the catalog UI. |

### Creating an ImagePromotion

Create an `ImagePromotion` resource using the Flight Control CLI:

```console
flightctl apply -f imagepromotion.yaml
```

If the source `ImageBuild` has not yet completed, the promotion waits automatically. You do not need to create the promotion after the build finishes.

### Monitoring ImagePromotion status

Query the status of a promotion:

```console
flightctl get imagepromotion my-image-promotion
```

The `status.conditions` field reports the current state:

| Status | Description |
| ----------------------- | ----------------------------------------------------------------------- |
| `WaitingForArtifacts` | Waiting for the image build or requested export artifacts to complete. |
| `Publishing` | Artifacts are being published to the catalog. |
| `Completed` | The promotion completed successfully. The catalog item version is available. |
| `Failed` | The promotion failed. Check the status message for details. |
| `BuildFailed` | The source image build failed; the promotion cannot proceed. |
| `BuildCanceled` | The source image build was canceled. |
| `AmendmentFailed` | Adding export formats to an already-published promotion failed. |

When the promotion reaches `Completed`, `status.publishedAt` records the time the catalog item version was first published. If export formats are added later, `status.lastAmendedAt` records the time of the most recent amendment.

### Adding export formats to a published promotion

After a promotion is published, you can add more disk image export formats without creating a new promotion. Export formats are append-only—existing formats cannot be removed.

To add formats, update the `spec.source.exportFormats` field in your resource definition to include the complete updated list, then apply it:

```console
flightctl apply -f imagepromotion.yaml
```

The promotion transitions back through `WaitingForArtifacts` and `Publishing` until the new export artifacts are available, then returns to `Completed`.

> [!NOTE]
> Adding export formats is only allowed when the promotion is in `Completed` status. Promotions in `Failed`, `Publishing`, or other non-completed states cannot be amended.

### Deleting an ImagePromotion

Delete an `ImagePromotion` using the `delete` command:

```console
flightctl delete imagepromotion my-image-promotion
```

Deleting an `ImagePromotion` removes the promotion record only. If the promotion has already been published, the catalog item version that was published is not affected.

### Example: Create a new catalog item

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImagePromotion
metadata:
  name: centos-stream9-v1-0-0
spec:
  source:
    imageBuildRef: centos-stream9-build
    exportFormats:
      - qcow2
  target:
    type: NewCatalogItem
    catalogName: platform-images
    catalogItemName: centos-stream9
    version: 1.0.0
    readme: |
      Initial release of the CentOS Stream 9 edge image.
```

### Example: Add a version to an existing catalog item

```yaml
apiVersion: flightctl.io/v1alpha1
kind: ImagePromotion
metadata:
  name: centos-stream9-v1-1-0
spec:
  source:
    imageBuildRef: centos-stream9-build-v1-1
    exportFormats:
      - qcow2
      - iso
  target:
    type: ExistingCatalogItem
    catalogName: platform-images
    catalogItemName: centos-stream9
    version: 1.1.0
    replaces: 1.0.0
    readme: |
      Updates the base image to the latest CentOS Stream 9 packages.
```

## Workflow: Building and Exporting Images

A typical workflow using ImageBuild and ImageExport resources:

1. **Create OCI Repository resources** for your source and destination registries (see [Configuring OCI Repositories](managing-repositories.md))
2. **Create an ImageBuild resource** to build your bootc image with the Flight Control agent
3. **Monitor the ImageBuild** using `flightctl get imagebuild NAME` or follow logs with `flightctl logs imagebuild/NAME -f`
4. **Create an ImageExport resource** referencing the completed ImageBuild
5. **Monitor the ImageExport** using `flightctl get imageexport NAME` or follow logs with `flightctl logs imageexport/NAME -f`
6. **Download the exported disk image** using `flightctl download imageexport/NAME ./output-file`
7. **Use the exported disk image** to provision devices

## API vs Manual Building

The Flight Control API provides a simple approach for image creation that automates the Containerfile generation and build process. For more advanced use cases, manual image building is required.

Both approaches produce the same result: bootc container images and disk images that can be used to provision devices managed by Flight Control.

For information on manual image building, see [Building OS Images](../building/building-images.md).
