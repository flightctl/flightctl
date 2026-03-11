# AAP Authentication

Flight Control API server integrates with Ansible Automation Platform (AAP) Gateway for authentication in AAP deployments.

## How Flight Control Handles AAP Authentication

Flight Control API server integrates with AAP Gateway by:

- Validating tokens against AAP Gateway
- Mapping AAP users to Flight Control users
- Auto-mapping AAP organizations to Flight Control organizations
- Using role-based authorization for permission control

## Standard Roles Provided

Flight Control uses the following standard roles for authorization:

- **`flightctl-admin`** - Full access to all resources within an organization
  - **Note:** This role cannot be set directly. Users automatically receive this role when they are set as AAP super admin
- **`flightctl-org-admin`** - Full access to all resources within a specific organization
- **`flightctl-operator`** - CRUD operations on devices, fleets, resourcesyncs, repositories; imagebuilds (including cancel and logs); imageexports (including cancel, download, and logs)
- **`flightctl-viewer`** - Read-only access to all resources; imagebuilds and imageexports (including logs, but no download)
- **`flightctl-installer`** - Access to get and approve enrollmentrequests, manage certificate signing requests; view imagebuilds and imageexports; download imageexports

## Organization Mapping

Flight Control automatically maps AAP organizations to Flight Control organizations:

- Each AAP organization becomes a Flight Control organization
- Mapping is automatic and requires no manual configuration
- Users inherit access based on their AAP organization membership

## Authorization

Flight Control uses role-based authorization with organization-scoped access:

1. User authenticates via AAP interface
2. Flight Control validates token with AAP Gateway
3. Flight Control retrieves user's AAP organizations and roles
4. For each organization, Flight Control determines user permissions based on their roles
5. Permissions are mapped based on the user's assigned roles

**Note:** Any role or organization configuration changes require users to log in again or wait approximately 5 minutes to receive updated assignments.

## Configuration

### Prerequisites

Before configuring AAP authentication, ensure you have:

1. AAP Gateway Access: Administrative credentials and API access to our AAP gateway instance.  Flight Control must have network access to your AAP Gateway
2. AAP Permissions: You must have an admin user with OAuth application creation permissions in AAP Gateway

### TLS Certs

If your AAP Gateway uses a custom CA or self-signed certificates, place the CA certificate at `/etc/flightctl/pki/auth/ca.crt`. This allows Flight Control to verify the TLS connection to your AAP Gateway.

### Option 1: Automated OAuth application creation

The automated approach uses the `flightctl-api-init.service` systemd service to automatically create OAuth applications during Flight Control deployment or service startup.

#### Step 1: Configure AAP credentials in service config

Configure the AAP settings in your service configuration file (`/etc/flightctl/service-config.yaml`):

```yaml
auth:
  type: aap
  aap:
    apiUrl: https://aap-gateway.example.com
    token: your-token-here
    # clientId will be populated automatically during service initialization
    # and placed in a file at /etc/flightctl/pki/aap-client-id
    authorizationUrl: https://aap-gateway.example.com/o/authorize/
    tokenUrl: https://aap-gateway.example.com/o/token/
    displayName: "AAP Provider"
    enabled: true
```

**How to obtain an admin token:**

1. Log into your AAP Gateway web interface
2. Navigate to **Access Management** -> **Users** -> Select your user with OAuth application creation permissions -> **Tokens**
3. Click **Create Token**, leave the OAuth application field blank and select Write scope.
4. Copy the token for use in the configuration
5. Place the copied token in the `auth.aap.token` field in `service-config.yaml`

#### Step 2: Deploy or restart Flight Control services

For new installations, deploy Flight Control:

```bash
sudo systemctl start flightctl.target
```

For existing installations, restart the services to trigger the initialization:

```bash
sudo systemctl restart flightctl.target
```

#### Step 3: Verify automatic configuration update

The `flightctl-api-init.service` automatically creates the OAuth application and writes the client ID to the filesystem:

```bash
# Check that the initialization service ran successfully
sudo systemctl status flightctl-api-init.service

# View initialization service logs
sudo journalctl -u flightctl-api-init.service

# Verify the client ID was generated
sudo cat /etc/flightctl/pki/aap-client-id
```

