# OAuth2 Authentication

Flight Control API server supports generic OAuth2 authentication for providers that don't fully support OIDC.

## How Flight Control Handles OAuth2

Flight Control API server implements OAuth2 authentication for providers that don't fully support OIDC. It:

- Performs OAuth2 authorization code flow
- Exchanges authorization codes for access tokens
- Validates access tokens with the provider
- Retrieves user information from provider's user info endpoint (if available)

## When to Use OAuth2

Use OAuth2 authentication when:

- ✅ Your provider supports OAuth2 but not OIDC

**Note:** If your provider supports OIDC, use [OIDC authentication](auth-oidc.md) instead as it provides better standardization.

## Organization and Role Mapping

Similar to OIDC, Flight Control maps organizations and roles from OAuth2 userinfo based on AuthProvider configuration:

### Organization Assignment

Configure how users are assigned to organizations via `organizationAssignment` in the AuthProvider:

- **Static** (`type: static`): Assigns all users to a specific organization
- **Dynamic** (`type: dynamic`): Maps organizations from userinfo claims using JSON path
  - `claimPath`: JSON path array to navigate to the organization claim
    - Example: `["groups"]` for `userinfo.groups`
    - Example: `["custom", "user_context", "organizations"]` for `userinfo.custom.user_context.organizations`
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
    - Example: `["custom", "roles"]` for `userinfo.custom.roles`
    - Example: `["custom", "user_context", "roles"]` for `userinfo.custom.user_context.roles`
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

### Token Introspection

OAuth2 providers require token introspection to validate access tokens. Flight Control supports three introspection methods:

#### 1. RFC 7662 Introspection

Standard OAuth2 token introspection endpoint:

```yaml
introspection:
  type: rfc7662
  url: "https://oauth.example.com/introspect"
```

#### 2. GitHub Introspection

For GitHub or GitHub Enterprise OAuth2 apps:

```yaml
introspection:
  type: github
  url: "https://api.github.enterprise.com/v3"  # Optional, only for GitHub Enterprise
```

**Note:** For public GitHub (github.com), the `url` field can be omitted and will default to the public GitHub API.

#### 3. JWT Introspection

For providers that issue JWT access tokens:

```yaml
introspection:
  type: jwt
  jwksUrl: "https://oauth.example.com/.well-known/jwks.json"
  issuer: "https://oauth.example.com"  # Optional
  audience:  # Optional
    - "flightctl-client"
    - "another-audience"
```

**Optional Fields Behavior:**

- **`issuer`**: When not provided, falls back to the OAuth2 provider's `issuer` field. If both are omitted, issuer validation is skipped (not recommended for production).
  
- **`audience`**: When not provided, defaults to the OAuth2 provider's `clientId`. If you specify multiple audiences, the JWT must contain at least one of them. If omitted entirely and no clientId is set, audience validation is skipped (not recommended for production).

**Security Note:** While `issuer` and `audience` are optional, it's strongly recommended to provide them for production environments to ensure proper token validation. Omitting these fields reduces security as token claims won't be validated against expected values.

#### Automatic Inference

Flight Control automatically infers introspection configuration for known providers:

- **GitHub URLs** (e.g., `github.com/login/oauth/authorize`) → GitHub introspection
- **Standard OAuth2 URLs** → Attempts to infer RFC 7662 endpoint from `tokenUrl` or `issuer`

If inference fails or you need a specific configuration, you must provide the `introspection` field explicitly.

**Important:** Once the `introspection` field is set, it cannot be removed. You can update it to a different introspection method, but the field itself is required for all OAuth2 providers.

### Redirect URLs

Configure the following redirect URLs in both Flight Control and your OAuth2 provider:

- `<UI_URL>/callback` - Web UI callback
- `http://localhost:8080/callback` - CLI webserver callback (default port 8080)

### Dynamic Provider Management

OAuth2 providers are configured dynamically via AuthProvider resources:

- Create AuthProvider resources via API/CLI
- Update provider configuration without service restart
- Delete providers when no longer needed
- Support multiple OAuth2 providers simultaneously

### Creating an OAuth2 Provider

```bash
flightctl apply -f authprovider.yaml
```

Or create directly:

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: my-oauth2-provider
spec:
  providerType: oauth2
  displayName: "Legacy OAuth2 Provider"
  issuer: "https://oauth.example.com"
  authorizationUrl: "https://oauth.example.com/authorize"
  tokenUrl: "https://oauth.example.com/token"
  userinfoUrl: "https://oauth.example.com/userinfo"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - profile
    - email
  introspection:
    type: rfc7662
    url: "https://oauth.example.com/introspect"
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-operator
EOF
```

**Note:** The `introspection` field may be automatically inferred based on the provider URLs. If automatic inference works for your provider, you can omit the `introspection` field during creation, but it cannot be removed once set.

**Example with dynamic organization and role assignment:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: oauth2-dynamic
spec:
  providerType: oauth2
  displayName: "OAuth2 Provider (Dynamic)"
  issuer: "https://oauth.example.com"
  authorizationUrl: "https://oauth.example.com/authorize"
  tokenUrl: "https://oauth.example.com/token"
  userinfoUrl: "https://oauth.example.com/userinfo"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - profile
    - email
  organizationAssignment:
    type: dynamic
    claimPath: ["custom", "user_context", "organizations"]  # Nested JSON path
    organizationNamePrefix: "org-"  # Optional: prefix for org names
  roleAssignment:
    type: dynamic
    claimPath: ["custom", "user_context", "roles"]  # Nested JSON path
    separator: ":"  # Separator for org:role format (default: ":")
EOF
```

