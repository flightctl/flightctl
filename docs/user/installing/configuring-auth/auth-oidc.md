# OIDC Authentication

Flight Control API server supports standard OpenID Connect (OIDC) authentication, working with any OIDC-compliant provider.

## How Flight Control Handles OIDC

Flight Control API server implements standard OpenID Connect authentication. It validates JWT tokens issued by any OIDC-compliant provider by:

- Discovering provider configuration from `/.well-known/openid-configuration`
- Validating token signatures using provider's public keys
- Verifying token claims (issuer, audience, expiration)
- Extracting user identity and claims from userinfo endpoint

## OIDC Providers

Flight Control works with any OIDC-compliant provider.
For standalone deployments, [PAM Issuer](auth-pam.md) is bundled as a default OIDC provider.

## Organization and Role Mapping

Flight Control maps organizations and roles from OIDC userinfo based on AuthProvider configuration:

### Organization Assignment

Configure how users are assigned to organizations via `organizationAssignment` in the AuthProvider:

- **Static** (`type: static`): Assigns all users to a specific organization
- **Dynamic** (`type: dynamic`): Maps organizations from userinfo claims using JSON path
  - `claimPath`: JSON path array to navigate to the organization claim
    - Example: `["groups"]` for `userinfo.groups`
    - Example: `["resource_access", "flightctl", "organizations"]` for `userinfo.resource_access.flightctl.organizations`
  - `organizationNamePrefix`: Optional prefix for organization names
  - `organizationNameSuffix`: Optional suffix for organization names
- **Per User** (`type: perUser`): Creates a separate organization for each user

  - `organizationNamePrefix`: Prefix for user-specific org name (default: `"user-org-"`)
  - `organizationNameSuffix`: Optional suffix for org name

  **Important:** When choosing `organizationAssignment=perUser`, it's recommended to use `roleAssignment=static` with the `flightctl-org-admin` role. Since each user manages their own organization, `flightctl-org-admin` provides the appropriate permissions for managing organization resources. See below for details on role assignment.

### Role Assignment

Configure how roles are assigned via `roleAssignment` in the AuthProvider:

- **Static** (`type: static`): Assigns specific roles to all users
  - `roles`: Array of role names to assign
- **Dynamic** (`type: dynamic`): Maps roles from userinfo claims using JSON path
  - `claimPath`: JSON path array to navigate to the roles/groups claim
    - Example: `["roles"]` for `userinfo.roles`
    - Example: `["realm_access", "roles"]` for `userinfo.realm_access.roles`
    - Example: `["resource_access", "flightctl", "roles"]` for `userinfo.resource_access.flightctl.roles`
  - `separator`: Separator for org:role format (default: `":"`) - roles containing the separator are split into organization-scoped roles

**Note:** Any role or organization configuration changes on the issuer side require users to log in again to receive updated assignments.

## Role Scoping

Roles support organization scoping using the `:` separator:

- `org1:role1` - Role `role1` scoped to organization `org1` only
- `*:role1` or `role1` - Role `role1` applies to all organizations the user belongs to

## Super Admin Role

The `flightctl-admin` role grants super admin access and is only recognized when provided as:

- `flightctl-admin` (applies to all orgs)
- `*:flightctl-admin` (explicitly applies to all orgs)

**Additional Permissions:**

When a user is assigned the `flightctl-admin` role, they automatically receive:

- Super admin access across all Flight Control resources
- `flightctl-org-admin` permissions to all organizations they are assigned to

## Recognized Roles

Flight Control currently recognizes the following roles with defined permissions:

- **`flightctl-admin`** - Full access to all resources (super admin)
- **`flightctl-org-admin`** - Full access to all resources within assigned organization
- **`flightctl-operator`** - CRUD operations on devices, fleets, resourcesyncs, repositories
- **`flightctl-viewer`** - Read-only access to devices, fleets, resourcesyncs, organizations
- **`flightctl-installer`** - Access to get and approve enrollmentrequests, and manage certificate signing requests

**Note:** Other role names can be assigned via AuthProvider configuration but will not have permissions unless they match these recognized roles.

## Configuration

### Redirect URLs

Configure the following redirect URLs in both Flight Control and your OIDC provider:

- `<UI_URL>/callback` - Web UI callback
- `http://localhost:8080/callback` - CLI webserver callback (default port 8080)

