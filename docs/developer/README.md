# Flight Control
Flight Control is a service for declarative, GitOps-driven management of edge device fleets running [ostree-based](https://github.com/ostreedev/ostree) Linux system images.

> [!NOTE]
> Flight Control is still in early stage development!

## Building

Prerequisites:
* `git`, `make`, and `go` (>= 1.23), `openssl`, `openssl-devel`, `podman-compose` and `go-rpm-macros` (in case one needs to build RPM's)

Flightctl agent reports the status of running rootless containers. Ensure the podman socket is enabled:

`systemctl --user enable --now podman.socket`

Checkout the repo and from within the repo run:

```
make build
```

To run unit tests, use `make unit-test`.  This requires installing gotestsum:

`go install gotest.tools/gotestsum@latest`

To generate API code and mocks, use `make generate`  This requires installing mockgen:

`go install go.uber.org/mock/mockgen@v0.4.0`

## Running

Note: If you are developing with podman on an arm64 system (i.e. M1/M2 Mac) change the postgresql
image with:
```
export PGSQL_IMAGE=registry.redhat.io/rhel9/postgresql-16
podman login registry.redhat.io
```

The service can be deployed locally in kind with the following command:
```
make deploy
```

To deploy with auth enabled:
```
AUTH=true make deploy
```

Note it stores its generated CA cert, server cert, and client-bootstrap cert in `$HOME/.flightctl/certs`
and the client configuration in `$HOME/.flightctl/client.yaml`.

Use the `flightctl` CLI to login and then apply, get, or delete resources:

```
bin/flightctl login $(cat ~/.flightctl/client.yaml | grep server | awk '{print $2}') --web --certificate-authority ~/.flightctl/certs/ca.crt
bin/flightctl apply -f examples/fleet.yaml
bin/flightctl get fleets
```

Note: If deployed without auth enabled, then there is no need to login.

Use an agent VM to test a device interaction, an image is automatically created from
hack/Containerfile.local and a qcow2 image is derived in output/qcow2/disk.qcow2, currently
this only works on a Linux host.

Note: An update to firewalld may need to be made if the agent is unable to connect to the api instance:

```bash
sudo firewall-cmd --zone=libvirt --add-rich-rule='rule family="ipv4" source address="<virbr0s subnet here>" accept' --permanent
sudo firewall-cmd --reload
```

You can deploy a DB container of different sizes using a DB_VERSION variable for make command:
* e2e (default) - minimal footprint for e2e testing
* small-1k - recommended setting for a demo environment 1000 devices max
* medium-10k - recommended setting for a demo environment 10k devices max

```
# will create the cluster, and the agent config files in bin/agent which will be embedded in the image
# this one will create a defailt `e2e DB container
make deploy
# to create a small DB container use
# make deploy DB_VERSION=small
make agent-vm agent-vm-console # user/password is user/user
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
make clean-agent-vm VMNAME=flightctl-device-1
make clean-agent-vm VMNAME=flightctl-device-2
make clean-agent-vm VMNAME=flightctl-device-3
```

To quickly create agent instances for testing/development in a containerized environment. This is particularly useful for testing lightweight agent deployments without setting up VMs.

```
make agent-container
make clean-agent-container
```

Use the **[devicesimulator](devicesimulator.md)** to simulate load from devices

```
bin/devicesimulator --count=100
```

## Metrics

Start the observability stack:

```
make deploy-e2e-extras
```

The Prometheus web UI is then accessible on `http://localhost:9090`

For detailed information about the metrics system architecture, see [Metrics Architecture](architecture/metrics.md).
