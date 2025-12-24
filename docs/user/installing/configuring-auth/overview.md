# Authentication Overview

This guide provides an overview of authentication in Flight Control to help you understand the available options and choose the right authentication method for your deployment.

## Default Authentication by Deployment

Flight Control automatically configures authentication based on your deployment environment:

| Deployment Environment | Default Authentication | Description |
|------------------------|----------------------|-------------|
| **AAP (Ansible Automation Platform)** | AAP Gateway | Integrates with AAP Gateway for authentication and authorization |
| **OpenShift** | OpenShift OAuth | Uses OpenShift's OAuth server for authentication |
| **Standalone (Quadlet/Podman)** | OIDC | Flight Control API server uses OIDC authentication. [PAM Issuer](auth-pam.md) (bundled OIDC provider) is deployed by default |
| **Kubernetes (non-OpenShift)** | Kubernetes RBAC | Uses Kubernetes service accounts and RBAC for authentication |

### Supported Authentication Methods

Flight Control API server supports the following authentication methods:

#### 1. OIDC (OpenID Connect)

Standard OpenID Connect protocol. Works with any OIDC-compliant provider. Supports dynamic provider configuration, flexible organization and role mapping, and multiple simultaneous providers.

→ [OIDC Authentication Documentation](auth-oidc.md)

#### 2. OAuth2

Generic OAuth2 protocol for providers that don't fully support OIDC. Supports dynamic provider configuration and flexible organization and role mapping.

→ [OAuth2 Authentication Documentation](auth-oauth2.md)

#### 3. Kubernetes

Validates Kubernetes service account tokens via TokenReview API. Maps to RoleBindings in the namespace where Flight Control is deployed. All users assigned to `default` organization.

→ [Kubernetes Authentication Documentation](auth-kubernetes.md)

#### 4. OpenShift

Integrates with OpenShift OAuth server. Auto-maps OpenShift projects to Flight Control organizations and uses RoleBindings from project namespaces.

→ [OpenShift Authentication Documentation](auth-openshift.md)

#### 5. AAP (Ansible Automation Platform)

Validates tokens via AAP Gateway API. Auto-maps AAP organizations to Flight Control organizations. Restricted to AAP super admins only.

→ [AAP Authentication Documentation](auth-aap.md)

## Managing Authentication Providers

### Managing Providers via the Flight Control UI

Users with administrative permissions can use the Flight Control UI to manage dynamic OIDC Providers.

1. **Access Settings**: Click the **Settings** button (⚙️) in the navigation bar. This button is only visible to users with administrative permissions.

2. **Navigate to Authentication Providers**: The Settings button takes you to the Authentication Providers page, where you can view all configured providers.

3. **Add a New Provider**: Click the **Add authentication provider** button to open the provider creation form.

4. **Edit an existing Provider**: Click the **Edit** button from the menu actions of a specific provider.

### Managing Providers via the Flight Control API/CLI

> **Note:** The examples below use the Flight Control CLI. See [Using the CLI](../../using/cli/overview.md) and [Logging in to the Service](../../using/cli/logging-in.md) for more information.

Flight Control supports two types of authentication provider configuration:

### Dynamic Configuration (OIDC/OAuth2)

OIDC and OAuth2 providers can be managed dynamically through the Flight Control API/CLI. This is the recommended approach for adding corporate SSO providers:

**Creating a Provider:**

```bash
flightctl apply -f - <<EOF
apiVersion: v1beta1
kind: AuthProvider
metadata:
  name: corporate-oidc
spec:
  providerType: oidc
  displayName: "Corporate SSO"
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

> **Note:** Organizations configured via `organizationAssignment` will not appear in Flight Control until a user actually logs in with that organization.

**Managing Providers:**

```bash
# List providers (ap is short for authprovider/authproviders)
flightctl get ap

# Get details
flightctl get ap corporate-oidc -o yaml

# Update provider
flightctl edit ap corporate-oidc

# Delete provider
flightctl delete ap corporate-oidc
```

**Benefits of Dynamic Configuration:**

- Add/remove OIDC/OAuth2 providers without service restart
- Multiple OIDC/OAuth2 providers can coexist
- Changes take effect immediately
- Ideal for adding corporate SSO after deployment
- Works alongside static authentication configuration

### Static Configuration (OIDC/OAuth2/Kubernetes/OpenShift/AAP)

All authentication methods can be configured statically in deployment files. Static configuration is **required** for Kubernetes, OpenShift, and AAP, but **optional** for OIDC and OAuth2 (which can also be configured dynamically).

**Characteristics:**

- Configured once during deployment
- Requires service restart for changes
- One static provider per Flight Control deployment
- Automatically configured based on deployment environment
- **Note:** OIDC and OAuth2 providers can also be added dynamically via API/CLI in addition to or instead of static configuration

For detailed configuration examples, see the specific authentication method documentation linked below.

## Related Documentation

- [OIDC Authentication](auth-oidc.md) - OIDC provider setup and configuration
- [OAuth2 Authentication](auth-oauth2.md) - OAuth2 provider setup
- [Kubernetes Authentication](auth-kubernetes.md) - Kubernetes RBAC integration
- [OpenShift Authentication](auth-openshift.md) - OpenShift OAuth integration
- [AAP Authentication](auth-aap.md) - AAP Gateway integration
- [PAM Issuer](auth-pam.md) - Bundled OIDC provider for Linux Deployment
- [Organizations](organizations.md) - Multi-tenancy configuration
- [API Resources](../../references/auth-resources.md) - Authorization reference
