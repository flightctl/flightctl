# Organizations

Flight Control provides organizations as a mechanism to group resources and facilitate access control for users.

## Overview

Organizations provide a measure of resource isolation within Flight Control. Resources in this context include entities such as devices and fleets.

Some important notes about how organizations work:

- Resources cannot be shared or moved between organizations.
- APIs never aggregate content across organizations.
  - For example, the devices list API returns resources only from a single selected organization, even if the caller has access to others.
- All organizations share the same Flight Control service configuration.
- Flight Control delegates organization to user mappings to the configured identity providers.
  - Adding or removing a user’s access to an organization is managed in the identity provider, not in Flight Control.

## Organizations configuration

Currently, Flight Control only supports multi-organization deployments when configured with a compatible OIDC identity provider. When organization support is enabled:

- Users are associated with organizations based on claims provided by the identity provider
- Resources (devices, fleets, etc.) are scoped to specific organizations
- Users can only access resources owned by their assigned organizations

### Requirements for Organization Support

Organization support requires:

1. **OIDC Authentication**: Organizations are only supported with OIDC authentication providers
2. **Compatible Claims**: The OIDC provider must include organization information in token claims
3. **Proper Configuration**: Both the service and identity provider must be configured to support organizations

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
