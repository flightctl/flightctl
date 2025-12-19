# Managing Device Fleets

## Understanding Fleets

Flight Control simplifies management of a large number of devices and workloads through the concept of "fleets". A fleet is a grouping of devices that get managed as one: When you push an operating system update to the fleet, all devices in the fleet will be updated. When you deploy an application to the fleet, all devices in the fleet will have the application deployed. Instead of observing the status of a large number of individual devices, you can observe the fleet's status that summarizes the statuses of all individual devices in the fleet.

Fleet-level management has several advantages:

* It scales your operations, because you perform operations only once per fleet instead of once per device.
* It minimizes the risk of configuration mistakes and configuration drift (differences between the desired and the target configuration that accumulate over time).
* It automatically applies the right configuration when you add devices to the fleet or replace devices in the fleet in the future.

There are scenarios in which fleet-level management may not be your best option:

* If your device configurations have very little in common.
* If you need tight control over the order and timing of updates.

Advantageously, you can have a mix of individually-managed and fleet-managed devices at the same time. You can join a device to a fleet, remove it from the fleet, or join it to another fleet later if needed. At any given time, a device cannot be member of more than one fleet, though (the device is said to be "owned" by the fleet).

Fleets are resources in Flight Control just like device, enrollment request, and others. You can give a fleet a name when you create it, but you cannot change that name later. You can organize your fleets using labels, just as described in [Organizing Devices](managing-devices.md#organizing-devices).

Most importantly, though, a fleet's specification consists of three parts that are detailed in later sections:

1. A **label selector** that determines which devices are part of the fleet (see [Selecting Devices into a Fleet](managing-fleets.md#selecting-devices-into-a-fleet)).
2. A **device template** that is the template for the device specifications of all devices in the fleet (see [Defining Device Templates](managing-fleets.md#defining-device-templates)).
3. A set of **policies** that govern how devices are managed, for example how changes to the device template are rolled out to devices (see [Defining Rollout Policies](managing-fleets.md#defining-rollout-policies)).

When a user updates a fleet's device template to a new version, a component called "fleet controller" starts the process of rolling out this version to all of the fleet's devices over time. When the controller has selected a device for update, it copies the new device template to the device's specification. The next time the device's agent checks in, it learns of the new specification and begins the update process.

The same thing happens when an individually managed device joins a fleet or a device changes fleets: The device template is copied to the device's specification and if that specification differs from the previous one as a result, the agent will perform an "update".

## Selecting Devices into a Fleet

In Flight Control, devices are not explicitly assigned to a fleet. Instead, each fleet has a "selector" that defines which labels a device must have to be selected into the fleet. This allows decoupling the concerns of organizing devices from operating them.

Let us have a look at a practical example. Assume the following list of point-of-sales (PoS) terminal devices and their labels:

| Device | Labels |
| ------ | ------ |
| A | type: pos-terminal, region: east, stage: production |
| B | type: pos-terminal, region: east, stage: development |
| C | type: pos-terminal, region: west, stage: production |
| D | type: pos-terminal, region: west, stage: development |

If all PoS terminals used more or less the same configuration and were managed by the same operations team, it could make sense to define a single fleet `pos-terminals` with a label selector `type=pos-terminal`. The result is that the fleet would contain all devices A, B, C, and D.

Often, though, you would have separate organizations developing solutions and deploying and operating them. In this case, it could make sense to define two fleets `development-pos-terminals` with label selector `type=pos-terminal, stage=development` that selects devices C and D and similar for the production PoS terminals. This way, fleets can be managed independently.

Note that you have to define selectors so that no two fleets select the same device. Say you had one fleet select `region=east` and another `stage=production`, then both would select device A. When Flight Control detects this situation, it keeps the device in the fleet it is currently assigned to (if any) and signals the conflict by setting the "MultipleOwners" condition on affected devices to "true".

### Selecting Devices into a Fleet on the Web UI

### Selecting Devices into a Fleet on the CLI

Before adding a label selector to a fleet, you can test it by getting the list of devices matching that selector. To test the selector `type=pos-terminal, stage=development` in the above example, you would run:

```console
flightctl get devices -l type=pos-terminal -l stage=development
```

If the list of returned devices is OK, you could then define a `development-pos-terminals` fleet selecting these like this:

```yaml
apiVersion: flightctl.io/v1beta1
kind: Fleet
metadata:
  name: development-pos-terminals
spec:
  selector:
    matchLabels:
      type: pos-terminal
      stage: development
[...]
```

## Defining Device Templates

A fleet's device template contains a device specification that gets applied to all devices in the fleet when the template gets updated. In other words, you could take an existing device's specification and create a new fleet whose template is a copy of that specification. You can then join that device to the fleet and join additional devices to the fleet and Flight Control would enforce that they all eventually have the exact same specification.

For example, you could specify in a fleet's device template that all devices in the fleet shall run the OS image `quay.io/flightctl/rhel:9.5`. The Flight Control service would then roll out this specification to all devices in the fleet and the Flight Control agents would update the devices accordingly. The same would apply to the other specification items described in [Managing Devices](managing-devices.md).

However, it would be impractical if *all* of a fleet's devices had to have the *exact same specification*. Flight Control therefore allows templates to contain placeholders that get filled in based on a device's name or label values. The syntax for these placeholders matches that of [Go templates](https://pkg.go.dev/text/template), but you may only use simple text or actions (no conditionals or loops, for example). You may reference anything under a device's metadata such as `{{ .metadata.labels.key }}` or `{{ .metadata.name }}`.

We also provide some helper functions:

* `upper`: Change to upper case. For example, `{{ upper .metadata.name }}`.
* `lower`: Change to lower case. For example, `{{ lower .metadata.labels.key }}`.
* `replace`: Replace all occurrences of a substring with another string. For example, `{{ replace "old" "new" .metadata.labels.key }}`.
* `getOrDefault`: Return a default value if accessing a missing label. For example, `{{ getOrDefault .metadata.labels "key" "default" }}`.

You can also combine helpers in pipelines, for example `{{ getOrDefault .metadata.labels "key" "default" | upper | replace " " "-" }}`.

Note: Always make sure to use proper Go template syntax. For example, `{{ .metadata.labels.target-revision }}` is not valid because of the hyphen, and you would need to use something like `{{ index .metadata.labels "target-revision" }}` instead.

Here are some examples of what you can do with placeholders in device templates:

* You can label devices by their deployment stage (say, `stage: testing` and `stage: production`) and then use the label with the key `stage` as placeholder when referencing the OS image to use (say, `quay.io/myorg/myimage:latest-{{ .metadata.labels.stage }}`) or when referencing a folder with configuration in a Git repository.
* You can label devices by deployment site (say, `site: factory-berlin` and `site: factory-madrid`) and then use the label with the key `site` as parameter when referencing the secret with network access credentials in Kubernetes.
* You can label devices by application version (say, `app-version: 1.2.3`) and then use the label to specify the container image for an application (say, `quay.io/myorg/myapp:{{ .metadata.labels.app-version }}` using the `index` function as `quay.io/myorg/myapp:{{ index .metadata.labels "app-version" }}`).
* You can use the device name to create unique application configurations by templating the inline application content or path with `{{ .metadata.name }}`.

The following fields in device templates support placeholders (including within values, unless otherwise noted):

| Field                             | Placeholders supported in              |
|-----------------------------------|----------------------------------------|
| OS Image                          | repository name, image name, image tag |
| Git Config Provider               | targetRevision, path                   |
| HTTP Config Provider              | URL suffix, path                       |
| Inline Config Provider            | content, path                          |
| Image Application Provider        | image tag                              |
| Inline Application Provider       | content, path                          |
| Application Environment Variables | values                                 |
| Application Volumes               | image tag                              |

### Using Kubernetes Secrets

In addition to the templating mechanism, you can also reference Kubernetes secrets in your device templates. This is useful for injecting sensitive information like passwords or certificates into your devices.

Note: When a Kubernetes secret referenced in a device template changes, a new template version is not created immediately. Instead, the secret change will be included in the next template version that gets created when you update the fleet's device template.

To use a Kubernetes secret, you can use the `secretRef` field in your device template. The `secretRef` field has the following subfields:

| Field       | Description                                                  |
|-------------|--------------------------------------------------------------|
| `name`      | The name of the Kubernetes secret.                           |
| `namespace` | The namespace where the Kubernetes secret is located.        |
| `mountPath` | The absolute path on the device where the secret should be mounted. |

Here is an example of a device template that uses a `secretRef`:

```yaml
spec:
  config:
    - name: my-secret
      secretRef:
        name: my-secret-name
        namespace: my-secret-namespace
        mountPath: /etc/my-secret
```

#### RBAC Permissions for Single-Namespace Access

For the Flight Control service to be able to access the Kubernetes secrets, the service account used by the Flight Control service needs the following RBAC permissions in the namespace where the secrets are stored. The following example shows how to grant read-only access to secrets within a single namespace.

Create a `Role` to define the permissions:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: flightctl-secret-reader
  namespace: <secret-namespace>
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
```

Then, create a `RoleBinding` to grant these permissions to the Flight Control service account:

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: flightctl-secret-reader-binding
  namespace: <secret-namespace>
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: flightctl-secret-reader
subjects:
- kind: ServiceAccount
  name: <flightctl-service-account-name>
  namespace: <flightctl-namespace>
```

Replace the following placeholders with the appropriate values for your environment:

* `<secret-namespace>`: The namespace where your secrets are stored.
* `<flightctl-service-account-name>`: The name of the Flight Control service account (typically found in the Flight Control deployment or Helm values).
* `<flightctl-namespace>`: The namespace where Flight Control is deployed.

#### RBAC Permissions for Cross-Namespace Access

A `ClusterRole` can grant access to cluster-scoped resources or to namespaced resources across all namespaces.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: flightctl-secret-reader
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get"]
```

A `ClusterRoleBinding` grants the permissions defined in a `ClusterRole` to a user or set of users.

```yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: flightctl-secret-reader-binding
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: flightctl-secret-reader
subjects:
- kind: ServiceAccount
  name: <flightctl-service-account-name>
  namespace: <flightctl-namespace>
```

Note: Both the Role/ClusterRole and its corresponding RoleBinding/ClusterRoleBinding are required for authentication and authorization. The Role or ClusterRole defines the permissions, while the Binding grants those permissions to the service account. When deciding whether to centralize RBAC management, consider using `Role` and `RoleBinding` for namespace-scoped access (more secure, better isolation) and `ClusterRole` with `ClusterRoleBinding` for cluster-wide access or when accessing resources across multiple namespaces (more efficient for centralized management).

**Best Practices for Cross-Namespace Access:**

* **Minimize Permissions**: The `ClusterRole` example above grants permission to read secrets in *all* namespaces. If you only need access to a few specific namespaces, it is more secure to create a `Role` and `RoleBinding` in each of those namespaces.
* **Service Account Namespace**: The service account must be specified with the namespace where it is deployed (`<flightctl-namespace>`). This service account can then be granted permissions in other namespaces via `RoleBinding` or across the cluster via `ClusterRoleBinding`.

## Defining Rollout Policies

You can define policies that govern how a change to a fleet's device template gets rolled out across devices of the fleet. This gives you control over

* groups of devices to update together (e.g. "one deployment site at a time"),
* the order in which groups are updated (e.g. "first sites in country A, then in country B"),
* the number or ratio of devices updating at a given time (e.g. "first 1%, then 10%, then the rest"), and
* the service availability during the rollout (e.g. "update no more than two devices per site at a time").

Rollout policies in Flight Control build on label selection of devices (see [Organizing Devices](managing-devices.md#organizing-devices)) and are thus adaptable to a wide range of use cases.

### Defining a Device Selection Strategy

Currently, Flight Control only supports the `BatchSequence` strategy for device selection. This strategy defines a stepwise rollout process where devices are grouped into batches based on specific criteria.

Batches are updated sequentially. After each batch completes, the rollout proceeds to the next batch, but only if the success ratio of the previous batch meets or exceeds the specified *success threshold*:

```text
# of successful updates in the batch
------------------------------------  * 100% >= success threshold [%]
     # of devices in the batch
```

In a batch sequence, the final batch is an implicit batch. It is not specified in the batch sequence. It selects all devices in a fleet that have not been selected by the explicit batches in the sequence.

To roll out updates in a sequence of batches, add a rollout policy to your fleet specification that defines a device selection strategy. Select the strategy `BatchSequence` and add a list of batch definitions. A device selection strategy uses the following parameters:

| Parameter | Description |
| --------- | ----------- |
| Strategy | The device selection strategy. Must be `BatchSequence`. |
| Sequence | A list of explicit batch definitions that will be processed in sequence. |

A batch definition takes the following parameters, of which at least one must be defined:

| Parameter | Description |
| --------- | ----------- |
| Selector | (Optional) A label selector that selects devices to be included into the batch. Label selection works analogous to [Selecting Devices into a Fleet](managing-fleets.md#selecting-devices-into-a-fleet), but limited to the device population of all devices in the fleet. |
| Limit | (Optional) Defines how many devices should be included in a batch at most. The limit can be specified either as an absolute number of devices or as percentage of the device population. If a selector is specified as well, that device population is the devices in the fleet that match the selector, otherwise it is all devices in the fleet. |

#### Defining a Device Selection Strategy on the CLI

To define a device selection strategy for the rollout, add a `rolloutPolicy` section to the fleet's specification that defines a `deviceSelection` strategy and a `successThreshold`.

The following example defines a rollout policy with 5 batches (4 explicit and 1 implicit), so that updates are rolled out across the fleet as follows:

1. Update 1 device from the set of devices labeled `stage: canary`.
2. Update 10% of devices from the set of devices labeled `region: emea`.
3. Update all remaining devices from the set of devices labeled `region: emea`.
4. Update 10% of all devices in the fleet (might be none, if the previous batch already updated 10% of the total population of the fleet).
5. (Implicit) Update all remaining devices in the fleet (might be none).

```yaml
apiVersion: v1beta1
kind: Fleet
metadata:
  name: default
spec:
  selector:
    [...]
  template:
    [...]
  rolloutPolicy:
    deviceSelection:
      strategy: 'BatchSequence'
      sequence:
        - selector:
            matchLabels:
              stage: canary
          limit: 1
        - selector:
            matchLabels:
              region: emea
          limit: 1%
        - selector:
            matchLabels:
              region: emea
        - limit: 10%
    successThreshold: 95%
```

### Defining a Disruption Budget

You can define a disruption budget to limit the number of devices that may be updated in parallel, ensuring a minimal level of service availability.

A disruption budget takes the following parameters:

| Parameter | Description |
| --------- | ----------- |
| GroupBy | Defines how devices are grouped when applying the disruption budget. The grouping is done by label keys. |
| MinAvailable | (Optional) Specifies the minimum number of devices per group that must remain available during a rollout. |
| MaxUnavailable | (Optional) Limits the number of devices per group that can be unavailable at the same time. |

#### Defining a Disruption Budget on the CLI

To define a disruption budget for the rollout, add a `rolloutPolicy` section to the fleet's specification that defines a `disruptionBudget` section.

The following example assumes a fleet of smart displays in retail stores that are labeled with the store they are located in (`store: some-store-location`). To ensure a minimum of 2 displays in each store remain available during a rollout, define a disruption budget that groups displays store (`groupBy: ["store"]`), each group having `minAvailable: 2`:

```yaml
apiVersion: v1beta1
kind: Fleet
metadata:
  name: smart-display-fleet
spec:
  selector:
    [...]
  template:
    [...]
  rolloutPolicy:
    disruptionBudget:
      groupBy: ["store"]
      minAvailable: 2
```
