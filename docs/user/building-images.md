# Building OS Images

## Understanding OS Images and the Image Build Process

Image-based OSes allow the whole OS (and optionally also OS configuration and applications) to be versioned, deployed, and updated as a single unit. This reduces operational risk:

* It minimizes potential drift between what has been thoroughly tested and what is deployed to a large number of devices.
* It minimizes the risk of failed updates that require expensive maintenance or replacement through transactional updates and rollbacks.

Flight Control initially focuses on image-based Linux OSes running [bootable container images (bootc)](https://containers.github.io/bootc/), with support for [ostree](https://ostreedev.github.io/ostree/) and [rpm-ostree](https://coreos.github.io/rpm-ostree/) images planned for later. It does not update package-based OSes.

At a high level, the image building process for bootc works as follows:

1. Choose a base bootc image (for example Fedora, CentOS, or RHEL).
2. Create a Containerfile that layers onto that base image
    * the Flight Control agent and configuration,
    * (optionally) any drivers specific to your target deployment environment, and
    * (optionally) host configuration (e.g. CA bundles) and application workloads common to all deployments from this image.
3. Build, publish, and sign an **OS image (bootc)** using `podman` and `skopeo`.
4. Build, publish, and sign an **OS disk image** using `bootc-image-builder` (bib) and `skopeo`.

<picture>
  <source media="(prefers-color-scheme: light)" srcset="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/image-building.svg">
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/image-building-dark.svg">
  <img alt="Diagram of image building process" src="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/image-building.svg">
</picture>

The OS disk image is used to image (or "flash") a device when it is provisioned. For subsequent device updates, only the OS image (bootc) is required. This is because bootc is a *file system* image, that is it contains just the files in the file system including their attributes, but the disk layout (partitions, volumes) and file systems need to have been created first. The OS disk image includes everything, the disk layout, bootloader, file systems, and the files in the OS image (bootc). It can therefore be written verbatim to the device's drive.

## Building and Publishing OS Images and Disk Images

This section describes the generic process for building an OS image (bootc) that contains the Flight Control agent and building an OS disk image for flashing to physical devices. Later sections then describe considerations when building for specific virtualization and bare metal provisioning environments.

Before you start, ensure you have installed the following prerequisites:

* `flightctl` CLI latest version ([installation guide](gettting-started.md#-installing-the-flight-control-cli))
* `podman` version 5.0 or higher ([installation guide](https://podman.io/docs/installation))
* `skopeo` version 1.14 or higher ([installation guide](https://github.com/containers/skopeo/blob/main/install.md))
* `bootc-image-builder` latest version ([installation guide](https://github.com/osbuild/bootc-image-builder))

### Choosing an Enrollment Method

When the Flight Control agent starts, it expects to find its configuration in `/etc/flightctl/config.yaml`. This configuration needs to contain:

* the Flight Control enrollment service to connect to (enrollment endpoint),
* the X.509 client certificate and key to connect with (enrollment certificate),
* optionally, any further agent configuration (see [Configuring the Flight Control Agent](configuring-agent.md)).

You have multiple options how and when to provision the enrollment endpoint and certificate to the device:

* You can build the OS image including enrollment endpoint and certificate (*"early binding"*).

  Devices using this image can automatically connect to "their" Flight Control service to request enrollment, without depending on any provisioning infrastructure. On the other hand, devices are bound to a specific service and owner. They also share the same, typically long-lived X.509 client certificate for connecting to the enrollment service.

* You can build the OS image without enrollment endpoint and certificate, but inject these at provisioning-time (*"late binding"*).

  Devices using this image are not bound to a single owner or service and can have device-specific, short-lived X.509 client certificates for connecting to the enrollment service. However, this requires the presence of virtualization or bare metal provisioning infrastructure that can request device-specific enrollment endpoints and certificates from Flight Control and inject these using mechanisms like [cloud-init](https://cloud-init.io/), [Ignition](https://coreos.github.io/ignition/supported-platforms/), or [kickstart](https://anaconda-installer.readthedocs.io/en/latest/kickstart.html).

* You can build the OS image including the agent configuration including the enrollment endpoint, but inject the enrollment certificate at provisioning-time.

> [!NOTE]
> The enrollment certificate is only used to secure the network connection for submitting an enrollment request. It is not involved in the actual verification or approval of the enrollment request. It is also no longer used with enrolled devices, as these rely on device-specific management certificates instead.

The following procedure describes the early binding method of building the agent configuration including the enrollment endpoint and certificate into the image.

### Requesting an Enrollment Certificate

Use the `flightctl` CLI to authenticate with the Flight Control service, then run the following command to obtain enrollment credentials with a validity of one year, in the format of an agent configuration file:

```console
flightctl certificate request --cert-type=enrollment --expiration=365d --output-format=embedded > config.yaml
```

The returned `config.yaml` contains the URLs of the Flight Control service, its CA bundle, and the enrollment client certificate and key for the agent. It should look similar to this:

```yaml
enrollment-service:
  authentication:
    client-certificate-data: LS0tLS1CRUdJTiBD...
    client-key-data: LS0tLS1CRUdJTiBF...
  service:
    certificate-authority-data: LS0tLS1CRUdJTiBD...
    server: https://agent-api.flightctl.127.0.0.1.nip.io:7443
  enrollment-ui-endpoint: https://ui.flightctl.127.0.0.1.nip.io:8080
  grpc-management-endpoint: grpcs://agent-grpc.flightctl.127.0.0.1.nip.io:7444
```

### Building the OS Image (bootc)

Create a file named `Containerfile` with the following content to build an OS image based on CentOS Stream 9 that includes the Flight Control agent and configuration:

```console
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y copr enable @redhat-et/flightctl centos-stream-9-x86_64 && \
    dnf -y install flightctl-agent && \
    dnf -y clean all && \
    systemctl enable flightctl-agent.service

ADD config.yaml /etc/flightctl/
```

> [!NOTE]
> If you have used Podman or Docker before to build application containers, you will notice this is a regular `Containerfile`, with the only difference that the base image referenced in `FROM` is bootable container (bootc) image. That means it already contains a Linux kernel. This allows you to reuse existing standard container build tools and workflows.

> [!IMPORTANT]
> When using Flight Control with a RHEL 9 base image, you need to disable the default automatic updates by adding the following command to the `Containerfile`:
>
> ```console
> RUN systemctl mask bootc-fetch-apply-updates.timer
> ```

Define the OCI registry, image repository, and image tag you want to use (ensure you have write-permissions to that repository):

```console
OCI_REGISTRY=quay.io
OCI_IMAGE_REPO=${OCI_REGISTRY}/your_org/centos-bootc-flightctl
OCI_IMAGE_TAG=v1
```

Build the OS image for your target platform:

```console
sudo podman build -t ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG} .
```

### Signing and Publishing the OS Image (bootc)

There are several methods for signing container images. We will focus on signing with [Sigstore](https://www.sigstore.dev/) signatures using a private key. For other options, refer to the [RHEL](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/9/html/building_running_and_managing_containers/assembly_signing-container-images_building-running-and-managing-containers) or [cosign](https://github.com/sigstore/cosign) documentations.

Generate a Sigstore key pair `signingkey.pub` and `signingkey.private`:

```console
skopeo generate-sigstore-key --output-prefix signingkey
```

Configure container tools like Podman and Skopeo to upload Sigstore signatures together with your signed image to your OCI registry:

```console
sudo tee "/etc/containers/registries.d/${OCI_REGISTRY}.yaml" > /dev/null <<EOF
docker:
    ${OCI_REGISTRY}:
        use-sigstore-attachments: true
EOF
```

Log in to your OCI registry, then sign and publish the OS image:

```console
sudo podman login ${OCI_REGISTRY}
sudo podman push --sign-by-sigstore-private-key ./signingkey.private ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

### Building the OS Disk Image

Next, create a directory called "output" and use `bootc-image-builder` to generate an OS disk image of type "raw" from your OS image:

```console
mkdir -p output

sudo podman run --rm -it --privileged --pull=newer \
    --security-opt label=type:unconfined_t \
    -v $(pwd)/output:/output \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type raw \
    ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

Once `bootc-image-builder` completes, you can find the disk image under `$(pwd)/output/image/disk.raw`.

Refer to `bootc-image-builder`'s [list of image types](https://github.com/osbuild/bootc-image-builder?tab=readme-ov-file#-image-types) for other supported types.

### Signing and Publishing the OS Disk Image (bootc)

Optionally, you can compress, sign, and publish your disk image to your OCI registry, too. This helps unify hosting and distribution. Using manifest lists, you can even keep matching bootc and disk images together:

```console
sudo podman manifest create \
    ${OCI_IMAGE_REPO}-unified:${OCI_IMAGE_TAG}

sudo podman manifest add \
    ${OCI_IMAGE_REPO}-unified:${OCI_IMAGE_TAG} \
    docker://${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}

gzip $(pwd)/output/image/disk.raw

sudo podman manifest add \
    --artifact --artifact-type application/vnd.diskimage.raw.gzip \
    --arch=amd64 --os=linux \
    ${OCI_IMAGE_REPO}-unified:${OCI_IMAGE_TAG} \
    $(pwd)/output/image/disk.raw.gz

sudo podman manifest push --all \
     --sign-by-sigstore-private-key ./signingkey.private \
    ${OCI_IMAGE_REPO}-unified:${OCI_IMAGE_TAG} \
    docker://${OCI_IMAGE_REPO}-unified:${OCI_IMAGE_TAG}
```

### Further References

For further information and practical examples, refer to:

* Example images in the [Flight Control demos repository](https://github.com/flightctl/flightctl-demos).
* Flight Control demos repository's [automated build pipeline](https://github.com/flightctl/flightctl-demos/blob/main/.github/workflows/build-bootc.yaml).
* The Fedora/CentOS bootc project's [community-provided examples](https://gitlab.com/fedora/bootc/examples).

## Considerations for Specific Target Platforms

### Red Hat OpenShift Container Native Virtualization (CNV)

### Red Hat Satellite

### VMware vSphere

When building OS images and disk images for VMware vSphere, follow the [generic process](#building-and-publishing-os-images-and-disk-images) with the following changes:

1. Use late binding of the enrollment endpoint and enrollment certificates, injecting the enrollment certificate or even the whole agent configuration through `cloud-init` when provisioning the virtual device.
2. Optionally, add the `open-vm-tools` guest tools to the image.
3. Build a disk image of type "vmdk" instead of "raw".

Create a file named `Containerfile` with the following content to build an OS image based on CentOS Stream 9 that includes the Flight Control agent and VMware guest tools, but no agent configuration:

```console
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y copr enable @redhat-et/flightctl centos-stream-9-x86_64 && \
    dnf -y install flightctl-agent; \
    dnf -y clean all; \
    systemctl enable flightctl-agent.service

RUN dnf -y install cloud-init open-vm-tools; \
    dnf -y clean all; \
    ln -s ../cloud-init.target /usr/lib/systemd/system/default.target.wants && \
    systemctl enable vmtoolsd.service
```

Build the OS image (bootc) in the [generic process](#building-and-publishing-os-images-and-disk-images), but build an image of type "vmdk" instead of "raw":

```console
mkdir -p output

sudo podman run --rm -it --privileged --pull=newer \
    --security-opt label=type:unconfined_t \
    -v $(pwd)/output:/output \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type vmdk \
    ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

Once `bootc-image-builder` completes, you can find the disk image under `$(pwd)/output/vmdk/disk.vmdk`.

## Best Practices When Building Images

### Prefer Build-time Configuration over Dynamic Runtime Configuration

Prefer adding configuration to the OS image itself at build-time, so they get tested, distributed, and updated together ("lifecycle-bound"). There are cases when this is not feasible or desirable, in which case use Flight Control's approach to dynamically configure devices at runtime instead:

* configuration that is deployment- or site-specific (e.g. a hostname or a site-specific network credential)
* secrets that are not secure to distribute via the image
* application workloads that need to be added, updated, or deleted without reboot or on a faster cadence than the OS

### Prefer Configuration in /usr over /etc

Prefer placing  configuration files under `/usr` if the configuration is static and the application or service supports it. This way, configuration remains read-only and fully defined by the image (avoiding ostree's 3-way merge of `/etc` as a potential source of drift). There are cases when this is not feasible or desirable:

* the configuration is deployment- or site-specific
* the application or service only supports reading configuration from `/etc`
* the configuration may need to be changed at runtime (although most applications allow configuration in `/etc` to override that in `/usr`)

### Use Drop-in Directories

Avoid editing configuration files, as this is a source of drift and hard to use with declarative configuration management. Instead, most system services support "drop-in directories" (recognized by the `.d/` at the end of the name) where you can "drop-in" (or replace or remove) configuration files that the service aggregates together. Examples: `/etc/containers/certs.d`, `/etc/cron.d`, `/etc/NetworkManager/conf.d`.

### Avoid Mutating the File System Out-of-Band

Avoid executing scripts or commands that change the file system as a side-effect, as these may be overwritten by bootc or Flight Control or may lead to drift or failed integrity checks. Instead, run such scripts or commands during image-building, so changes become part of the image or use Flight Control's configuration management mechanisms instead.

### Further References

See also the guidance in the [bootc documentation](https://containers.github.io/bootc/building/guidance.html).
