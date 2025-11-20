# OpenShift Authentication

Flight Control API server integrates with OpenShift OAuth for seamless authentication in OpenShift deployments.

## How Flight Control Handles OpenShift Authentication

Flight Control API server integrates with OpenShift OAuth by:

- Validating OAuth tokens against OpenShift OAuth server
- Mapping OpenShift users to Flight Control users
- Auto-mapping OpenShift projects to Flight Control organizations
- Using RoleBindings from project namespaces for authorization

## Standard ClusterRoles Provided

Flight Control provides the following standard ClusterRoles out-of-the-box:

- **`flightctl-admin`** - Full access to all Flight Control resources
- **`flightctl-operator`** - CRUD operations on devices, fleets, resourcesyncs, repositories
- **`flightctl-viewer`** - Read-only access to devices, fleets, resourcesyncs, organizations
- **`flightctl-installer`** - Access to enrollmentrequests and certificate signing requests

## Organization Mapping

Flight Control automatically maps OpenShift projects to Flight Control organizations:

- Each OpenShift project becomes a Flight Control organization
- Users inherit access to Flight Control organizations based on their OpenShift project membership

## Authorization

Flight Control uses RoleBindings from project namespaces to determine user permissions:

1. User authenticates via OpenShift OAuth
2. Flight Control retrieves user's OpenShift projects
3. For each project, Flight Control checks RoleBindings in the project namespace
4. Permissions are mapped based on ClusterRoles bound to the user

## Configuration

### Static Configuration

OpenShift authentication is configured via Helm values or configuration files.

Configuration values need to be set in `values.yaml` (see [values.yaml](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl/values.yaml) global/auth section):

```yaml
global:
  auth:
    type: openshift
    # Additional OpenShift-specific configuration
```

### Single Provider

One OpenShift cluster per Flight Control deployment. Changes to authentication configuration require service restart.

## When to Use OpenShift Authentication

- ✅ Deploying on OpenShift
- ✅ Want seamless OpenShift integration
- ✅ Need automatic project-to-organization mapping
- ✅ Want to leverage existing OpenShift RBAC

## Login

Users authenticate via web browser using their OpenShift credentials:

```bash
flightctl login https://flightctl.example.com --web
```

The CLI opens a browser for OpenShift authentication. Users log in with their OpenShift cluster credentials, and Flight Control validates the OAuth token with the OpenShift OAuth server.

## Setting Up User Permissions

### 1. Create RoleBinding in Project

Bind users to Flight Control ClusterRoles in your OpenShift project:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: user-flightctl-operator
  namespace: my-project  # Your OpenShift project
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: flightctl-operator
subjects:
  - kind: User
    name: alice@example.com
    apiGroup: rbac.authorization.k8s.io
```

### 2. User Accesses Flight Control

When the user logs into Flight Control:

1. They authenticate via OpenShift OAuth
2. Flight Control maps `my-project` to a Flight Control organization
3. The user gets `flightctl-operator` permissions in that organization

## Example Permissions Setup

### Admin User

Grant full access to a project admin:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: admin-flightctl-admin
  namespace: my-project
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: flightctl-admin
subjects:
  - kind: User
    name: admin@example.com
    apiGroup: rbac.authorization.k8s.io
```

### Operator User

Grant operational access:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ops-flightctl-operator
  namespace: my-project
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: flightctl-operator
subjects:
  - kind: User
    name: operator@example.com
    apiGroup: rbac.authorization.k8s.io
```

### Viewer User

Grant read-only access:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: viewer-flightctl-viewer
  namespace: my-project
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: flightctl-viewer
subjects:
  - kind: User
    name: viewer@example.com
    apiGroup: rbac.authorization.k8s.io
```

## Multi-Project Access

Users with access to multiple OpenShift projects will have access to multiple Flight Control organizations:

```bash
# User has access to project-a and project-b in OpenShift
# After login, they can access both organizations in Flight Control

flightctl get devices --org project-a
flightctl get devices --org project-b
```

## Related Documentation

- [Authentication Overview](authentication-overview.md) - Overview of all authentication methods
- [API Resources](auth-resources.md) - Authorization reference and API endpoints
- [Organizations](organizations.md) - Multi-tenancy configuration
- [Kubernetes Authentication](kubernetes-auth.md) - For Kubernetes deployments
