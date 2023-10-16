# Flight Control
Flight Control is a service for declarative, GitOps-driven management of edge device fleets running [ostree-based](https://github.com/ostreedev/ostree) Linux system images.

> [!NOTE]  
> Flight Control is still in early stage development!

## Building

Prerequisites:
* `git`, `make`, and `go` (>= 1.20), and `podman-compose`

Checkout the repo and from within the repo run:

```
make build
```

## Running

Start the Flight Control database:

```
podman-compose deploy/podman/compose.yaml up
```

Start the Flight Control API server:

```
bin/flightctl-server
```

Note it stores its generated CA cert, server cert, and client-bootstrap cert in `$HOME/.flightctl/certs`.

Use the `flightctl` CLI to apply, get, or delete resources"

```
bin/flightctl apply -f examples/fleet.yaml
bin/flightctl get fleets
```
