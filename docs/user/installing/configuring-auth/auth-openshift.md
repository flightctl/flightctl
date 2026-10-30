# OpenShift Authentication

Flight Control API server integrates with OpenShift OAuth for seamless authentication in OpenShift deployments.

## How Flight Control Handles OpenShift Authentication

Flight Control API server integrates with OpenShift OAuth by:

- Validating OAuth tokens with the Kubernetes `SelfSubjectReview` API
- Mapping OpenShift users to Flight Control users
- Auto-mapping OpenShift projects to Flight Control organizations
- Using RoleBindings from project namespaces for authorization

## Standard ClusterRoles Provided

Flight Control provides the following standard ClusterRoles out-of-the-box:

- **`flightctl-admin-<namespace>`** - Full access to all Flight Control resources
- **`flightctl-org-admin-<namespace>`** - Full access to all Flight Control resources within an organization
- **`flightctl-operator-<namespace>`** - CRUD operations on devices, fleets, resourcesyncs, repositories; application lifecycle (start/stop/restart); device and application console; imagebuilds (including cancel and logs); imageexports (including cancel, download, and logs)
- **`flightctl-viewer-<namespace>`** - Read-only access to devices, fleets, resourcesyncs, organizations; imagebuilds and imageexports (including logs, but no download)
- **`flightctl-installer-<namespace>`** - Access to enrollmentrequests, certificate signing requests; view imagebuilds and imageexports; download imageexports

**Note:** ClusterRole names include a namespace suffix (e.g., `flightctl-admin-<namespace>`). The `<namespace>` value matches your Helm release namespace. When creating RoleBindings, you must use the suffixed ClusterRole names.

**Note:** Flight Control automatically creates a service account named `flightctl-admin`. In cluster-scoped installs it is bound to the global `flightctl-admin-<namespace>` role. In namespace-scoped installs it is bound to the project-scoped `flightctl-org-admin-<namespace>` role. To use other role types, create your own service account and bind it to the desired role.

### Namespace-scoped installations

