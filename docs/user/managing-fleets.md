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

Note that you have to define selectors so that no two fleets select the same device. Say you had one fleet select `region=east` and another `stage=production`, then both would select device A. When Flight Control detects this situation, it keeps the device in the fleet it is currently assigned to (if any) and signals the conflict by setting the "OverlappingSelectors" condition on affected fleets to "true".

### Selecting Devices into a Fleet on the Web UI

### Selecting Devices into a Fleet on the CLI

Before adding a label selector to a fleet, you can test it by getting the list of devices matching that selector. To test the selector `type=pos-terminal, stage=development` in the above example, you would run:

```console
flightctl get devices -l type=pos-terminal -l stage=development
```

If the list of returned devices is OK, you could then define a `development-pos-terminals` fleet selecting these like this:

```yaml
apiVersion: flightctl.io/v1alpha1
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

After applying the change, you can check whether there's an overlap with another fleet's selector like this (assuming you have installed the `jq` command):

```console
flightctl get fleets/development-pos-terminals -o json | jq -r '.status.conditions[] | select(.type=="OverlappingSelectors").status'
```

You should see the output:

```console
False
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

The following fields in device templates support placeholders (including within values, unless otherwise noted):

| Field | Placeholders supported in |
| ----- | ------------------------- |
| OS Image | repository name, image name, image tag |
| Git Config Provider | targetRevision, path |
| HTTP Config Provider | URL suffix, path |
| Inline Config Provider | content, path |
