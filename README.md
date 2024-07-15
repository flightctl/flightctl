# Flight Control
Flight Control is a service for declarative, GitOps-driven management of edge device fleets running [ostree-based](https://github.com/ostreedev/ostree) Linux system images.

> [!NOTE]
> Flight Control is still in early stage development!

## Building

Prerequisites:
* `git`, `make`, and `go` (>= 1.20), `openssl-devel`, and `podman-compose`

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
and the client configuration in `$HOME/.flightctl/client.yaml`.

Use the `flightctl` CLI to login and then apply, get, or delete resources:

```
bin/flightctl login $(cat ~/.flightctl/client.yaml | grep server | awk '{print $2}')
bin/flightctl apply -f examples/fleet.yaml
bin/flightctl get fleets
```

Use an agent VM to test a device interaction, an image is automatically created from
hack/Containerfile.local and a qcow2 image is derived in output/qcow2/disk.qcow2, currently
this only works on a Linux host.

```
# will create the cluster, and the agent config files in bin/agent which will be embedded in the image
make deploy
make agent-vm agent-vm-console # user/password is redhat/redhat
```

The agent-vm target accepts multiple parameters:
- VMNAME: the name of the VM to create (default: flightctl-device-default)
- VMCPUS: the number of CPUs to allocate to the VM (default: 1)
- VMMEM: the amount of memory to allocate to the VM (default: 512)
- VMWAIT: the amount of minutes to wait on the console during first boot (default: 0)

It is possible to create multiple VMs with different names:

```
make agent-vm VMNAME=flightctl-device-1
make agent-vm VMNAME=flightctl-device-2
make agent-vm VMNAME=flightctl-device-3
```

Those should appear on the root virsh list:
```
$ sudo virsh list
 Id   Name                        State
-------------------------------------------
 13   flightctl-device-1          running
 14   flightctl-device-2          running
 15   flightctl-device-3          running
````

And you can log in the consoles with agent-vm-console:
```
make agent-vm-console VMNAME=flightctl-device-1
```

NOTE: You can exit the console with Ctrl + ] , and `stty rows 80` and `stty columns 140` (for example) are useful to resize your console otherwise very small.


If you created individual devices you need to clean them one by one:
```
make agent-vm-clean VMNAME=flightctl-device-1
make agent-vm-clean VMNAME=flightctl-device-2
make agent-vm-clean VMNAME=flightctl-device-3
```

Use the `devicesimulator` to simulate load from devices:

```
bin/devicesimulator --count=100
```

## Metrics

Start the observability stack:

```
podman-compose -f deploy/podman/observability.yaml up
```

The Grafana and Prometheus web UIs are then accessible on `http://localhost:3000` and `http://localhost:9090`, respectively.
