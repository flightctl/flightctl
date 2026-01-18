# Managing Image Builds and Exports

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
apiVersion: flightctl.io/v1beta1
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
    tag: v1.0.0                             # Destination tag
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
* `tag`: The tag to apply to the built image

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
* **Failed**: The build failed (check the message for details)

Query the status:

```console
flightctl get imagebuild my-image-build
```

The status includes:

* `conditions`: Array of condition objects showing the current state
* `imageReference`: The full image reference of the built image (populated on completion)

### Example: Early Binding ImageBuild

```yaml
apiVersion: flightctl.io/v1beta1
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
    tag: v1.0.0-early
  binding:
    type: early
```

### Example: Late Binding ImageBuild

```yaml
apiVersion: flightctl.io/v1beta1
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
    tag: v1.0.0
  binding:
    type: late
```

## ImageExport Resource

The `ImageExport` resource converts bootc container images into disk image formats (qcow2, vmdk, iso, etc.) suitable for provisioning physical or virtual devices. It uses `bootc-image-builder` under the hood to perform the conversion.

### ImageExport Specification

An `ImageExport` resource has the following structure:

```yaml
apiVersion: flightctl.io/v1beta1
kind: ImageExport
metadata:
  name: my-image-export
spec:
  source:
    type: imageBuild                    # or "imageReference"
    imageBuildRef: my-image-build        # Required if type is "imageBuild"
    # OR for imageReference:
    # repository: my-source-registry
    # imageName: rhel-edge-base
    # imageTag: "9.4"
  destination:
    repository: my-export-registry       # Name of Repository resource
    imageName: rhel-edge-exported        # Destination image name
    tag: v1.0.0                          # Destination tag
  format: qcow2                          # Export format: qcow2, vmdk, iso, etc.
  tagSuffix: "-qcow2"                    # Optional suffix to append to tag
```

**Source Configuration:**
The source can be specified in two ways:

1. **From ImageBuild**: Reference an existing ImageBuild resource
   * `type: imageBuild`
   * `imageBuildRef`: The name of the ImageBuild resource to export
   * If the referenced ImageBuild is not yet completed, the ImageExport will wait until the ImageBuild reaches `Completed` status before starting the export

2. **From Image Reference**: Reference an image directly from a registry
   * `type: imageReference`
   * `repository`: The name of an OCI Repository resource
   * `imageName`: The container image name
   * `imageTag`: The image tag

**Destination Configuration:**

* `repository`: The name of a Repository resource of type `oci` with ReadWrite access
* `imageName`: The image name where the exported disk image will be pushed
* `tag`: The tag to apply to the exported image

The exported disk image artifact is pushed to the destination repository as an [ORAS artifact](https://oras.land/) with a reference to the provided destination image. To retrieve the exported artifact, use the `oras discover` command:

```console
oras discover quay.io/my-user/centos-bootc-custom:v1.1.0-early --distribution-spec v1.1-referrers-api --platform linux/arm64
```

This will show the exported disk image artifacts associated with the destination image reference.

**Format Configuration:**

* `format`: The disk image format to export. Supported formats include:
  * `qcow2`: QEMU disk image format (for OpenShift Virtualization, KVM, etc.)
  * `vmdk`: VMware disk image format
  * `iso`: ISO disk image format (for bare metal provisioning)
  * Other formats supported by `bootc-image-builder`
* `tagSuffix`: Optional suffix to append to the destination tag (e.g., `-qcow2`, `-vmware`)

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
* **Failed**: The export failed (check the message for details)

Query the status:

```console
flightctl get imageexport my-image-export
```

### Example: Export from ImageBuild

```yaml
apiVersion: flightctl.io/v1beta1
kind: ImageExport
metadata:
  name: my-edge-export-from-build
spec:
  source:
    type: imageBuild
    imageBuildRef: my-edge-image-build-2
  destination:
    repository: my-export-registry
    imageName: rhel-edge-exported
    tag: v1.0.0
  format: qcow2
  tagSuffix: "-qcow2"
```

### Example: Export from Image Reference

```yaml
apiVersion: flightctl.io/v1beta1
kind: ImageExport
metadata:
  name: my-edge-export-from-registry
spec:
  source:
    type: imageReference
    repository: my-source-registry
    imageName: rhel-edge-base
    imageTag: "9.4"
  destination:
    repository: my-export-registry
    imageName: rhel-edge-exported
    tag: v2.0.0
  format: vmdk
  tagSuffix: "-vmware"
```

## Workflow: Building and Exporting Images

A typical workflow using ImageBuild and ImageExport resources:

1. **Create OCI Repository resources** for your source and destination registries (see [Configuring OCI Repositories](managing-repositories.md))
2. **Create an ImageBuild resource** to build your bootc image with the Flight Control agent
3. **Monitor the ImageBuild** until it reaches `Completed` status
4. **Create an ImageExport resource** referencing the completed ImageBuild
5. **Monitor the ImageExport** until it reaches `Completed` status
6. **Use the exported disk image** to provision devices

## API vs Manual Building

The Flight Control API provides a simple approach for image creation that automates the Containerfile generation and build process. For more advanced use cases, manual image building is required.

Both approaches produce the same result: bootc container images and disk images that can be used to provision devices managed by Flight Control.

For information on manual image building, see [Building OS Images](../building/building-images.md).
