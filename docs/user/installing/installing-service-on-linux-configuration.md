# Configuring Flight Control Services

Flight Control services are configured through a configuration file that includes authentication settings, database connections, and other service parameters.

## Service Configuration File

The Flight Control service reads its configuration from `/root/.flightctl/config.yaml` by default. The configuration file is automatically generated with default values when the service starts for the first time.

## Service Configuration Parameters

The service configuration file accepts the following parameters:

Note - this is currently a subset of all configuration options.

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `auth` | `AuthConfig` | | Authentication configuration for the Flight Control service. |
| `organizations` | `OrganizationsConfig` | | Organization support configuration. Default: organizations disabled |
| `deltaGeneration` | `DeltaGenerationConfig` | | Deployment defaults for OS and application delta generation. |

### Auth Configuration

The `auth` section configures how the Flight Control service authenticates users:

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `oidc` | `OIDCAuth` | | OIDC authentication provider configuration. |
| `k8s` | `K8sAuth` | | Kubernetes authentication provider configuration. |
| `aap` | `AAPAuth` | | AAP Gateway authentication provider configuration. |
| `caCert` | `string` | | Custom CA certificate for authentication provider. |
| `insecureSkipTlsVerify` | `boolean` | | Skip TLS certificate verification. Default: `false` |

> [!WARNING]
> Setting `insecureSkipTlsVerify: true` disables certificate validation and should only be used in development environments.

#### OIDC Authentication

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `oidcAuthority` | `string` | Y | The base URL for the OIDC realm that is reachable by Flight Control services. |
| `externalOidcAuthority` | `string` | | The base URL for the OIDC realm that is reachable by clients. |

### Organizations Configuration

The `organizations` section enables multi-organization support when used with compatible identity providers:

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `enabled` | `boolean` | Y | Enable IdP-provided organization support. When `true`, the service expects organization information from the identity provider. Default: `false` |

For more information on configuring organizations, see [Organizations](configuring-auth/organizations.md)

> [!NOTE]
> Organization support is currently only available with OIDC authentication providers. Kubernetes and AAP Gateway authentication do not support multi-organization deployments.

### Delta generation configuration

You can customize deployment defaults for OS and application delta generation under `deltaGeneration`. For Helm deployments, set these values in the values file used for installation or upgrade. For packaged Podman deployments, set them in `/etc/flightctl/service-config.yaml`.

Generation requires a valid, writable OCI target configured through either option:

- Set `deltaGeneration.defaultRepository` in the deployment configuration to provide a shared default.
- Create an OCI Repository resource with `type: oci`, `accessMode: ReadWrite`, and `deltaStorageTarget: true`, as described in [Managing Repositories: Configuring a delta storage target](../using/managing-repositories.md#configuring-a-delta-storage-target). Configure this resource at runtime after installation.

Either option satisfies the storage prerequisite. The registry must be reachable with the configured push credentials and TLS settings. An organization's Repository resource takes precedence over the deployment default when both are configured.

| Parameter | Description |
| --------- | ----------- |
| `defaultRepository` | Shared default OCI storage target. An organization's delta storage target takes precedence. See the repository fields below. |
| `maxConcurrentDeltaGenerations` | Maximum concurrent generation jobs per delta worker instance. Defaults to `2` when omitted or set to `0` or a negative value. Values greater than `32` are capped at `32`. |
| `timeout` | Deadline for each generation job. Defaults to `30m` when omitted or set to `0` or a negative duration. A fleet can override it with `spec.rolloutPolicy.deltaGeneration.deltaGenerationTimeout`. |
| `maxWaitForDelta` | Maximum time to hold an update while deltas are prepared. By default, the update waits until all generation pairs reach a terminal state. `0s` starts generation and continues the update immediately. A fleet can override it with `spec.rolloutPolicy.deltaGeneration.maxWaitForDelta`. |

Standalone devices use these deployment defaults. Fleet overrides apply to both OS and application generation. See [Configuring delta generation](../using/managing-fleets.md#configuring-delta-generation) for rollout options.

For example, configure a shared repository, two concurrent jobs, and a ten-minute preparation wait:

```yaml
deltaGeneration:
  defaultRepository:
    registry: registry.example.com
    repository: my-org/deltas
    scheme: https
  maxConcurrentDeltaGenerations: 2
  timeout: 30m
  maxWaitForDelta: 10m
```

The `defaultRepository` block accepts the following deployment values:

| Parameter | Description |
| --------- | ----------- |
| `registry` | Required when configuring a default target. Registry hostname, optionally including a port. |
| `repository` | Optional exact repository path under the registry. Mutually exclusive with `namespace`. |
| `namespace` | Optional namespace prefix for target image names. Mutually exclusive with `repository`. |
| `scheme` | Registry connection scheme: `https` or `http`. |
| `skipServerVerification` | Skip registry TLS certificate verification. Defaults to `false`. |
| `caCrt` | Optional base64-encoded PEM certificate authority for the registry. In the rendered service `config.yaml`, the field is named `ca.crt`. |
| `secretName` | Helm only. Name of a Kubernetes Secret in the installation namespace with `username` and `password` keys for registry push access. |

For a private registry on Helm, create the credential Secret and set `deltaGeneration.defaultRepository.secretName` to its name. On Podman, provide `DELTA_GENERATION_DEFAULT_REPOSITORY_USERNAME` and `DELTA_GENERATION_DEFAULT_REPOSITORY_PASSWORD` to the delta worker container. Credentials are supplied separately from the configuration file.

With a registry-only target, generated deltas use the target image's repository path under the configured registry. See [Configuring a delta storage target](../using/managing-repositories.md#configuring-a-delta-storage-target) for organization targets and destination examples.
