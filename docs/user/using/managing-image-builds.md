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
