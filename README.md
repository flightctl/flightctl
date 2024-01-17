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

## Running

Note: If you are developing with podman on an arm64 system (i.e. M1/M2 Mac) change the postgresql
image with:
```
export PGSQL_IMAGE=registry.redhat.io/rhel8/postgresql-12
podman login registry.redhat.io
```


Start the Flight Control database:

```
podman-compose -f deploy/podman/compose.yaml up
```

Start the Flight Control API server:

```
bin/flightctl-server
```

Note it stores its generated CA cert, server cert, and client-bootstrap cert in `$HOME/.flightctl/certs`.

Use the `flightctl` CLI to apply, get, or delete resources:

```
bin/flightctl apply -f examples/fleet.yaml
bin/flightctl get fleets
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


## Agent Image Creation

This section aims to provide a comprehensive guide to build a bootable container image for the agent. This means using a bootable Fedora container image as base, we will create our own image that will allow users to enroll devices (VMs or physical appliances) into their own FlightCtl service.

Let's take a look at the provided Containerfile located in packaging/Containerfile.fedora:

```
FROM quay.io/centos-bootc/fedora-bootc:eln

COPY rpmbuild/RPMS/x86_64/flightctl-agent-0.0.1-1.el9.x86_64.rpm /tmp/

COPY packaging/flightctl-custom-assets/flightctl_rsa.pub /usr/etc-system/root.keys
RUN touch /etc/ssh/sshd_config.d/30-auth-system.conf; \
    mkdir -p /usr/etc-system/; \
    echo 'AuthorizedKeysFile /usr/etc-system/%u.keys' >> /etc/ssh/sshd_config.d/30-auth-system.conf; \
    chmod 0600 /usr/etc-system/root.keys
VOLUME /var/roothome

ADD packaging/flightctl-custom-assets/config.yaml /etc/flightctl/
ADD packaging/flightctl-custom-assets/ca.crt /etc/flightctl
ADD packaging/flightctl-custom-assets/client-enrollment.* /etc/flightctl/

RUN rpm-ostree install -y /tmp/flightctl-agent-0.0.1-1.el9.x86_64.rpm
RUN ln -s /usr/lib/systemd/system/podman.socket /usr/lib/systemd/system/multi-user.target.wants/
RUN ln -s /usr/lib/systemd/system/flightctl-agent.service /usr/lib/systemd/system/multi-user.target.wants/
RUN ostree container commit 

```

In order to make this work for your own service, first you need to have deployed the service (by following the section above where the podman-compose manifests is applied).

Install the rpm build tools with `sudo dnf install -y rpmdevtool` and execute `make rpm` to build the agent RPM package. It will be picked up and injected into the image.
Now, add the following assets to the directory `packaging/flightctl-custom-assets/`:

- The public SSH key you want to inject as `packaging/flightctl-custom-assets/flightctl_rsa.pub`
- The CA certificate:  `packaging/flightctl-custom-assets/ca.crt`
- The client enrollment keypair: 
  - `packaging/flightctl-custom-assets/client-enrollment.crt`
  - `packaging/flightctl-custom-assets/client-enrollment.key`
- The configuration file that points to your service: `packaging/flightctl-custom-assets/config.yaml`

It is important to highlight, that in a production environment, SSH will be disabled by default for security reasons. 
The certificates will be stored probably in `$HOME/.flightctl/certs/`.

The config.yaml should look like this:

```
agent:
  server: https://localhost
  enrollementUi: https://localhost
  statusUpdateInterval: 1m0s
  fetchSpecInterval: 1m0s
```

Replace localhost by the IP/host and port of the machine where the service is exposed. Take into account that agents must be able to connect to the service.

Now it's time to build the image! If you want to experiment with it, and push it to your own registry, you can do something like:

```
podman build -t quay.io/$MYQUAYUSER/flightctl-agent:latest -f packaging/Containerfile.fedora ./
podman push quay.io/$MYQUAYUSER/flightctl-agent:latest
```

In order to make the deployment of this image easy, you can convert it to QCOW2 format and use it as a raw disk in Libvirt. The following command will create a file called disk.qcow2 within the output folder:

```
mkdir output
sudo podman run --rm -it --privileged --pull=newer \
     --security-opt label=type:unconfined_t \
     -v $(pwd)/output:/output \
     quay.io/centos-bootc/bootc-image-builder:latest \
     quay.io/$MYQUAYUSER/flightctl-agent:latest
```