This configuration will:

- Map organizations from nested claim: `userinfo.custom.user_context.organizations`
  - The `claimPath: ["custom", "user_context", "organizations"]` navigates to the nested array
- Prefix organization names with `org-`
- Map roles from nested claim: `userinfo.custom.user_context.roles`
  - The `claimPath: ["custom", "user_context", "roles"]` navigates to the nested array
- Support role scoping with `:` separator (e.g., `org1:role1` for org-specific, `role1` for global)

**Example with simple (non-nested) claim paths:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: oauth2-simple
spec:
  providerType: oauth2
  displayName: "OAuth2 Provider (Simple)"
  issuer: "https://oauth.example.com"
  authorizationUrl: "https://oauth.example.com/authorize"
  tokenUrl: "https://oauth.example.com/token"
  userinfoUrl: "https://oauth.example.com/userinfo"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
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

**Example with per-user organization assignment:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: oauth2-peruser
spec:
  providerType: oauth2
  displayName: "OAuth2 Provider (Per User)"
  issuer: "https://oauth.example.com"
  authorizationUrl: "https://oauth.example.com/authorize"
  tokenUrl: "https://oauth.example.com/token"
  userinfoUrl: "https://oauth.example.com/userinfo"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
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

**Example with GitHub OAuth2:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: github-oauth2
spec:
  providerType: oauth2
  displayName: "GitHub OAuth2"
  authorizationUrl: "https://github.com/login/oauth/authorize"
  tokenUrl: "https://github.com/login/oauth/access_token"
  userinfoUrl: "https://api.github.com/user"
  clientId: "your-github-client-id"
  clientSecret: "your-github-client-secret"
  enabled: true
  scopes:
    - read:user
    - read:org
  introspection:
    type: github
    # url field is optional for public GitHub, required for GitHub Enterprise
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-operator
EOF
```

**Note:** For GitHub, the `introspection` configuration is automatically inferred from the GitHub URLs, so you can omit it during creation.

**Example with JWT-based OAuth2:**

```bash
cat <<EOF | flightctl apply -f -
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: jwt-oauth2
spec:
  providerType: oauth2
  displayName: "JWT OAuth2 Provider"
  issuer: "https://oauth.example.com"
  authorizationUrl: "https://oauth.example.com/authorize"
  tokenUrl: "https://oauth.example.com/token"
  userinfoUrl: "https://oauth.example.com/userinfo"
  clientId: "flightctl-client"
  clientSecret: "your-client-secret"
  enabled: true
  scopes:
    - profile
    - email
  introspection:
    type: jwt
    jwksUrl: "https://oauth.example.com/.well-known/jwks.json"
    issuer: "https://oauth.example.com"  # Falls back to spec.issuer if omitted
    audience:  # Falls back to spec.clientId if omitted
      - "flightctl-client"
  organizationAssignment:
    type: static
    organizationName: default
  roleAssignment:
    type: static
    roles:
      - flightctl-operator
EOF
```

This configuration is useful when your OAuth2 provider issues JWT access tokens that can be validated locally using a JWKS endpoint, avoiding the need for remote introspection calls.

**Simplified JWT Configuration:**

If your JWT issuer matches the OAuth2 provider's issuer and your audience is the clientId, you can simplify:

```yaml
introspection:
  type: jwt
  jwksUrl: "https://oauth.example.com/.well-known/jwks.json"
  # issuer and audience will default to spec.issuer and spec.clientId
```

### Managing OAuth2 Providers

```bash
# List all providers (ap is short for authprovider/authproviders)
flightctl get ap

# Get provider details
flightctl get ap my-oauth2-provider -o yaml

# Update a provider
flightctl edit ap my-oauth2-provider

# Delete a provider
flightctl delete ap my-oauth2-provider
```

## Login

Users authenticate via web browser:

```bash
flightctl login https://flightctl.example.com --web
```

The CLI will open a browser for authentication. Users select their OAuth2 provider from the login page and complete authentication through the provider's interface.

## Related Documentation

- [Authentication Overview](overview.md) - Overview of all authentication methods
- [OIDC Authentication](auth-oidc.md) - Preferred authentication method
- [Organizations](organizations.md) - Multi-tenancy configuration
- [API Resources](../../references/auth-resources.md) - Authorization reference
