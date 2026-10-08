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
| `deltaGeneration` | `DeltaGenerationConfig` | | Deployment defaults for OS and application delta generation. By default, organizations configure their own OCI delta storage targets. |

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

You can customize deployment defaults for OS and application delta generation under `deltaGeneration`. For Helm deployments, set these values in the values file used for installation or upgrade. For packaged Podman deployments, set them in `/etc/flightctl/service-config.yaml`. The service renders this input into its component-specific `config.yaml` at startup.

Generation requires a valid, writable OCI target configured through either option:

- Set `deltaGeneration.defaultRepository` in the deployment configuration to provide a shared default.
- Create an OCI Repository resource with `type: oci`, `accessMode: ReadWrite`, and `deltaStorageTarget: true`, as described in [Managing Repositories: Configuring a delta storage target](../using/managing-repositories.md#configuring-a-delta-storage-target). Configure this resource at runtime after installation.

Either option satisfies the storage prerequisite. An organization's Repository resource takes precedence over the deployment default when both are configured.

Ensure both the control plane and the device agent can access the registry, as described in [Delta storage target access requirements](../using/managing-repositories.md#configuring-a-delta-storage-target).

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `defaultRepository` | `object` | | Shared default OCI storage target. An organization's delta storage target takes precedence. See the repository fields below. |
| `maxConcurrentDeltaGenerations` | `integer` | | Maximum concurrent generation jobs per delta worker instance. Defaults to `2` when omitted or set to `0` or a negative value. Values greater than `32` are capped at `32`. Multiply this value by the delta-worker replica count for the deployment's maximum concurrent jobs. |
| `timeout` | `duration` | | Deadline for each generation job, expressed as a Go duration such as `30m`. Defaults to `30m` when omitted or set to `0` or a negative duration. A fleet can override it with `spec.rolloutPolicy.deltaGeneration.deltaGenerationTimeout`. |
| `maxWaitForDelta` | `duration` | | Optional preparation wait cap, expressed as a Go duration such as `10m`. Omitted by default: the update waits until all generation pairs reach a terminal state. `timeout` bounds each running job; queued pairs can extend the total wait. `0s` starts generation and continues the update immediately. A fleet can override it with `spec.rolloutPolicy.deltaGeneration.maxWaitForDelta`. |

Standalone devices use these deployment defaults. Fleet overrides apply to both OS and application generation. See [Configuring delta generation](../using/managing-fleets.md#configuring-delta-generation) for rollout options.

The following block is valid in both Helm values and `/etc/flightctl/service-config.yaml`. It configures a shared repository and a ten-minute preparation wait:

```yaml
deltaGeneration:
  defaultRepository:
    registry: registry.example.com
    repository: my-org/deltas
    scheme: https
  maxConcurrentDeltaGenerations: 2 # Default
  timeout: 30m # Default
  maxWaitForDelta: 10m
```

The `defaultRepository` block accepts the following deployment values:

| Parameter | Type | Required | Description |
| --------- | ---- | :------: | ----------- |
| `registry` | `string` | Y | Registry hostname, optionally including a port. Required when configuring a default target. |
| `repository` | `string` | | Exact repository path under the registry. Mutually exclusive with `namespace`. |
| `namespace` | `string` | | Namespace prefix for the final segment of each target image name. Mutually exclusive with `repository`. |
| `scheme` | `string` | | Registry connection scheme: `https` or `http`. |
| `skipServerVerification` | `boolean` | | Skip registry TLS certificate verification. Defaults to `false`. |
| `caCrt` | `string` | | Base64-encoded PEM certificate authority for the registry. In the rendered service `config.yaml`, the field is named `ca.crt`. |
| `secretName` | `string` | | Helm only. Name of a Kubernetes Secret in the installation namespace with `username` and `password` keys for registry push access. |

For a private registry on Helm, create the credential Secret and set `deltaGeneration.defaultRepository.secretName` to its name. See [Configuring delta generation on Kubernetes](installing-service-on-kubernetes.md#configuring-delta-generation) for an example. Credentials are injected separately from the configuration file.

#### Configuring Podman registry credentials

For a private deployment default on Podman, supply registry credentials through Podman secrets and a [Quadlet drop-in](https://docs.podman.io/en/stable/markdown/podman-systemd.unit.5.html).

1. Store the username in `<username_file>` and the password or token in `<password_file>`. Restrict access to these files to the administrator.
2. Create the secrets:

   ```console
   sudo podman secret create flightctl-delta-generation-default-repository-username <username_file>
   ```

   ```console
   sudo podman secret create flightctl-delta-generation-default-repository-password <password_file>
   ```

3. Create the drop-in directory:

   ```console
   sudo mkdir -p /etc/containers/systemd/flightctl-delta-worker.container.d
   ```

4. Create `/etc/containers/systemd/flightctl-delta-worker.container.d/delta-generation-repository.conf` with the following content:

   ```ini
   [Container]
   Secret=flightctl-delta-generation-default-repository-username,type=env,target=DELTA_GENERATION_DEFAULT_REPOSITORY_USERNAME
   Secret=flightctl-delta-generation-default-repository-password,type=env,target=DELTA_GENERATION_DEFAULT_REPOSITORY_PASSWORD
   ```

5. Reload the units and restart the delta worker:

   ```console
   sudo systemctl daemon-reload
   ```

   ```console
   sudo systemctl restart flightctl-delta-worker.service
   ```

With a registry-only target, generated deltas use the target image's repository path under the configured registry. See [Configuring a delta storage target](../using/managing-repositories.md#configuring-a-delta-storage-target) for organization targets and destination examples.
