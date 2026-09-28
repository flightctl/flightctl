# OS and application delta e2e

Ginkgo suite for control-plane OS delta generation and application delta usage. The standalone and fleet-owned application specs each update one container, inline Compose, inline Quadlet, Helm, and VM app using valid delta artifacts. Helm charts resolve normally; their nested workload images use the CRI delta importer. Both specs use supplied deltas for container, Compose, Quadlet, and Helm workload images, and verify a control-plane-generated VM hint on the rendered Quadlet workload. A separate Compose spec verifies fallback when its nested delta artifact is unavailable.

Rebuild agent images after the base Containerfile change so the VM has `oci-delta`:

```bash
make e2e-agent-images
```

Run:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/delta
```

Run only the standalone or fleet-owned application delta spec:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_FOCUS="standalone device updates all application types"
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_FOCUS="fleet updates all application types"
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_FOCUS="nested application delta artifact is unavailable"
```

The application specs require the V12 MicroShift-capable device image, the auxiliary OCI registry and Helm chart fixtures, plus a VM image with `oci-delta`, `skopeo`, and `crictl` available. They clean up test-owned target and delta tags; shared application fixtures remain intact. The specs are labeled `delta`, `microshift`, `slow`, and `vm` and run serially.
