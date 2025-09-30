# Organizations

Flight Control provides organizations as a mechanism to group resources and facilitate access control for users.

## Overview

Organizations provide a measure of resource isolation within Flight Control. Resources in this context include entities such as devices and fleets.

Some important notes about how organizations work:

- Resources cannot be shared or moved between organizations.
- APIs never aggregate content across organizations.
  - For example, the devices list API returns resources only from a single selected organization, even if the caller has access to others.
- Organizations provide a scope for names.  Names of resources are unique within an organization, but may not be across all organizations.
- All organizations share the same Flight Control service configuration.
- User-to-organization mappings are managed by the configured auth identity provider (IdP)
  - Flight Control does not directly manage user membership

## Default Organization

Flight Control always provisions a default organization during installation.

The default organization owns resources when no identity provider is configured.
If multiple organizations are not configured, users generally do not need to be aware of or interact with this default organization.

## Configuring Multiple Organizations

Multi-organization support requires a compatible identity provider. When enabled:

- Users are assigned to organizations by the IdP.
- Resources (devices, fleets, etc.) are scoped to specific organizations
- Users can only access resources owned by their assigned organizations

Organization support is enabled by setting a Helm configuration value (service-config value if using quadlets):

```yaml
global:
  organizations:
    enabled: true
```

> [!NOTE] Multi-organization deployments require a supported authentication provider (OIDC or AAP).

### Requirements for OIDC Organizations Integration

OIDC based organization support requires:

1. **OIDC Auth**: Flight Control must be configured with a compatible OIDC auth provider
2. **Compatible Claims**: The OIDC provider must include organization information in token claims
3. **Service Configuration**: Organizations must be enabled via configuration passed to Flight Control Services

#### Token Claims

Organization information in token claims must use the following format:

```json
  "organization": {
    "organization-name": {
      "id": "organization-unique-identifier"
    },
    ...
  }

```

An example organization claim from a decoded JWT looks like:

```json
  "organization": {
    "pinkcorp": {
      "id": "a6e97659-16a5-4b18-9d90-7fe88a744e2a"
    },
    "orangecorp": {
      "id": "7ca05aab-652c-46a4-aef5-1093e573865c"
    }
  }
```

If using Keycloak, configure organization claims in your realm as described in the [Keycloak documentation on managing organizations](https://www.keycloak.org/docs/latest/server_admin/index.html#_managing_organizations) to match this format.

### Requirements for AAP Organizations Integration

AAP based organization support requires:

1. **AAP Auth**: Flight Control must be configured with AAP as the auth provider
2. **Service Configuration**: Organizations must be enabled via configuration passed to Flight Control Services

#### AAP User / Organization Mappings

Organization membership is derived from AAP relationships:

- A user directly assigned as a member of an organization.
- A user assigned to a team owned by an organization.
- A user assigned as an administrator of an organization.

If any condition is met, the user gains access to the organization in Flight Control.

Two "special" AAP roles grant access to all organizations:

- Superuser
- Platform Auditor

## Usage with multiple organizations

When a user has access to more than one organization, they must select an organization before performing operations.

- In the UI, the active organization is chosen from a selector after login.
- In the CLI, the active organization is set by context commands or specified per-command.

### CLI

List available organizations:

```bash
flightctl get organizations
NAME                                     DISPLAY NAME        EXTERNAL ID
d02b1abf-a372-45c7-a794-41547109075c     Example Org One     123
34cec4c4-bbb4-440e-9a90-2b275805aab5     Example Org Two     456
```

Set the active organization context:

```bash
flightctl config set-organization d02b1abf-a372-45c7-a794-41547109075c
```

Run commands scoped to the active organization:

```bash
flightctl get devices
```

Alternatively, specify an organization per-command with the --org flag:

```bash
flightctl get devices --org=d02b1abf-a372-45c7-a794-41547109075c
```

Check the current organization:

```bash
flightctl config current-organization
```