When you set `global.namespaceScoped=true`, Flight Control creates the standard roles as namespaced Roles and does not create security context constraints. Install the [tenant chart](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl-tenant/README.md) in each additional project; it creates the project Roles, RoleBindings, and the RoleBinding that lets the control plane read RoleBindings for organization mapping. For more information, see [Installing without cluster-admin permissions](../installing-service-on-kubernetes.md#installing-without-cluster-admin-permissions).

The installer must already hold the permissions delegated by these Roles, including `flightctl.io` permissions, or have explicit namespaced `escalate` and `bind` authorization. Stock OpenShift project-admin permissions alone do not satisfy this requirement.

### Service-account OAuth client

By default, OpenShift authentication uses a cluster-scoped OAuthClient. For a namespace-only install, set `global.auth.openshift.clientType=serviceaccount` to use the `flightctl-api` ServiceAccount as the OAuth client instead. The chart then:

- Annotates the `flightctl-api` ServiceAccount with the UI and CLI redirect URIs.
- Uses the client ID `system:serviceaccount:<namespace>:flightctl-api`.
- Requests the scopes `user:info user:check-access user:list-projects` by default.
- Does not create an OAuthClient.

The API token proxy injects the projected ServiceAccount token as the OAuth client credential. The credential is not included in the public `/auth/config` response, and it is reread from the token file so rotation is picked up. Clients must use the Flight Control token proxy for the authorization-code exchange; the CLI requires `--web`.

Because the resulting token is scope-constrained, Flight Control uses different OpenShift APIs:

- Identity: `GET /apis/user.openshift.io/v1/users/~`.
- Access checks: Kubernetes `SelfSubjectAccessReview`.
- Permission listings: OpenShift `authorization.openshift.io/v1` self-subject rules review with an explicit empty scope list.
- Project discovery: `GET /apis/project.openshift.io/v1/projects` with the user's token.

OpenShift disables refresh tokens for these clients, so users must re-authenticate when the token expires. Verify service-account client and `user:list-projects` behavior on your OpenShift version before relying on it; the OpenShift 4.20/4.21 source supports both, while the service-account documentation does not list the project-list scopes.

Example values:

```yaml
global:
  auth:
    type: openshift
    openshift:
      clientType: serviceaccount
      authorizationUrl: https://oauth-openshift.apps.example.com/oauth/authorize
      tokenUrl: https://oauth-openshift.apps.example.com/oauth/token
      issuer: https://oauth-openshift.apps.example.com
```

The tenant chart remains required for additional projects and their RoleBinding-based role mappings.

## Organization Mapping

Flight Control automatically maps OpenShift projects to Flight Control organizations:

- Each OpenShift project becomes a Flight Control organization
- Users inherit access to Flight Control organizations based on their OpenShift project membership

### Project Filtering

By default, Flight Control only considers OpenShift projects labeled with `io.flightctl/instance=<releaseName>`. To include a project, have an administrator authorized to update Namespace labels apply the label:

```bash
oc label namespace my-project io.flightctl/instance=my-release
```

Only projects with this label will be mapped to Flight Control organizations. You can customize the label selector via `global.auth.openshift.projectLabelFilter` in your Helm values.

Stock project-admin permissions do not authorize Namespace label updates.

## Authorization

Flight Control uses RoleBindings from project namespaces to determine user permissions:

1. User authenticates via OpenShift OAuth
2. Flight Control retrieves user's OpenShift projects
3. For each project, Flight Control checks RoleBindings in the project namespace
4. Permissions are mapped based on ClusterRoles bound to the user

**Note:** Any role or organization configuration changes require users to log in again (with a different token if you logged using an Openshift token) or wait approximately 5 minutes to receive updated assignments.

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

Optional: `global.auth.openshift.organizationNamePrefix`. When set, OpenShift project names are exposed in Flight Control as prefix + project name (e.g. `ocp-` + `my-project` → `ocp-my-project`). Useful when mixing multiple auth sources so OpenShift-backed organizations have a distinct name prefix.

**Important:** Set `organizationNamePrefix` during initial deployment only. Adding or changing the prefix on an existing deployment changes organization names, causing users to lose access to resources created under the previous names. If you must change the prefix, plan for data migration.

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

Use `oc adm policy add-role-to-user` to assign roles to users in your project. **Important:** You must use the namespace-specific role names where `<namespace>` is your Helm release namespace. Cluster-scoped installs create ClusterRoles; namespace-scoped installs create Roles and the tenant chart creates them in each additional project.

```bash
# Required: Grant view permissions so the user has access to the project
oc adm policy add-role-to-user view my-user -n my-org

# Choose one of the following Flight Control roles based on the user's needs:
# Replace <namespace> with your Helm release namespace (e.g., "flightctl" or "flightctl-prod")
# Grant Flight Control organization-admin permissions
oc adm policy add-role-to-user flightctl-org-admin-<namespace> my-user -n my-org

# Grant Flight Control operator permissions
oc adm policy add-role-to-user flightctl-operator-<namespace> my-user -n my-org

# Grant Flight Control viewer permissions
oc adm policy add-role-to-user flightctl-viewer-<namespace> my-user -n my-org
```

**Example:** If your Helm release namespace is `flightctl`, use:

```bash
oc adm policy add-role-to-user flightctl-org-admin-flightctl my-user -n my-org
```

**Note:** `flightctl-admin-<namespace>` is a global super-admin role and must not be bound in a single project. Use `flightctl-org-admin-<namespace>` for a project-scoped administrator.

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

Users with access to multiple OpenShift projects will have access to multiple Flight Control organizations.
For CLI organization selection and `--org` usage, see [Usage with multiple organizations](organizations.md#usage-with-multiple-organizations).

## Troubleshooting

**Error: "unable to log in to the application" / "You do not have access to any organizations"**  
Make sure the user has a RoleBinding attached to the **view** role in at least one organization (namespace) and that the namespace has the right label.

**403 error when performing actions on flightctl resources**  
Make sure the user has a RoleBinding attached to a role with the right permission in the organization's namespace.

## Related Documentation

- [Authentication Overview](overview.md) - Overview of all authentication methods
- [API Resources](../../references/auth-resources.md) - Authorization reference and API endpoints
- [Organizations](organizations.md) - Multi-tenancy configuration
- [Kubernetes Authentication](auth-kubernetes.md) - For Kubernetes deployments
