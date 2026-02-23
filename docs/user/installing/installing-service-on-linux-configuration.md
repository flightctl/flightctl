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
| `defaultAliasKeys` | `[]string` | | Ordered list of keys used to compute a default device alias from enrollment request system info. When set, the first non-empty value from the device's `status.systemInfo` is used as the device alias when approving an enrollment request that does not specify an alias. Default: `["hostname"]`. Set to an empty list to disable. See [Default device alias](#default-device-alias). |

### Default device alias

When `defaultAliasKeys` is non-empty, the service computes a default alias for each enrollment request from the device's reported system information. On approval, if the approval does not set an alias label, the device's `metadata.labels["alias"]` is set to this computed value.

Each key in `defaultAliasKeys` can be:

- **Fixed fields:** `architecture`, `bootID`, `operatingSystem`, `agentVersion`
- **Additional properties:** any key from system info (for example, `hostname`, `productSerial`, `kernel`)
- **Custom info:** `customInfo.<key>` for a value from user-defined device scripts (the suffix after `customInfo.` must be a valid label-like token)

Keys are evaluated in order; the first non-empty value is used. The result is sanitized for use as a Kubernetes label value (length and character rules). The default configuration uses `["hostname"]`. Set the list to empty to disable the default alias.

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
