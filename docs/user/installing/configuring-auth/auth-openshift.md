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

- **`flightctl-admin-<namespace>`** - Full access to all Flight Control resources
- **`flightctl-operator-<namespace>`** - CRUD operations on devices, fleets, resourcesyncs, repositories
- **`flightctl-viewer-<namespace>`** - Read-only access to devices, fleets, resourcesyncs, organizations
- **`flightctl-installer-<namespace>`** - Access to enrollmentrequests and certificate signing requests

**Note:** ClusterRole names include a namespace suffix (e.g., `flightctl-admin-<namespace>`). The `<namespace>` value matches your Helm release namespace. When creating RoleBindings, you must use the suffixed ClusterRole names.

**Note:** Flight Control automatically creates a service account named `flightctl-admin` with the `flightctl-admin-<namespace>` role; to use other role types, create your own service account and bind it to the desired ClusterRole.

## Organization Mapping

Flight Control automatically maps OpenShift projects to Flight Control organizations:

- Each OpenShift project becomes a Flight Control organization
- Users inherit access to Flight Control organizations based on their OpenShift project membership

### Project Filtering

By default, Flight Control only considers OpenShift projects labeled with `io.flightctl/instance=<releaseName>`. To include a project, label it:

```bash
oc label namespace my-project io.flightctl/instance=my-release
```

Only projects with this label will be mapped to Flight Control organizations. You can customize the label selector via `global.auth.openshift.projectLabelFilter` in your Helm values.

## Authorization

Flight Control uses RoleBindings from project namespaces to determine user permissions:

1. User authenticates via OpenShift OAuth
2. Flight Control retrieves user's OpenShift projects
3. For each project, Flight Control checks RoleBindings in the project namespace
4. Permissions are mapped based on ClusterRoles bound to the user

**Note:** Any role or organization configuration changes require users to log in again or wait approximately 5 minutes to receive updated assignments.

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

You can manage user roles directly using the OpenShift CLI (`oc`).

### 1. Create or Use an Existing Project

Create a new OpenShift project (which will be mapped to a Flight Control organization), or use an existing one:

```bash
oc new-project my-org
```

### 2. Assign Roles to Users

Use `oc adm policy add-role-to-user` to assign roles to users in your project. **Important:** You must use the namespace-specific ClusterRole names (e.g., `flightctl-admin-<namespace>`) where `<namespace>` is your Helm release namespace:

```bash
# Required: Grant view permissions so the user has access to the project
oc adm policy add-role-to-user view my-user -n my-org

# Choose one of the following Flight Control roles based on the user's needs:
# Replace <namespace> with your Helm release namespace (e.g., "flightctl" or "flightctl-prod")
# Grant Flight Control admin permissions
oc adm policy add-role-to-user flightctl-admin-<namespace> my-user -n my-org

# Grant Flight Control operator permissions
oc adm policy add-role-to-user flightctl-operator-<namespace> my-user -n my-org

# Grant Flight Control viewer permissions
oc adm policy add-role-to-user flightctl-viewer-<namespace> my-user -n my-org
```

**Example:** If your Helm release namespace is `flightctl`, use:

```bash
oc adm policy add-role-to-user flightctl-admin-flightctl my-user -n my-org
```

**Note:** You may see a warning "User 'my-user' not found" when assigning roles to users that don't exist yet in OpenShift. This is expected and the role will still be added. The user will be able to use these permissions once they authenticate.

### 3. User Login

The user can then log in to OpenShift and Flight Control:

```bash
# Login to OpenShift
oc login -u my-user -p <password> https://api.ocp.example.com:6443

# Login to Flight Control using OpenShift token
flightctl login https://flightctl.example.com -k --token=$(oc whoami -t)
```

After login, the user will automatically have access to the Flight Control organization mapped from the OpenShift project, with the permissions granted by the assigned roles.

## Multi-Project Access

Users with access to multiple OpenShift projects will have access to multiple Flight Control organizations:

```bash
# User has access to project-a and project-b in OpenShift
# After login, they can access both organizations in Flight Control

flightctl get devices --org project-a
flightctl get devices --org project-b
```

## Troubleshooting

**Error: no organizations found**  
Make sure the user has a RoleBinding attached to the **view** role in at least one organization (namespace) and that the namespace has the right label.

**403 error when performing actions on flightctl resources**  
Make sure the user has a RoleBinding attached to a role with the right permission in the organization's namespace.

## Related Documentation

- [Authentication Overview](overview.md) - Overview of all authentication methods
- [API Resources](../../references/auth-resources.md) - Authorization reference and API endpoints
- [Organizations](organizations.md) - Multi-tenancy configuration
- [Kubernetes Authentication](auth-kubernetes.md) - For Kubernetes deployments