The service safely handles multiple runs, although you can remove the `token` from the `service-config.yaml` after the OAuth application is created.

### Option 2: Manual OAuth application creation

#### Step 1: Access AAP Gateway Applications

1. Log into your AAP Gateway web interface
2. Navigate to **Access Management** → **OAuth Applications**
3. Click **Create OAuth application** to create a new application

#### Step 2: Create new OAuth Application

Configure the OAuth application with these settings:

- **Name:** `Flight Control` (or your preferred application name)
- **URL:** You can set this to your Flight Control UI URL to provide a link from within AAP to Flight Control.
- **Organization:** `Default`
- **Authorization grant type:** `Authorization code`
- **Client type:** `Public`
- **Redirect URIs:** Set to:

  ```bash
  https://your-flightctl-base-domain:443/callback http://127.0.0.1/callback
  ```

Note: The redirect URIs should be a space delimited list. Two URIs are required:

- A URL to your UI with a /callback path appended. The default base domain and port combo is `https://your-flightctl-base-domain:443/callback`, but if you have different routing configured you will need to update the URL accordingly. Make sure to replace your-flightctl-base-domain with your actual domain.
- A URL to `http://127.0.0.1/callback` to ensure login sessions using the CLI work

#### Step 4: Obtain client credentials

After creating the application:

1. Copy the **Client ID** from the application details

#### Step 5: Update Flight Control configuration

Update your service configuration file (`/etc/flightctl/service-config.yaml`):

```yaml
auth:
  type: aap
  aap:
    apiUrl: https://aap-gateway.example.com
    clientId: your-copied-client-id
    authorizationUrl: https://aap-gateway.example.com/o/authorize/
    tokenUrl: https://aap-gateway.example.com/o/token/
    displayName: "AAP Provider"
    enabled: true
```

Then start or restart the Flight Control services:

```bash
# Start
sudo systemctl start flightctl.target
# Restart
sudo systemctl restart flightctl.target
```

### Configuration parameters

The following parameters are supported for AAP authentication configuration:

| Parameter | Description | Required | Default |
|-----------|-------------|----------|---------|
| `apiUrl` | AAP Gateway API base URL | Yes | None |
| `authorizationUrl` | OAuth authorization endpoint | Yes | `{apiUrl}/o/authorize/` |
| `tokenUrl` | OAuth token endpoint | Yes | `{apiUrl}/o/token/` |
| `clientId` | OAuth client identifier | Yes | None |
| `clientSecret` | OAuth client secret | No | None |
| `token` | Admin token for app creation | No | None |
| `displayName` | Provider display name in UI | No | "AAP Provider" |
| `enabled` | Enable AAP authentication | No | true |

**Note:** The `clientId` is automatically generated when using the automated approach, or manually configured when using the manual approach.

### Single Provider

Flight Control supports one AAP Gateway per deployment. Changes to authentication configuration require restarting the Flight Control services.

## When to Use AAP Authentication

- ✅ Deploying as part of AAP
- ✅ Integration with AAP identity and permissions
- ✅ Want automatic AAP organization mapping
- ✅ Need role-based access control with organization scoping
- ✅ Users authenticate through AAP interface

## Login

**Users authenticate via AAP interface, not directly through Flight Control.**

The authentication flow:

1. User accesses Flight Control through AAP
2. AAP handles authentication
3. AAP Gateway provides token to Flight Control
4. Flight Control validates token with AAP Gateway
5. User accesses Flight Control with AAP-managed identity

Flight Control validates these existing AAP tokens rather than providing a separate login interface.

## Multi-Organization Access

Users with access to multiple AAP organizations will have access to multiple Flight Control organizations:

```bash
# User has access to org-a and org-b in AAP
# After authentication, they can access both organizations in Flight Control

flightctl get devices --org org-a
flightctl get devices --org org-b
```

## Related Documentation

- [Authentication Overview](overview.md) - Overview of all authentication methods
- [API Resources](../../references/auth-resources.md) - Authorization reference and API endpoints
- [Organizations](organizations.md) - Multi-tenancy configuration
- [OIDC Authentication](auth-oidc.md) - For OIDC deployments