### Dynamic Provider Management

OIDC providers are configured dynamically via AuthProvider resources:

- Create AuthProvider resources via API/CLI
- Update provider configuration without service restart
- Delete providers when no longer needed
- Support multiple OIDC providers simultaneously

### Creating an OIDC Provider

```bash
flightctl apply -f authprovider.yaml
```

Or create directly:

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: my-oidc-provider
spec:
  providerType: oidc
  displayName: "Corporate OIDC"
  issuer: "https://auth.example.com"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - openid
    - profile
    - email
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-operator
EOF
```

**Example with dynamic organization and role assignment:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: corporate-oidc-dynamic
spec:
  providerType: oidc
  displayName: "Corporate OIDC (Dynamic)"
  issuer: "https://auth.example.com"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - openid
    - profile
    - email
  organizationAssignment:
    type: dynamic
    claimPath: ["resource_access", "flightctl", "organizations"]  # Nested JSON path
    organizationNamePrefix: "org-"  # Optional: prefix for org names
  roleAssignment:
    type: dynamic
    claimPath: ["resource_access", "flightctl", "roles"]  # Nested JSON path
    separator: ":"  # Separator for org:role format (default: ":")
EOF
```

This configuration will:

- Map organizations from nested claim: `userinfo.resource_access.flightctl.organizations`
  - The `claimPath: ["resource_access", "flightctl", "organizations"]` navigates to the nested array
- Prefix organization names with `org-`
- Map roles from nested claim: `userinfo.resource_access.flightctl.roles`
  - The `claimPath: ["resource_access", "flightctl", "roles"]` navigates to the nested array
- Support role scoping with `:` separator (e.g., `org1:role1` for org-specific, `role1` for global)

**Example with per-user organization assignment:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: corporate-oidc-peruser
spec:
  providerType: oidc
  displayName: "Corporate OIDC (Per User)"
  issuer: "https://auth.example.com"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - openid
    - profile
    - email
  organizationAssignment:
    type: perUser
    organizationNamePrefix: "user-org-"  # Default: "user-org-"
    organizationNameSuffix: ""  # Optional suffix
  roleAssignment:
    type: static
    roles:
      - flightctl-org-admin
EOF
```

This configuration will:

- Create a separate organization for each user (e.g., `user-org-alice`, `user-org-bob`)
- Assign all users the `flightctl-org-admin` role in their personal organization, giving them full administrative access to manage their organization's resources

**Example with simple (non-nested) claim paths:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: simple-oidc
spec:
  providerType: oidc
  displayName: "Simple OIDC"
  issuer: "https://auth.example.com"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - openid
    - profile
    - email
  organizationAssignment:
    type: dynamic
    claimPath: ["groups"]  # Simple top-level claim
  roleAssignment:
    type: dynamic
    claimPath: ["roles"]  # Simple top-level claim
    separator: ":"
EOF
```

This configuration demonstrates:

- Simple top-level claims: `userinfo.groups` and `userinfo.roles`
- Use `["groups"]` for a top-level groups array in userinfo
- Use `["roles"]` for a top-level roles array in userinfo

### Managing OIDC Providers

```bash
# List all providers (ap is short for authprovider/authproviders)
flightctl get ap

# Get provider details
flightctl get ap my-oidc-provider -o yaml

# Update a provider
flightctl edit ap my-oidc-provider

# Delete a provider
flightctl delete ap my-oidc-provider
```

## When to Use OIDC Authentication

- ✅ Production deployments with corporate SSO.
- ✅ Development and testing environments
- ✅ Multiple identity sources
- ✅ Need flexible organization and role mapping

## Login

Users authenticate via web browser:

```bash
flightctl login https://flightctl.example.com --web
```

The CLI will open a browser for authentication. Users select their OIDC provider from the login page and complete authentication through the provider's interface.

## Related Documentation

- [Authentication Overview](overview.md) - Overview of all authentication methods
- [OAuth2 Authentication](auth-oauth2.md) - Alternative for non-OIDC providers
- [PAM Issuer](auth-pam.md) - Bundled OIDC provider for Linux Deployment
- [Organizations](organizations.md) - Multi-tenancy configuration
- [API Resources](../../references/auth-resources.md) - Authorization reference
