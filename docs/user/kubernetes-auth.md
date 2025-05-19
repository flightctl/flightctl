# Authorization with Kubernetes

Flight control Kubernetes authorization uses a Role Based Access Control (RBAC) to control authorization for flight control API endpoints.
Some `helm` configuration values are needed to be modified (see [values.yaml](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl/values.yaml) global/auth section)

The following variables need to be set:

* Set **global.auth.type** to **k8s**
* Optionally set **global.auth.caCert**, **global.auth.insecureSkipTlsVerify**

With these settings, the k8s cluster on which Flight Control will be deployed, will be used as auth authority.

If you want to use a different cluster as auth authority, the following variables need to be set too:

* Set **global.auth.k8s.apiUrl** to API URL of the external k8s cluster
* Set **global.auth.k8s.externalApiToken** to a token which has permission to CREATE `authentication.k8s.io/tokenreview` resource in the external k8s cluster

If the k8s cluster is an OpenShift cluster, you can also set **global.auth.k8s.externalOpenShiftApiUrl** which enables interactive login for the CLI (`flightctl login <fctl_url> --web`)

If deploying on ACM  (by global.target: acm), the k8s auth values are automatically calculated.

## Kubernetes RBAC authorization

To use [Kubernetes RBAC authorization](https://kubernetes.io/docs/reference/access-authn-authz/rbac/) either **Role** and **RoleBinding**
or **ClusterRole** and **ClusterRoleBinding** must be used.

Note: **Role**/**RoleBinding** are used for namespace based authorization.  **ClusterRole**/**ClusterRoleBinding** are used for cluster
wide authorization (all namespaces in a cluster).

API objects ***Role*** or ***ClusterRole***, should be used to define the allowed API resources and verbs for a particular role.

API objects ***RoleBinding*** or ***ClusterRoleBinding*** provide association between subjects (example users) to a specific role.

## API endpoints

API endpoints are documented in [Authentication resources](auth-resources.md).  The resources and verbs in this document
should be used for creating API objects ***Role*** or ***ClusterRole***.
