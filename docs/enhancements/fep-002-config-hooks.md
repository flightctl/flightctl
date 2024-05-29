# EP: Definition of the Fleet and Device API spec
<!-- this format is inspired by the K8S KEP format https://raw.githubusercontent.com/kubernetes/enhancements/master/keps/NNNN-kep-template/README.md -->
## Summary

This RFE defines a mechanism for per-device configuration extensiblity,
where the device configuration can be instructed to talk to an external
system or hook to fetch specific configurations for the device. This
RFE also defines a talk-back mechanism for those hooks to tell flightctl
that such configuration needs to be updated/revoked/etc.

## Motivation

Integration with external systems when devices may need specific
per-device tokens, per-device configurations sometimes transient
is a not-well covered use case in our API. Enabling such thing
with today's API would not be possible. This is why we propose
this configuration hooks API.

This could be useful i.e. to integrate with ACM, where a service could
be running on the ACM cluster, and the fleets configured to have
the devices onboarded to ACM would point to that service in the fleet
configuration items to fetch the configuration at specific
points in time (hook points), anyway this is just an example, the
mechanism should be generic to enable customers to leverage this.

### Goals

* Define hook points in the device configuration or flightctl
  to enable the described functionality.

* Allow an external system to be queried to retrieve per-device specific
  configuration.

* Allow the external system to call-back to flightctl to inform about
  changes to be made in the previously supplied configuration.

### Non-Goals

* Define a specific product integration, this is a generic mechanism
  that could be used with any external system.

## Proposal

We define a specific type of configuration that can be used to query an external system.

```yaml
apiVersion: v1alpha1
kind: Fleet
metadata:
  creationTimestamp: "2024-04-30T14:06:17Z"
  generation: 1
  labels: {}
  name: optical-inspector-production-fleet
  owner: ResourceSync/basic-nginx-r3sourcesync
spec:
  selector:
    matchLabels:
      device: optical-inspector
  template:
    metadata:
      generation: 1
    spec:
      config:
...
...
      - configType: ExternalConfigProviderSpec
        externalEndpoint:
          config-name: my-config
          url: https://acm-cluster-flightctl-hook.example.com/flightctl-hooks/
          httpCredentials: my-credentials
...
...

      os:
        image: quay.io/flightctl/flightctl-agent-basic-nginx:latest
      monitoring:
        systemd:
          matchPatterns:
          - microshift.service
          - crio.service
          - flightctl-agent.service
status:
  conditions:
  - lastTransitionTime: "2024-05-09T14:20:21Z"
    status: "False"
    type: OverlappingSelectors
```

### High level format of the hook calls
#### FlightCTL to external system

##### CALL: POST https://acm-cluster-flightctl-hook.example.com/flightctl-hooks/
[{"event: "ON_DEVICE_SPEC_UPDATE",
 "event_uuid": "uuid-for-tracking",
 "config_-_name": "my-config",
 "device_metadata": { -json representation of the device metadata on our API, including the owner/fleet -},
 "callback_endpoint": "https://api.flightctl.example.com/external-provider-callback/",
 "callback_token": "my-token"}
 "last_config_hash": "hash-of-the-last-applied-config"
]

###### RESPONSE: 200 OK
 [{"event": "ON_DEVICE_SPEC_UPDATE",
  "event_uuid": "uuid-for-tracking",
  "device_name": "device-name",
  "config-name": "my-config",
  "configuration": { representation of the configuration to be injected on the device i.e. in ignition format },
 }]

###### RESPONSE: 304 Not Modified
 [{"event": "ON_DEVICE_SPEC_UPDATE",
  "event_uuid": "uuid-for-tracking",
  "device_name": "device-name",
  "config-name": "my-config",
 }]

 
#### External system to FlightCTL


##### CALLS POST to https://api.flightctl.example.com/external-provider-callback/

[{"event: "ON_CONFIG_UPDATE",
 "event_uuid": "uuid-for-tracking",
 "config_name": "my-config",
 "device_names": [`device-name1`, `device-name2`],
]


### The hook events

#### FlightCTL to external system
* `ON_FLEET_JOIN`: This event is triggered when a device is added to a fleet for
  the first time. The external system can use this event to provide the initial
  configuration or to start building any specific configuration that needs to be
  created.

* `ON_DEVICE_SPEC_UPDATE`: This event is triggered when a device.spec is updated. At this
  point, if the device spec contains an ExternalConfigProviderSpec, the external system
  can be called to get the new configuration.

* `ON_DEVICE_METADATA_UPDATE`: This event is triggered when a device.metadata is updated. At this
  point, if the device spec contains an ExternalConfigProviderSpec, the external system
  can be called to let it know about metadata changes: labels, etc.

* `ON_DEVICE_POLL`: This event is triggered by flightctl device-controller periodically
  to check if the device configuration injected from this external system is still valid
  the periodicty is decided by the external system on previous hook calls.

* `ON_ENROLLMENT_REQUEST`: This is an event that is triggered when a device waiting
  for enrollment request is targeted to this fleet because of the labels being
  proposed on the device. This is an early notification that can be used to start
  constructing credentials or triggering any other action.

#### External system to FlightCTL
When calling the external system flightctl provides a callback endpoint, as well as the
necessary authorization (like a token) to call back to flightctl. The external system
can notify flightctl about the following events:

* `ON_CONFIG_UPDATE`: This event is triggered when the external system wants an specific
 device configuration to be updated: i.e. new credentials for the network, a token
 being removed because the application has finished onboarding, etc. This event
 can point to a single device, or to a list of devices.




### User Stories

#### Story 1
As a Fleet administrator, I want my Microshift based devices on a specific fleet
to automatically inject klusterlet details, connection tokens or any credentials
automatically, just by pointing my fleet to a service running on the ACM cluster.

#### Story 2
As a IT user, I want to have a central webhook that distributes network credentials
to my devices, and has the logic to know which credentials belong to which device
based on device attributes (location, serial number, region..). While this could
be handled with templating, we need additional flexibility or templating cannot

capture our needs well enough.

### Risks and Mitigations

Fleets consuming this introduce an external dependency that needs to be called for every
device. This could be mitigated by having a local cache of the configuration on the device
and only calling the external system when the cache is stale.

## Design Details



### Scalability


### Troubleshooting


## Implementation History

## Drawbacks


## Alternatives
