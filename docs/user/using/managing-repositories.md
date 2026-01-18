# Managing Repositories

Repository resources define how Flight Control accesses external sources. Flight Control supports several repository types for different use cases:

* **Git repositories**: For storing and synchronizing device configuration files
* **HTTP repositories**: For accessing configuration files over HTTP/HTTPS
* **SSH repositories**: For accessing Git repositories via SSH
* **OCI repositories**: For referencing container image registries (used by ImageBuild and ImageExport)

This document focuses on OCI repositories. For information on Git, HTTP, and SSH repositories used for device configuration, see [Managing Devices](managing-devices.md#getting-configuration-from-a-git-repository).

## Repository Connectivity Status

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
