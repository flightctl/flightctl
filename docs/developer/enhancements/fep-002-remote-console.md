# EP: Remote console access to devices
<!-- this format is inspired by the K8S KEP format https://raw.githubusercontent.com/kubernetes/enhancements/master/keps/NNNN-kep-template/README.md -->
## Summary

This enhancement proposal defines a new API endpoint to provide remote console access to devices.

## Motivation

Under some circumstances administrators may need access to the devices for debugging purposes.
Some complex issues may require direct access to the device console to troubleshoot the problem,
then once the issue is identified and resolved, the remediation can be applied at the fleet level.

### Goals

* Define the API for remote console access to devices.
* Provide a way to access the device console from the API.
* Provide a way to access the device console from the CLI.
* Provide a way to access the device console from the UI.
* Make this feature opt-in and configurable in the agent.
* Ensure that audit logs are generated for all console access.

### Non-Goals

## Proposal

The commit where this RFE is posted provides an interim implementation of the feature, which
while functional, is not complete.

### TO-DOs:
* Check if protoc compilation can be performed with //go:generate like the rest of the APIs.
* Use buf for protobuf compilation as it has more powerful lintig capabilities and can
  detect changes breaking older clients.

* Make sure that a new console session, while one is happening is either: blocked, shared, or
  kills the previous one (--force flag/query parameter?)

* In the interim solution we offer an endpoint to request a console, that provides a grpc endpoint
  and a session ID. The GRPC endpoint is on the agent api-side under a different authentication
  realm (mTLS). In the final solution we should have a ws method `/ws/devices/{name}/console` which
  directly drop us into a console, or denies access based on permissions.

* An administrator can forcedfully close existing console connections.

* The proof-of-concept relies on the fact that both, client and server connect to the same gRPC endpoint
  and process, making the data stream possible. In the final implementation the agent service must
  be a separate pod, and the frontend API and the agent service must communicate through rabbitmq
  (i.e. using ephemeral queues/exchanges for session communication, based on session ID)

* Sessions should be tracked with timeouts. I.e. if a session is requested but one end does not
  connect, it should timeout and be cleaned up from server.


### General



### User Stories

#### Story 1


### Notes/Constraints/Caveats

### Risks and Mitigations

## Design Details


### Scalability

### Troubleshooting


## Implementation History

## Drawbacks

## Alternatives

