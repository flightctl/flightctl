# Managing Repositories

Repository resources define how Flight Control accesses external sources. Flight Control supports several repository types for different use cases:

* **Git repositories**: For storing and synchronizing device configuration files
* **HTTP repositories**: For accessing configuration files over HTTP/HTTPS
* **SSH repositories**: For accessing Git repositories via SSH
* **OCI repositories**: For referencing container image registries (used by ImageBuild and ImageExport)

This document focuses on OCI repositories. For information on Git, HTTP, and SSH repositories used for device configuration, see [Managing Devices](managing-devices.md#getting-configuration-from-a-git-repository).

## Repository Connectivity Status

For Git repository TLS settings and certificate requirements, see [HTTPS authentication](managing-devices.md#https-authentication).

Flight Control automatically performs connectivity checks on all repositories to verify they are accessible. The connectivity status is reflected in the repository's `status.conditions` field with a condition of type `RepositoryAccessible`.

You can check the connectivity status of a repository using the CLI:

```console
flightctl get repository my-registry
```

The output will show the `ACCESSIBLE` status in the table view, or you can inspect the full status:

```console
flightctl get repository my-registry -o yaml
```

The status will show:

* **`Accessible: True`**: The repository is reachable and accessible with the configured credentials
* **`Accessible: False`**: The repository is not accessible (check network connectivity, credentials, or repository URL)

Flight Control periodically tests repository connectivity in the background. When connectivity changes, events are emitted:

* `RepositoryAccessible`: When a repository becomes accessible
* `RepositoryInaccessible`: When a repository becomes inaccessible

If a repository shows as inaccessible, verify:

* Network connectivity to the repository
* Correct repository URL and configuration
* Valid authentication credentials (if required)
* Firewall rules and access permissions

## OCI Repositories

OCI (Open Container Initiative) repositories are used to reference container image registries in Flight Control. They are required for [ImageBuild](managing-image-builds.md#imagebuild-resource) and [ImageExport](managing-image-builds.md#imageexport-resource) resources, which need to pull source images and push built/exported images to registries.

### OCI Repository Specification

An OCI repository resource has the following structure:

```yaml
apiVersion: flightctl.io/v1beta1
kind: Repository
metadata:
  name: my-registry
spec:
  type: oci
  registry: quay.io                    # Registry hostname
  scheme: https                        # http or https
  accessMode: ReadWrite                # Read or ReadWrite
  ociAuth:                             # Optional: authentication
    authType: docker
    username: my-username
    password: my-password
```

### Required Fields

* `type`: Must be set to `oci`
* `registry`: The OCI registry hostname, FQDN, or IP address with optional port
  * Examples: `quay.io`, `registry.redhat.io`, `myregistry.com:5000`, `192.168.1.1:5000`, `[::1]:5000`

### Optional Fields

* `repository`: Exact repository path under the registry; mutually exclusive with `namespace`
* `namespace`: Prefix for the final segment of each target image name; mutually exclusive with `repository`. Valid for delta storage targets; ImageBuild and ImageExport destination validation rejects this field.
* `deltaStorageTarget`: Set to `true` to use this Repository as the organization's delta storage target. Requires `accessMode: ReadWrite`. Defaults to `false`.
* `scheme`: URL scheme for connecting to the registry
  * Values: `http` or `https` (default: `https`)
* `accessMode`: Access permissions for the registry
  * `Read`: Read-only access (pull images only)
  * `ReadWrite`: Read and write access (pull and push images)
  * Default: `Read`
* `ociAuth`: Authentication credentials for private registries
  * `authType`: Authentication type (currently only `docker` is supported)
  * `username`: Registry username
  * `password`: Registry password or token
  * Omit this field for public registries that don't require authentication
* `ca.crt`: Base64-encoded root CA certificate for custom certificate authorities
* `skipServerVerification`: Boolean to skip remote server verification (not recommended for production)

### Creating an OCI Repository

#### Public Registry (Read-Only)

For public registries that don't require authentication:

```yaml
apiVersion: flightctl.io/v1beta1
kind: Repository
metadata:
  name: quay-io
spec:
  type: oci
  registry: quay.io
  scheme: https
  accessMode: Read
```

Create the repository:

```console
flightctl apply -f repository-oci-public.yaml
```

#### Private Registry (Read-Write)

For private registries that require authentication and need push access:

```yaml
apiVersion: flightctl.io/v1beta1
kind: Repository
metadata:
  name: my-registry
spec:
  type: oci
  registry: quay.io
  scheme: https
  accessMode: ReadWrite
  ociAuth:
    authType: docker
    username: my-username
    password: my-password
```

Create the repository:

```console
flightctl apply -f repository-oci-private.yaml
```

> [!WARNING]
> Store repository credentials securely. Consider using secrets management systems or environment variables when providing credentials via the API.

### Configuring a delta storage target

OS and application delta generation requires a valid, writable OCI storage target for the organization or deployment. The delta registry must be reachable by both the control plane and the device agent. Configure push access for the control plane and pull access for the agent, with appropriate credentials and TLS settings.

Mark one Repository as the organization's target by setting `deltaStorageTarget: true`. Only one Repository can be the target for an organization. Creating or updating a second target is rejected. Configure it with `accessMode: ReadWrite` and credentials that can push to the registry.

Use the optional `repository` and `namespace` fields to choose the destination path. These fields are mutually exclusive.

For the target image `quay.io/acme/os:v2` and delta registry `registry.example.com`, destination paths are:

| Repository fields | Delta destination | Example |
| ----------------- | ----------------- | ------- |
| Registry only | Target image's repository path under the configured registry. | `registry.example.com/acme/os` |
| `repository: my-org/deltas` | Exact repository path under the registry. | `registry.example.com/my-org/deltas` |
| `namespace: my-org` | Namespace followed by the final segment of the target image name. | `registry.example.com/my-org/os` |

For example, this Repository is a writable delta target with the exact repository path `my-org/deltas`:

```yaml
apiVersion: flightctl.io/v1beta1
kind: Repository
metadata:
  name: generated-deltas
spec:
  type: oci
  registry: quay.io
  repository: my-org/deltas
  accessMode: ReadWrite
  deltaStorageTarget: true
  ociAuth:
    authType: docker
    username: <registry_username>
    password: <registry_token>
```

The example includes `ociAuth` for a private registry. Use credentials with push access; see [Private Registry (Read-Write)](#private-registry-read-write).

The deployment-level `deltaGeneration.defaultRepository` setting provides a shared default target for organizations. An organization Repository marked with `deltaStorageTarget: true` takes precedence over this default. See [Delta generation configuration](../installing/installing-service-on-linux-configuration.md#delta-generation-configuration) for registry settings, credentials, concurrency, job timeouts, and update wait defaults.

For fleet wait behavior and update status, see [Configuring delta generation](managing-fleets.md#configuring-delta-generation) and [Using control-plane-generated OS deltas](managing-devices.md#using-control-plane-generated-os-deltas).

[CI-published deltas](managing-devices.md#using-ci-published-os-deltas) are published in the target image's repository, where agents discover them directly.

### Curating base images

OCI repositories can include a curated list of trusted base images available in the registry. When base images are configured, the Flight Control UI presents them as selectable options when creating image builds, instead of requiring users to type image names and tags manually. Users can still enter custom values that are not in the curated list.

A base image entry specifies:

* An image name (the image path within the registry)
* One or more selectable tags for that image
* An optional human-readable display name shown in the UI

To configure base images, add the `baseImages` field to the OCI repository spec:

```yaml
apiVersion: flightctl.io/v1beta1
kind: Repository
metadata:
  name: quay-io
spec:
  type: oci
  registry: quay.io
  scheme: https
  accessMode: Read
  baseImages:
    - displayName: CentOS Stream
      imageName: centos-bootc/centos-bootc
      tags:
        - stream9
        - stream10
```

The `baseImages` field accepts a list of entries with the following fields:

| Field | Required | Description |
| ------------- | -------- | -------------------------------------------------------------------- |
| `imageName` | Yes | Image path within the registry (for example, `centos-bootc/centos-bootc`). Must be 1–255 characters. Image names must be unique within the repository. |
| `tags` | Yes | One or more tags for this image. Each tag must be unique within the entry. |
| `displayName` | No | Human-readable label shown in the UI when selecting a base image. |

> [!NOTE]
> Image names and tags must conform to OCI naming rules. Duplicate image names within the same repository are rejected by the API.

Apply the updated repository definition:

```console
flightctl apply -f repository-oci-base-images.yaml
```

Once saved, any image build wizard that uses this repository as the source shows the configured base images in a dropdown. Selecting an image name filters the available tags to those listed for that entry.

### Using OCI Repositories with ImageBuild and ImageExport

OCI repositories are used by [ImageBuild](managing-image-builds.md#imagebuild-resource) and [ImageExport](managing-image-builds.md#imageexport-resource) resources to reference container image registries. They are referenced by name in the `repository` field of these resources.

For details on how to use OCI repositories with ImageBuild and ImageExport, see [Managing Image Builds and Exports](managing-image-builds.md).

### Registry Hostname Formats

The `registry` field accepts various formats:

* **Hostname**: `quay.io`, `registry.redhat.io`
* **Hostname with port**: `myregistry.com:5000`
* **IP address with port**: `192.168.1.1:5000`
* **IPv6 address with port**: `[::1]:5000`

### Security Considerations

* Use `https` scheme in production environments
* Store credentials securely and avoid committing them to version control
* Use `Read` access mode when only pulling images is required
* Use `ReadWrite` access mode only when pushing images is necessary
* Consider using registry tokens or service accounts instead of personal credentials
* For custom CAs, provide the certificate via `ca.crt` field

### Troubleshooting

#### Authentication Failures

If you encounter authentication errors:

1. Verify the credentials are correct
2. Check that the registry URL is correct
3. Ensure the `accessMode` matches your intended operations (use `ReadWrite` for pushing)
4. For private registries, verify the credentials have the necessary permissions

#### Connection Issues

If you cannot connect to the registry:

1. Verify network connectivity to the registry
2. Check that the `scheme` (http/https) matches the registry configuration
3. For custom registries, verify the `registry` hostname or IP is correct
4. If using a custom CA, ensure the `ca.crt` is correctly base64-encoded

#### Access Mode Errors

If you get permission errors when pushing:

1. Ensure the destination repository has `accessMode: ReadWrite`
2. Verify the credentials have push permissions on the registry
3. Check that the image name and tag are valid for the registry
