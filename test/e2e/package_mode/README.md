# Package-mode E2E prerequisites

These tests require:

- test runner support for privileged containers with read-write `/sys/fs/cgroup` bind mounts for the nested Podman helper container
- prepared agent config and certs under `bin/agent/etc/flightctl`

Prepare the environment first with:

```bash
make prepare-e2e-test
```

Run the package-mode suite with:

```bash
make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/package_mode
```
