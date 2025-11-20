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

- **`flightctl-admin`** - Full access to all Flight Control resources
- **`flightctl-operator`** - CRUD operations on devices, fleets, resourcesyncs, repositories
- **`flightctl-viewer`** - Read-only access to devices, fleets, resourcesyncs, organizations
- **`flightctl-installer`** - Access to enrollmentrequests and certificate signing requests

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

If your cluster is OpenShift, use [OpenShift Authentication](openshift-auth.md) instead for better integration.

### ACM Deployments

If deploying on ACM (by global.target: acm), the k8s auth values are automatically calculated.

## Limitations

- **Multi-organization NOT supported**: All users are assigned to the `default` organization
- **Single provider**: One Kubernetes cluster per Flight Control deployment
- **Static configuration**: Changes require service restart

## When to Use Kubernetes Authentication

- ✅ Kubernetes deployments (non-OpenShift)
- ✅ Want to reuse existing Kubernetes RBAC
- ✅ Integration with Kubernetes identity
- ✅ Familiar with Kubernetes RBAC concepts

**Note:** For OpenShift deployments, use [OpenShift authentication](openshift-auth.md) instead for better integration.

## Kubernetes RBAC Authorization

Flight Control uses [Kubernetes RBAC authorization](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) by checking **RoleBindings** in the namespace where Flight Control is deployed.

**How it works:**

- Flight Control provides standard **ClusterRoles** (e.g., `flightctl-admin`, `flightctl-operator`)
- You create **RoleBindings** in the Flight Control namespace to grant users these roles
- Flight Control checks RoleBindings to determine user permissions

## API Endpoints

API endpoints are documented in [Authentication resources](auth-resources.md). The resources and verbs in this document should be used for creating API objects **Role** or **ClusterRole**.

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

The deployment automatically creates a `flightctl-admin` service account. To use it:

```bash
# Create a token for the flightctl-admin service account
kubectl create token flightctl-admin -n flightctl

# Login with the token
flightctl login https://flightctl.example.com --token=<token>
```

**Example with custom expiration:**

```bash
# Create a long-lived token (30 days)
TOKEN=$(kubectl create token flightctl-admin -n flightctl --duration=720h)

# Login
flightctl login https://flightctl.example.com --token=$TOKEN
```

## Related Documentation

- [Authentication Overview](authentication-overview.md) - Overview of all authentication methods
- [API Resources](auth-resources.md) - Authorization reference and API endpoints
- [Organizations](organizations.md) - Multi-tenancy configuration
- [OpenShift Authentication](openshift-auth.md) - For OpenShift deployments
