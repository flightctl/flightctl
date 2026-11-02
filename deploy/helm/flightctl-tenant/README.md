# Flight Control tenant chart

This chart installs the per-project RBAC bundle for a namespace-scoped Flight Control installation. Install it once in each OpenShift project that Flight Control should manage.

The bundle creates:

- The `flightctl-org-admin`, `flightctl-operator`, `flightctl-viewer`, and `flightctl-installer` roles, named with the main Flight Control release namespace as the suffix.
- RoleBindings for the users, groups, or service accounts in `roleBindings`.
- A `flightctl-auth-reader` Role and RoleBinding that lets the main Flight Control control plane read RoleBindings in this project for organization and role mapping.

## Prerequisites

- A namespace-scoped Flight Control installation. See the main chart's [Namespace-scoped installation](../flightctl/README.md#namespace-scoped-installation).
- Project-admin permissions in the project where you install this chart.
- Authorization to create and delegate the permissions declared by the rendered Flight Control Roles. An authorized RBAC administrator must grant those `flightctl.io` permissions to the installer, or grant explicit namespaced `escalate` and `bind` privileges. Stock OpenShift project-admin permissions do not include these custom permissions.
- For OpenShift authentication, an administrator authorized to update Namespace labels must apply the project label described below. Stock project-admin permissions do not authorize this operation.
- The main Flight Control release namespace and release name.

The reader Role delegates only `get` and `list` access to RoleBindings, which project administrators already hold. A tenant project administrator can grant this access to the main Flight Control ServiceAccount without cluster-admin permissions. The custom Flight Control Roles require the separate authorization above.

## Install

```console
helm install flightctl-tenant oci://quay.io/flightctl/charts/flightctl-tenant \
  --namespace <project> \
  --set flightctlNamespace=<main_flightctl_namespace> \
  --set flightctlReleaseName=<main_flightctl_release_name> \
  --values tenant-values.yaml
```

Example `tenant-values.yaml`:

```yaml
flightctlNamespace: flightctl
flightctlReleaseName: flightctl
roleBindings:
  - role: org-admin
    subjects:
      - kind: Group
        name: platform-admins
  - role: viewer
    subjects:
      - kind: User
        name: alice
```

## Label the project

The main chart's OpenShift auth provider only considers projects with the configured project label. Have an administrator authorized to update Namespace labels apply the main Flight Control release name:

```console
oc label namespace <project> io.flightctl/instance=<main_flightctl_release_name>
```

If the main chart sets a custom `global.auth.openshift.projectLabelFilter`, use the label from that filter instead.

If users need to see the project in the OpenShift console, also grant the built-in `view` role:

```console
oc adm policy add-role-to-user view <user> -n <project>
```

## Role names

The role names must match the main Flight Control release namespace, for example `flightctl-org-admin-flightctl` for a main chart installed in the `flightctl` namespace. The control plane strips the namespace suffix when it maps RoleBindings to Flight Control roles.

Use `org-admin` for a project-scoped administrator. `flightctl-admin` is a global super-admin role and is not valid in a tenant project.
