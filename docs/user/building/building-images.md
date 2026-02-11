# Building OS Images

## Understanding OS Images and the Image Build Process

Image-based OSes allow the whole OS (and optionally also OS configuration and applications) to be versioned, deployed, and updated as a single unit. This reduces operational risk:

* It minimizes potential drift between what has been thoroughly tested and what is deployed to a large number of devices.
* It minimizes the risk of failed updates that require expensive maintenance or replacement through transactional updates and rollbacks.

Flight Control initially focuses on image-based Linux OSes running [bootable container images (bootc)](https://bootc-dev.github.io/bootc/), with support for [ostree](https://ostreedev.github.io/ostree/) and [rpm-ostree](https://coreos.github.io/rpm-ostree/) images planned for later. It does not update package-based OSes.

At a high level, the image building process for bootc works as follows:

1. Choose a base bootc image (for example Fedora, CentOS, or RHEL).
2. Create a Containerfile that layers onto that base image
    * the Flight Control agent and configuration,
    * (optionally) any drivers specific to your target deployment environment, and
    * (optionally) host configuration (e.g. CA bundles) and application workloads common to all deployments from this image.
3. Build, publish, and sign an **OS image (bootc)** using `podman` and `skopeo`.
4. Build, publish, and sign an **OS disk image** using `bootc-image-builder` (bib) and `skopeo`.

<picture>
  <source media="(prefers-color-scheme: light)" srcset="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/image-building.svg"/>
  <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/image-building-dark.svg"/>
  <img alt="Diagram of image building process" src="https://raw.githubusercontent.com/flightctl/flightctl/main/docs/images/image-building.svg"/>
</picture>

The OS disk image is used to image (or "flash") a device when it is provisioned. For subsequent device updates, only the OS image (bootc) is required. This is because bootc is a *file system* image, that is it contains just the files in the file system including their attributes, but the disk layout (partitions, volumes) and file systems need to have been created first. The OS disk image includes everything, the disk layout, bootloader, file systems, and the files in the OS image (bootc). It can therefore be written verbatim to the device's drive.

## Building and Publishing OS Images and Disk Images

This section describes the generic process for building an OS image (bootc) that contains the Flight Control agent and building an OS disk image for flashing to physical devices. Later sections then describe considerations when building for specific virtualization and bare metal provisioning environments.

> [!NOTE]
> For basic images, you can use Flight Control's built-in [ImageBuild and ImageExport services](../using/managing-image-builds.md) to automate the image building and export process through the API.

Before you start, ensure you have installed the following prerequisites:

* `flightctl` CLI latest version ([installation guide](../installing/installing-cli.md))
* `podman` version 5.0 or higher ([installation guide](https://podman.io/docs/installation))
* `skopeo` version 1.14 or higher ([installation guide](https://github.com/containers/skopeo/blob/main/install.md))
* `container-selinux` version 2.241 or higher (required by `bootc-image-builder`)

### Choosing an Enrollment Method

When the Flight Control agent starts, it expects to find its configuration in `/etc/flightctl/config.yaml`. This configuration needs to contain:

* the Flight Control enrollment service to connect to (enrollment endpoint),
* the X.509 client certificate and key to connect with (enrollment certificate),
* optionally, any further agent configuration (see [Installing the Flight Control Agent](../installing/installing-agent.md)).

You can provision the enrollment endpoint and certificate to the device in the following ways:

* **Early binding:** You can build an OS image that includes both the enrollment endpoint and certificate.

  Devices using this image can automatically connect to "their" Flight Control service to request enrollment, without depending on any provisioning infrastructure. On the other hand, devices are bound to a specific service and owner. They also share the same, typically long-lived X.509 client certificate for connecting to the enrollment service.

* **Late binding:** You can build an OS image without enrollment endpoint and certificate and instead inject both at provisioning-time.

  Devices using this image are not bound to a single owner or service and can have device-specific, short-lived X.509 client certificates for connecting to the enrollment service. However, late binding requires virtualization or bare metal provisioning infrastructure that can request device-specific enrollment endpoints and certificates from Flight Control and inject them into the provisioned device using mechanisms such as [cloud-init](https://cloud-init.io/), [Ignition](https://coreos.github.io/ignition/supported-platforms/), or [kickstart](https://anaconda-installer.readthedocs.io/en/latest/kickstart.html).

> [!NOTE]
> The enrollment certificate is only used to secure the network connection for submitting an enrollment request. It is not involved in the actual verification or approval of the enrollment request. It is also no longer used with enrolled devices, as these rely on device-specific management certificates instead.

The following procedure describes the early binding method of building the agent configuration including the enrollment endpoint and certificate into the image.

### Requesting an Enrollment Certificate

Use the `flightctl` CLI to authenticate with the Flight Control service, then run the following command to obtain enrollment credentials with a validity of one year, in the format of an agent configuration file:

```console
flightctl certificate request --signer=enrollment --expiration=365d --output=embedded > config.yaml
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
  enrollment-ui-endpoint: https://ui.flightctl.127.0.0.1.nip.io:8081
```

### Building the OS Image (bootc)

Create a file named `Containerfile` with the following content to build an OS image based on CentOS Stream 9 that includes the Flight Control agent and configuration:

```console
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo && \
    dnf -y install flightctl-agent && \
    dnf -y clean all && \
    systemctl enable flightctl-agent.service

# Optional: To enable podman-compose application support, uncomment below
# RUN dnf -y install epel-release && \
#     dnf -y install podman-compose && \
#     dnf -y clean all && \
#     systemctl enable podman.service

ADD config.yaml /etc/flightctl/
```

> [!NOTE]
> If you have used Podman or Docker before to build application containers, you will notice this is a regular `Containerfile`, with the only difference that the base image referenced in `FROM` is bootable container (bootc) image. That means it already contains a Linux kernel. This allows you to reuse existing standard container build tools and workflows.

> [!NOTE]
If your device relies on an OS image from a private repository, [authentication credentials](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/9/html-single/using_image_mode_for_rhel_to_build_deploy_and_manage_operating_systems/index#configuring-container-pull-secrets_managing-users-groups-ssh-key-and-secrets-in-image-mode-for-rhel) (pull secrets) must be placed in the appropriate system path `/etc/ostree/auth.json`. Authentication must exist on the device before it can be consumed.

Define the OCI registry, image repository, and image tag you want to use (ensure you have write-permissions to that repository):

```console
OCI_REGISTRY=quay.io
OCI_IMAGE_REPO=${OCI_REGISTRY}/your_org/centos-bootc
OCI_IMAGE_TAG=v1
```

Build the OS image for your target platform:

```console
sudo podman build -t ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG} .
```

#### Using RHEL base images

When using Flight Control with a RHEL 9 base image, you need to make a few changes to the `Containerfile`, specifically you need to disable RHEL's default automatic updates and use a different command to enable the EPEL repository in case you need `podman-compose`:

```console
FROM registry.redhat.io/rhel9/rhel-bootc:9.5

RUN dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo && \
    dnf -y install flightctl-agent && \
    dnf -y clean all && \
    systemctl enable flightctl-agent.service && \
    systemctl mask bootc-fetch-apply-updates.timer

# Optional: To enable podman-compose application support, uncomment below
# RUN dnf -y install https://dl.fedoraproject.org/pub/epel/epel-release-latest-9.noarch.rpm && \
#     dnf -y install podman-compose && \
#     dnf -y clean all && \
#     rm -rf /var/{cache,log} /var/lib/{dnf,rhsm} && \
#     systemctl enable podman.service

ADD config.yaml /etc/flightctl/
```

> [!IMPORTANT]
> To build RHEL-based bootc images, the build host itself must be a registered RHEL or Fedora system
> that has access to Red Hat content through subscription-manager.
>
> You also need to log in to the Red Hat registry before building your image:
>
> ```console
> sudo podman login registry.redhat.io
> ```

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

Next, create a directory called "output" and use `bootc-image-builder` to generate an OS disk image of type `iso` from your OS image:

```console
mkdir -p output

sudo podman run --rm -it --privileged --pull=newer \
    --security-opt label=type:unconfined_t \
    -v "${PWD}/output":/output \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type iso \
    ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

Once `bootc-image-builder` completes, you can find the ISO disk image under `${PWD}/output/bootiso/install.iso`.

Refer to `bootc-image-builder`'s [list of image types](https://github.com/osbuild/bootc-image-builder?tab=readme-ov-file#-image-types) for other supported types.

### Optional: Signing and Publishing the OS Disk Image to an OCI Registry

Optionally, you can compress, sign, and publish your disk image as so-called "OCI artifacts" to your OCI registry, too. This helps unify hosting and distribution. For example, to publish your ISO disk image to a repository named after your bootc image with `/diskimage-iso` appended, run the following commands:

```console
sudo chown -R $(whoami):$(whoami) "${PWD}/output"

OCI_DISK_IMAGE_REPO=${OCI_IMAGE_REPO}/diskimage-iso

sudo podman manifest create \
    ${OCI_DISK_IMAGE_REPO}:${OCI_IMAGE_TAG}

sudo podman manifest add \
    --artifact --artifact-type application/vnd.diskimage.iso \
    --arch=amd64 --os=linux \
    ${OCI_DISK_IMAGE_REPO}:${OCI_IMAGE_TAG} \
    "${PWD}/output/bootiso/install.iso"

sudo podman manifest push --all \
     --sign-by-sigstore-private-key ./signingkey.private \
    ${OCI_DISK_IMAGE_REPO}:${OCI_IMAGE_TAG} \
    docker://${OCI_DISK_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

### Configuring devices to verify image signatures

On the device, the Flight Control agent pulls OS images by invoking the **Podman CLI** (the `podman` binary) when applying updates; nothing else on the device performs those pulls. The agent does not verify image signatures itself—verification is enforced by Podman when it is configured with a signature policy. So you must configure the host so that when the agent runs `podman pull`, Podman requires a valid signature. Use the **system-wide** Podman configuration (the paths below), because that is what the Podman CLI uses when invoked by the agent process (typically running as root).

#### OTA updates (agent-driven pulls)

On every device that should only run signed OS images:

1. **Deploy the public key** used for signing (for example, `signingkey.pub` from the key pair you generated when signing) to a path readable by the agent process (typically root), for example `/etc/pki/containers/os-image-signing.pub`.

2. **Configure the registry to use Sigstore attachments** by adding or merging a file under the system-wide directory `/etc/containers/registries.d/` (for example `/etc/containers/registries.d/${OCI_REGISTRY}.yaml`):

   ```yaml
   docker:
     ${OCI_REGISTRY}:
       use-sigstore-attachments: true
   ```

   Replace `${OCI_REGISTRY}` with your registry (for example `quay.io`). This allows Podman to fetch and validate Sigstore signatures from the registry when the agent pulls.

3. **Configure the signature policy** in the system-wide `/etc/containers/policy.json` so that images from your OS image repository require a Sigstore signature signed by your public key. Add or merge a `transports.docker` entry for your registry and namespace (repository prefix), for example:

   ```json
   {
     "default": [{ "type": "insecureAcceptAnything" }],
     "transports": {
       "docker": {
         "${OCI_REGISTRY}/your_org": [
           {
             "type": "sigstoreSigned",
             "keyPath": "/etc/pki/containers/os-image-signing.pub"
           }
         ]
       }
     }
   }
   ```

   Replace `${OCI_REGISTRY}/your_org` with the registry and namespace (or full repository) where your signed OS images are stored. The `default` entry is required; scope policies for other registries can be added as needed. After this is in place, `podman pull` (and thus the agent's OS update) will only succeed if the image is signed with the matching private key.

You can bake this configuration and the public key into your OS image so that all devices built from it enforce verification, or deploy them via configuration management. For full details and other verification methods, see the [RHEL documentation on signing and verifying container images](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/9/html/building_running_and_managing_containers/assembly_signing-container-images_building-running-and-managing-containers).

#### Initial provisioning (disk image)

Verification of the OS *disk* image (ISO, qcow2, vmdk) when first provisioning a device is done by the provisioning system that writes the image (for example, OpenShift Virtualization, VMware, or a bare-metal installer). Refer to that system's documentation for how to verify or allow only signed artifacts before writing the disk image to the device.

### Further References

For further information and practical examples, refer to:

* Example images in the [Flight Control demos repository](https://github.com/flightctl/flightctl-demos).
* Flight Control demos repository's [automated build pipeline](https://github.com/flightctl/flightctl-demos/blob/main/.github/workflows/build-bootc-image.yaml).
* The Fedora/CentOS bootc project's [community-provided examples](https://gitlab.com/fedora/bootc/examples).

## Considerations for Specific Target Platforms

### Red Hat OpenShift Virtualization

When building an OS image and disk image for OpenShift Virtualization, follow the [generic process](#building-and-publishing-os-images-and-disk-images) with the following changes:

1. Use late binding of the enrollment endpoint and enrollment certificates, injecting the enrollment certificate or even the whole agent configuration through `cloud-init` when provisioning the virtual device.
2. Add the `open-vm-tools` guest tools to the image.
3. Build a disk image of type "qcow2" instead of type "iso".
4. Optional: Upload the disk image to an OCI registry as a container disk.

Create a file named `Containerfile` with the following content to build an OS image based on CentOS Stream 9 that includes the Flight Control agent and VM guest tools, but no agent configuration:

```console
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo && \
    dnf -y install flightctl-agent && \
    dnf -y clean all && \
    systemctl enable flightctl-agent.service

RUN dnf -y install cloud-init open-vm-tools && \
    dnf -y clean all && \
    ln -s ../cloud-init.target /usr/lib/systemd/system/default.target.wants && \
    systemctl enable vmtoolsd.service

# Optional: To enable podman-compose application support, uncomment below
# RUN dnf -y install epel-release epel-next-release && \
#    dnf -y install podman-compose && \
#    dnf -y clean all && \
#    systemctl enable podman.service
```

Build, sign, and publish the OS image (bootc) following the [generic process](#building-and-publishing-os-images-and-disk-images).

For the disk image, build an image of type "qcow2" instead of "iso":

```console
mkdir -p output

sudo podman run --rm -it --privileged --pull=newer \
    --security-opt label=type:unconfined_t \
    -v "${PWD}/output":/output \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type qcow2 \
    ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

Once `bootc-image-builder` completes, you can find the disk image under `${PWD}/output/qcow2/disk.qcow2`.

As OpenShift Virtualization can download disk images from an OCI registry, but expects a "container disk" image instead of an OCI artifact, use the following procedure to build, sign, and upload the QCoW2 disk image:

Create a file called `Containerfile.qcow2` with the following content:

```console
FROM registry.access.redhat.com/ubi9/ubi:latest AS builder
ADD --chown=107:107 output/qcow2/disk.qcow2 /disk/
RUN chmod 0440 /disk/*

FROM scratch
COPY --from=builder /disk/* /disk/
```

This adds the QCoW2 disk image to a builder container in order to set the required file ownership (107 is the QEMU user) and file permissions (0440), then copies the file to a scratch image.

Next, build, sign, and publish your disk image:

```console
sudo chown -R $(whoami):$(whoami) "${PWD}/output"

OCI_DISK_IMAGE_REPO=${OCI_IMAGE_REPO}/diskimage-qcow2

sudo podman build -t ${OCI_DISK_IMAGE_REPO}:${OCI_IMAGE_TAG} -f Containerfile.qcow2 .

sudo podman push --sign-by-sigstore-private-key ./signingkey.private ${OCI_DISK_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

### VMware vSphere

When building OS images and disk images for VMware vSphere, follow the [generic process](#building-and-publishing-os-images-and-disk-images) with the following changes:

1. Use late binding of the enrollment endpoint and enrollment certificates, injecting the enrollment certificate or even the whole agent configuration through `cloud-init` when provisioning the virtual device.
2. Add the `open-vm-tools` guest tools to the image.
3. Build a disk image of type "vmdk" instead of type "iso".

Create a file named `Containerfile` with the following content to build an OS image based on CentOS Stream 9 that includes the Flight Control agent and VM guest tools, but no agent configuration:

```console
FROM quay.io/centos-bootc/centos-bootc:stream9

RUN dnf -y config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo && \
    dnf -y install flightctl-agent && \
    dnf -y clean all && \
    systemctl enable flightctl-agent.service

RUN dnf -y install cloud-init open-vm-tools && \
    dnf -y clean all && \
    ln -s ../cloud-init.target /usr/lib/systemd/system/default.target.wants && \
    systemctl enable vmtoolsd.service
```

Build the OS image (bootc) in the [generic process](#building-and-publishing-os-images-and-disk-images), but build an image of type "vmdk" instead of "iso":

```console
mkdir -p output

sudo podman run --rm -it --privileged --pull=newer \
    --security-opt label=type:unconfined_t \
    -v "${PWD}/output":/output \
    -v /var/lib/containers/storage:/var/lib/containers/storage \
    quay.io/centos-bootc/bootc-image-builder:latest \
    --type vmdk \
    ${OCI_IMAGE_REPO}:${OCI_IMAGE_TAG}
```

Once `bootc-image-builder` completes, you can find the disk image under `${PWD}/output/vmdk/disk.vmdk`.

### References

For details and other target platforms, refer to

* The "[Deploying the RHEL bootc images](https://docs.redhat.com/en/documentation/red_hat_enterprise_linux/9/html-single/using_image_mode_for_rhel_to_build_deploy_and_manage_operating_systems/index#deploying-the-rhel-bootc-images_using-image-mode-for-rhel-to-build-deploy-and-manage-operating-systems)" section of the RHEL documentation

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

See also the guidance in the [bootc documentation](https://bootc-dev.github.io/bootc/building/guidance.html).
