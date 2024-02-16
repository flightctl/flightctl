# Flight Control
Flight Control is a service for declarative, GitOps-driven management of edge device fleets running [ostree-based](https://github.com/ostreedev/ostree) Linux system images.

> [!NOTE]
> Flight Control is still in early stage development!

## Building

Prerequisites:
* `git`, `make`, and `go` (>= 1.20), and `podman-compose`

Flightctl agent reports the status of running rootless containers. Ensure the podman socket is enabled:

`systemctl --user enable --now podman.socket`

Checkout the repo and from within the repo run:

```
make build
```

To run unit tests, use `make unit-test`.  This requires installing gotestsum:

`go install gotest.tools/gotestsum@latest`

To generate API code and mocks, use `make generate`  This requires installing mockgen:

`go install github.com/golang/mock/mockgen@v1.6.0`

## Running

Note: If you are developing with podman on an arm64 system (i.e. M1/M2 Mac) change the postgresql
image with:
```
export PGSQL_IMAGE=registry.redhat.io/rhel8/postgresql-12
podman login registry.redhat.io
```

The service can be deployed locally in kind with the following command:
```
make deploy
```

Note it stores its generated CA cert, server cert, and client-bootstrap cert in `$HOME/.flightctl/certs`
and the client configuration in `$HOME/.flightctl/config.yaml`.

Use the `flightctl` CLI to apply, get, or delete resources:

```
bin/flightctl apply -f examples/fleet.yaml
bin/flightctl get fleets
```

Use the `devicesimulator` to simulate load from devices:

```
bin/devicesimulator --count=100
```

## Running the server locally
For development purposes it can be useful to run the database in a container in kind, and
the server locally. To do this, first start the database:

```
make deploy-db
```

Then start the server:

```
rm $HOME/.flightctl/client.yaml
bin/flightctl-server
```

## Metrics

Start the observability stack:

```
podman-compose -f deploy/podman/observability.yaml up
```

The Grafana and Prometheus web UIs are then accessible on `http://localhost:3000` and `http://localhost:9090`, respectively.
