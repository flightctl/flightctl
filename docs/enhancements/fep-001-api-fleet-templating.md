# EP: Definition of the Fleet and Device API spec
<!-- this format is inspired by the K8S KEP format https://raw.githubusercontent.com/kubernetes/enhancements/master/keps/NNNN-kep-template/README.md -->
## Summary

The Fleet API is designed to manage big groups of devices that share configuration
and operating system image.

Some use cases point to the necessity of customizing some of those details based
on the device's metadata.

## Motivation

Allowing templating of the device spec in the fleet API we could achieve a more
flexible management, allowing customers to manage devices in a more granular way.

### Goals

* Define templating mechanism for the device spec template configuration in fleets,
  based on labels and other metadata.

* Enable different os images based on the device labels.
```yaml
  template:
    spec:
      os:
        image: "access-control-{{ index .device.metadata.labels `region` }}:1.0.0"
```

* Enable different references for a config source based on labels:
```yaml
- name: microshift-manifests
  configType: GitConfigProviderSpec
  gitRef:
    repository: basic-nginx-demo
    targetRevision: "1.0.0-{{ index .device.metadata.labels `region` }}"
    path: /basic-nginx-demo/configuration
```

* Enable different folders within a config source based on labels:

```yaml
- name: microshift-manifests
  configType: GitConfigProviderSpec
  gitRef:
    repository: basic-nginx-demo
    targetRevision: 1.0.0
    path: "/basic-nginx-demo/configuration/{{ index .device.metadata.labels `region` }}"
```

* Enable different folders within a config source based on labels:

```yaml
- name: microshift-manifests
  configType: GitConfigProviderSpec
  gitRef:
    repository: basic-nginx-demo
    targetRevision: 1.0.0
    path: "/basic-nginx-demo/configuration/{{ index .device.metadata.labels `region` }}/{{  index .device.metadata.labels `site` }}"  
```

* Enable templating of inline configuration based on device details:
```yaml
 - name: motd-update
   configType: InlineConfigProviderSpec
   inline:
     ignition:
       version: 3.4.0
     storage:
       files:
         - contents:
             source: >-
               data:,Device%20{{ .device.metadata.name }}%0AThis%20system%20is%20managed%20by%20flightctl.%0A
           mode: 422
           overwrite: true
           path: "/etc/motd"
```
### Non-Goals

## Proposal

### Device API

The device API does not change, in the device API we don't allow templating. Any
device specifications from a fleet should already be rendered into the device

Variable/template expansion is not supported at the device API level, as:
  * We are operating on a single device, variables aren't really beneficial
  * Updates to labels will not affect configuration unless the device is part of a fleet.

### Fleet API

The template.spec field of a fleet allows the use of go-template logic to render the device spec.
```yaml
kind: Fleet
metadata:
  name: autonomous-forklifts
spec:
  template:
    spec:
      os:
        image: forklifts-{{ .device.metadata.label[factory] }}:latest
      config:
        - name: factory-wifi-access-credentials
          configType: KubernetesSecretProviderSpec
          k8sSecretRef:
            cluster: …
            namespace: factory-{{ .device.metadata.label[factory] }}
            name: wifi-access-credentials
            mountPath: /etc/network/…
        - name: motd-update
          configType: InlineConfigProviderSpec
          inline:
            ignition:
              version: 3.4.0
            storage:
              files:
                - contents:
                    source: >-
                        data:,{{ .device.name }}%0A
                  mode: 422
                  overwrite: true
                  path: "/etc/acm-cluster-id"
```

The exposed values for use in templates is intentionally limited:

* device.metadata.labels
* device.metadata.name

Making sure that the render of the device template is deterministic (i.e. not including timestamps or dates, etc..).

#### Expected behavior

When `fleet.spec.template` is updated, the fleet controller
freezes the template, including non-expanded variables in the TemplateVersion. All existing devices
are scanned to determine possible git/image/secret sources references by the `TemplateVersion` in the
fleet. Those source references are captured in/alongside the `TemplateVersion` object. For non-versioned
configuration sources (Vault secrets, K8s config maps, etc.), a snapshot of the configuration will be
created and referenced under a (sortable) pseudo-version (e.g. `secret-$namespace-$name-$datetime-$hash`)

Once the fleet controller has assigned the `fleet-controller/templateVersion` annotation to the fleet
the rollout phase starts.

