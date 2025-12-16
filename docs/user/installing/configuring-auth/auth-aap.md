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
- **`flightctl-operator`** - CRUD operations on devices, fleets, resourcesyncs, repositories
- **`flightctl-viewer`** - Read-only access to all resources
- **`flightctl-installer`** - Access to get and approve enrollmentrequests, and manage certificate signing requests

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

### Static Configuration

AAP authentication is configured via configuration files.

Configuration values need to be set in `config.yaml`:

```yaml
auth:
  type: aap
  aap:
    apiUrl: https://aap-gateway.example.com
    authorizationUrl: https://aap-gateway.example.com/o/authorize/
    tokenUrl: https://aap-gateway.example.com/o/token/
    clientId: your-client-id
    clientSecret: your-client-secret  # Optional
    displayName: "AAP Provider"  # Optional
    enabled: true  # Optional, defaults to true
```

### Single Provider

One AAP Gateway per Flight Control deployment. Changes to authentication configuration require service restart.

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
