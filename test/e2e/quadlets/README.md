# Quadlets E2E Tests

E2E tests for **Quadlets application type support** as defined in the Edge Management - application Test Plan, **section 4.2**.

## Requirements

- **Environment**: Quadlet tests require a **RHEL device** with FlightCtl deployed via **quadlets** (e.g. `make deploy-quadlets-vm`). Standard e2e VMs use a podman-compose agent and **do not support** quadlet applications.

Quadlet tests use **inline** quadlet content (or OCI image provider for artifact tests) and a **plain public container image** (`quay.io/flightctl-tests/alpine:v1`, a minimal runtime) for the quadlet `Image=` field. They do **not** use the compose application bundle `sleep-app`, which contains a compose file and is for the compose application type.

## Running

Use the same pattern as other e2e env vars (`FLIGHTCTL_NS`, `KUBEADMIN_PASS`): **export** them before running make.

```bash
# With a quadlet-capable environment (e.g. RHEL VM from deploy-quadlets-vm)
export FLIGHTCTL_NS=flightctl
export KUBEADMIN_PASS=your-oc-password

make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/quadlets
```

To run only the "Quadlet application lifecycle" context:

```bash
make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/quadlets GINKGO_FOCUS="Quadlet application lifecycle"
```

To run a single test by its `It` description:

```bash
make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/quadlets FLIGHTCTL_NS=flightctl-external \
  GINKGO_FOCUS="verifies that a quadlets application can be deployed, updated and removed in an edge manager device"
```

**Troubleshooting:** If `make in-cluster-e2e-test` fails during `deploy-e2e-extras` with "image not present locally" and then "permission denied" when pulling (e.g. podman creating `/var/lib/containers/storage/libpod`), ensure podman/docker can pull images (e.g. run with appropriate permissions or pre-pull `quay.io/flightctl/e2eregistry:2`).

Or, if the full e2e stack is already up and you have a quadlet VM enrolled:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/quadlets
```

**Note:** Current e2e infrastructure does not yet provide quadlet VMs in the default pool. Run with `GO_E2E_DIRS=test/e2e/quadlets` when you have a quadlet-capable environment.

## Test Plan Mapping (Section 4.2)

| Test plan case | It description | Label / Polarion |
|----------------|----------------|-------------------|
| flightctl quadlets application lifecycle | verifies that a quadlets application can be deployed, updated and removed in an edge manager device | quadlets-lifecycle |
| A complex application can be deployed to flightctl | verifies that a complex application including networks, volumes, pods, images, containers and envs can be deployed to an edge manager device | quadlets-complex |
| Image provider can extract and deploy Quadlet files from OCI artifacts | verifies that we can create single or multiple files artifacts (also compressed) packaged in an image and install them in an EM device | quadlets-oci-artifacts |
| Inline quadlets complex application with references ... survives a reboot | inline quadlets complex application with references can be deployed to an EM device and survives a reboot | 86280 (OCP-86280), sanity |
| Validations in quadlets applications | verifies that there are validations and readable error messages in quadlets application files | quadlets-validations |
| A quadlets app with crashed containers reports Degraded status | verifies that a crashing quadlets app is reported as Degraded | quadlets-degraded |

Polarion IDs for the remaining cases are TBD by QE; update the `Label(...)` in each `It` when assigned.

## References

- Test plan: `t.pdf` section 4.2 (Quadlets applications type support)
- E2E development guide: [test/e2e/README.md](../README.md)
- Deploy quadlets VM: [test/scripts/deploy_quadlets_rhel.sh](../../scripts/deploy_quadlets_rhel.sh)