When the fleet controller applies a `TemplateVersion` to a specific device through the Device API:
* The template variables get rendered (i.e. the device's labels at that point in time get used)

* An audit log entry is created for the device, capturing the rendered template and variables used
  for expansion.

* When later in time a device has labels updated (which could potentially result in template
  evaluation changes) the device controller will queue the device to be re-evaluated and the
  `TemplateVersion` re-rendered.

#### Expected behavior on issues

##### Device cannot be rendered from Template due to missing label
When a device cannot be rendered for some reason (e.g. missing label), the fleet-controller
will mark the device with a label `fleet-controller/failed-to-reconcile=true`, and add an annotation
`fleet-controller/failed-to-reconcile-reason` with a human-readable reason. The fleet will include
a status condition `DeviceFailedToReconcile`.

Once all devices are corrected and can be reconciled again this status condition will be removed.

#### Device cannot generate /device/{name}/rendered version due to missing resource
When a device cannot render the template due to missing resources (e.g. git repo not found, secret not found),
the device-controller will set a status condition on the device `MissingResource` with a human-readable reason.

### User Stories 

#### Story 1: network access credentials
As an IT administrator, I want to use site-specific network access credentials for devices in the same fleet, so that I do not have to deal with the complexity of maintaining one fleet per site.

#### Story 2: site specific endpoints
As an IT administrator, I want to provide site-specific server endpoints to devices that depend on LAN-configured edge servers.

#### Story 3: site specific certificate bundles
As an IT administrator, I want to provide site-specific certificate bundles to enable trust with the local site infrastructure.

#### Story 4: injecting the device name into the configuration
As an IT administrator, I want the name of my MicroShift instance in ACM to have the same cluster name as the underlying device's name or fingerprint, so I can easily correlate between the two.

#### Story 5: site specific telemetry endpoints
As an IT administrator, I want to provide site-specific/region-specific telemetry endpoints to devices.

#### Story 6: dialing up verbosity for logging in my user application in specific devices
As an IT administrator, I want to dial up the verbosity for logging on a specific device, so I can troubleshoot it.

#### Story 7: region specific application service endpoints
As an IT administrator, I want to provide region-specific service endpoints to my applications

#### Story 8: region specific radio bands/regulations
As a developer, I want to ensure that my devices automatically comply with local regulations when enrolled in specific regions. (i.e. configured with region-specific radio bands for WiFi, 5G, or other radio technologies)

#### Story 9: region specific data sovereignty
As a developer, I want to ensure that my devices connect to the specific regional endpoints to ensure compliance with data sovereignty.

### Notes/Constraints/Caveats

Traceability of device spec changes over time becomes increasingly important with templating,
as devices will render the TemplateVersions in different ways over time, we propose that
every device render emits an audit log entry, so we will eventually need to implement an
audit log where we can send/record how devices are being rendered over time.

Would it suffice to have the ability to send those specific traces to a logging system?
Keeping those in the database would probably be dangerous over time, specially at the scales
we intend this system to work. We should not use the DB to store an audit log.

### Risks and Mitigations

When rolling out a configuration to a fleet of devices, during the rollout,
some devices could turn out to be not renderable, for example:
* due to a missing label
* because the template rendering is referencing non existing resources

This is partially mitigated by the initial device scan performed by the fleet controller
when a fleet.spec.template changes. Still, labels could change while a fleet is being rolled out.

We probably need to differentiate between "fleet controller did not find the label, so
failed to do the substitution" and "the referenced resource (after substitution) does
not exist / isn't accessible". There is little the service can do about the latter:
verifying service-side is costly and also futile, because the resource could be there when
the service tried, but no longer there when the agent needs it. At the end of the day,
the agent will try during staging, fail, and set its condition to "failed to reconcile".
The former case would be similar (failure to reconcile). Then, if errors exceed threshold,
stop the rollout.

How do we signal this is happening to a device? Status on the device is only set by the
agent.

### Troubleshooting

Troubleshooting can be done by looking at the device status and rendered device spec. Any errors found in templates
could be reported as part of the fleet status conditions, any issues when applying templates
to devices will be signaled by device labels and annotations (an external controller cannot set status on a device)

## Implementation History

## Drawbacks

## Alternatives

### Every configuration combination has it's own fleet
This is the most obvious alternative, this proposal is not implemented, and administrators
take care of replicating fleet definitions as necessary to accomodate the different
configurations.

This approach does not allow customization based on fields like the device name.

### Fleet template inheritance / sub-fleets
Fleets can inherit base configuration from other fleets and modify only the necessary changes
we then use labels to attach devices to specific sub-fleets.

This approach is less flexible and does not allow customization based on fields like the device name.

### Template conditionals

Instead of using variables, allow optional config items in the fleet template. 
Something like "if label key equals value, then include this secret".  Then we
can "freeze" the entire template because we know all of the options ahead of time.

```yaml
kind: Fleet
metadata:
  name: autonomous-forklifts
spec:
  template:
    spec:
      os:
        switchFromLabel:
          - label: factory
            value: a
            image: forklifts-factory-a:latest

          - label: factory
            value: b
            image: forklifts-factory-b:latest

      config:
        - name: factory-wifi-access-credentials
          ifLabelMatches:
            - label: factory
              value: a
          configType: KubernetesSecretProviderSpec
          k8sSecretRef:
            cluster: …
            namespace: factory-a
            name: wifi-access-credentials
            mountPath: /etc/network/…
        - name: factory-wifi-access-credentials
          ifLabelMatches:
            - label: factory
              value: b
          configType: KubernetesSecretProviderSpec
          k8sSecretRef:
            cluster: …
            namespace: factory-b
            name: wifi-access-credentials
            mountPath: /etc/network/…

```

The drawback of this alternative is the need to update the fleet definitio for every new site,
i.e. 1000 sites would require 1000 entries in the yaml.