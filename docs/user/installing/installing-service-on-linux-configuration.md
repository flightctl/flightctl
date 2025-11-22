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

For more information on configuring organizations, see [Organizations](organizations.md)

> [!NOTE]
> Organization support is currently only available with OIDC authentication providers. Kubernetes and AAP Gateway authentication do not support multi-organization deployments.
