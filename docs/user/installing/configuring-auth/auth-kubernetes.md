# Kubernetes Authentication

Flight Control API server supports Kubernetes service account-based authentication for Kubernetes deployments.

## How Flight Control Handles Kubernetes Authentication

Flight Control API server validates Kubernetes service account tokens by:

- Sending TokenReview requests to the Kubernetes API server
- Validating the token's authenticity and user identity
- Mapping Kubernetes users/service accounts to Flight Control users
- Checking RoleBindings in the namespace where Flight Control is deployed for authorization
- Assigning all users to the `default` organization

## Standard ClusterRoles Provided

Flight Control provides the following standard ClusterRoles out-of-the-box:

- **`flightctl-admin-<namespace>`** - Full access to all Flight Control resources
- **`flightctl-operator-<namespace>`** - CRUD operations on devices, fleets, resourcesyncs, repositories
- **`flightctl-viewer-<namespace>`** - Read-only access to devices, fleets, resourcesyncs, organizations
- **`flightctl-installer-<namespace>`** - Access to enrollmentrequests and certificate signing requests

**Note:** ClusterRole names include a namespace suffix (e.g., `flightctl-admin-<namespace>`). The `<namespace>` value matches your Helm release namespace. When creating RoleBindings, you must use the suffixed ClusterRole names.

**Note:** Flight Control automatically creates a service account named `flightctl-admin` with the `flightctl-admin-<namespace>` role; to use other role types, create your own service account and bind it to the desired ClusterRole.

## Configuration

### Static Configuration

Kubernetes authentication is configured via Helm values or configuration files.

Some `helm` configuration values need to be modified (see [values.yaml](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl/values.yaml) global/auth section).

The following variables need to be set:

- Set **global.auth.type** to **k8s**
- Optionally set **global.auth.caCert**, **global.auth.insecureSkipTlsVerify**

With these settings, the k8s cluster on which Flight Control will be deployed will be used as auth authority.

### Using External Kubernetes Cluster

If you want to use a different cluster as auth authority, the following variables need to be set:

- Set **global.auth.k8s.apiUrl** to API URL of the external k8s cluster
- Set **global.auth.k8s.externalApiToken** to a token which has permission to CREATE `authentication.k8s.io/tokenreview` resource in the external k8s cluster

### OpenShift Clusters

If your cluster is OpenShift, use [OpenShift Authentication](auth-openshift.md) instead for better integration.

### ACM Deployments

If deploying on ACM (Advanced Cluster Management), use [OpenShift Authentication](auth-openshift.md) instead of Kubernetes authentication.

## Limitations

- **Multi-organization NOT supported**: All users are assigned to the `default` organization
- **Single provider**: One Kubernetes cluster per Flight Control deployment
- **Static configuration**: Changes require service restart

## When to Use Kubernetes Authentication

- ✅ Kubernetes deployments (non-OpenShift)
- ✅ Want to reuse existing Kubernetes RBAC
- ✅ Integration with Kubernetes identity
- ✅ Familiar with Kubernetes RBAC concepts

**Note:** For OpenShift deployments, use [OpenShift authentication](auth-openshift.md) instead for better integration.

## Kubernetes RBAC Authorization

Flight Control uses [Kubernetes RBAC authorization](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) by checking **RoleBindings** in the namespace where Flight Control is deployed.

**How it works:**

- Flight Control provides standard **ClusterRoles** with namespace-specific names (e.g., `flightctl-admin-<namespace>`, `flightctl-operator-<namespace>`)
- You create **RoleBindings** in the Flight Control namespace to grant users these roles
- Flight Control checks RoleBindings to determine user permissions
- The namespace suffix enables multiple Flight Control deployments in the same cluster without name conflicts

**Note:** Any role or organization configuration changes require users to log in again or wait approximately 5 minutes to receive updated assignments.

## API Endpoints

API endpoints are documented in [Authentication resources](../../references/auth-resources.md). The resources and verbs in this document should be used for creating API objects **Role** or **ClusterRole**.

## Login

### Using Service Account Token

Users must create a service account token and authenticate with it.

**Create a token for a service account:**

```bash
# Create a token for a service account (valid for 1 hour by default)
kubectl create token <service-account-name> -n <namespace>

# Create a token with custom expiration (e.g., 24 hours)
kubectl create token <service-account-name> -n <namespace> --duration=24h
```

**Login with the token:**

```bash
# Login with the token
flightctl login https://flightctl.example.com --token=<token>
```

Or paste the token in the UI token field.

### Using flightctl-admin Service Account

The deployment automatically creates a `flightctl-admin` service account (the service account name does not include the release suffix). To use it:

```bash
# Create a token for the flightctl-admin service account
# Replace <namespace> with your Flight Control namespace (typically matches your Helm release name)
kubectl create token flightctl-admin -n <namespace>

# Login with the token
flightctl login https://flightctl.example.com --token=<token>
```

**Example with custom expiration:**

```bash
# Create a long-lived token (30 days)
# Replace <namespace> with your Flight Control namespace
TOKEN=$(kubectl create token flightctl-admin -n <namespace> --duration=720h)

# Login
flightctl login https://flightctl.example.com --token=$TOKEN
```

**Note:** When creating RoleBindings for users or service accounts, remember to reference the namespace-specific ClusterRole names (e.g., `flightctl-admin-<namespace>`) in the RoleBinding's `roleRef.name` field.

## Related Documentation

- [Authentication Overview](overview.md) - Overview of all authentication methods
- [API Resources](../../references/auth-resources.md) - Authorization reference and API endpoints
- [Organizations](organizations.md) - Multi-tenancy configuration
- [OpenShift Authentication](auth-openshift.md) - For OpenShift deployments
