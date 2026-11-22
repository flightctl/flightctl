# Running VM-render integration tests

This suite tests VM application rendering and launcher-image selection. It runs
against local test services and converts VM manifests with the real
`vm-to-quadlet` binary. It does not boot a VM or require `make deploy`, kind, or a
running Flight Control installation.

## Run the suite

From the repository root, enable the user Podman socket if needed:

```bash
systemctl --user enable --now podman.socket
```

Run the suite through the integration-test target:

```bash
make integration-test TEST_DIR=./test/integration/tasks/vmrender
```

The target starts the database and Alertmanager, runs migrations, and cleans up
shared services afterward. The suite starts its own Redis instances. Set
`INTEGRATION_PROCS=1` to run without parallel suite processes, or
`KEEP_INTEGRATION_STACK=1` to keep shared services for debugging.

## VM conversion image dependency

Suite setup uses this pinned tool image:

```text
quay.io/flightctl/vm-to-quadlet:2d2a64d59c10bbe33943e1c74835e5063177c3fb
```

The reference is defined in `suite_test.go`. Testcontainers obtains the image
when it is absent, copies `/usr/local/bin/vm-to-quadlet` to a temporary directory,
and runs that Linux binary on the host. Use a Linux host with the same
architecture as the image. The extraction container and temporary files are
removed after the suite.

The worker images built by `make deploy` include their own copy of the binary.
They do not populate this separate test image in the local container store.

To check image access separately from the tests:

```bash
podman pull quay.io/flightctl/vm-to-quadlet:2d2a64d59c10bbe33943e1c74835e5063177c3fb
```

Use the same container runtime and user as the tests. For rootless Podman, an
image pulled with `sudo podman` is in a different image store. When `DOCKER_HOST`
selects a different runtime, load the image into that runtime instead.

## Recover from image download failures

A failure in `SynchronizedBeforeSuite` means no test cases have run yet. Check the
underlying image-pull error. A TLS transfer error such as `bad record MAC` is a
registry/network transfer failure; deploying Flight Control does not repair it.

Retry the pull after restoring registry connectivity. Alternatively, pull the
exact image on another compatible machine, export it with `podman save`, transfer
the archive, and import it locally with `podman load`.

You can also build the pinned tool image locally using the checked-in Containerfile:

```bash
podman build \
  -f test/integration/tasks/vmrender/Containerfile.vm-to-quadlet \
  -t quay.io/flightctl/vm-to-quadlet:2d2a64d59c10bbe33943e1c74835e5063177c3fb .
```

This builds the same pinned source revision and tags it with the reference the
suite expects. It still requires access to the base images, GitHub, and Go module
sources. Keep the Containerfile's revision and the suite image reference aligned.

After making the image available, rerun the suite command above. A successful
setup is followed by execution of its specs; a setup failure is not a pass
or a test skip that establishes rendering behavior.
