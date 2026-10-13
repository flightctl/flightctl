# OS and application delta e2e

Ginkgo suite for control-plane OS delta generation and application delta usage. The application tests are split into two groups:

- The non-Helm application specs run on the delta-capable bootc base image and update container, inline Compose, inline Quadlet, and VM applications. They do not change the OS or configure MicroShift. V2 uses newly built, full target images with an added layer; the target refs stay in the registry while the test verifies they are absent from the device before prefetch. The desired specs contain no delta hints. After the server reports successful generation, the test reads the rendered device spec and requires the generated hints before it waits for agent-reported status. It then checks the application-level `lastDelta.outcome`, target image digests, and `Running`/`Healthy` status. Fallback cases require a reported fallback reason.
- Compose includes two images. Quadlet exercises image discovery from a `.container` unit, a referenced `.image` unit, and an image-backed `.volume`, with containers sharing a `.pod` unit. The `.kube`/Pod YAML fixture was removed because kube-quadlet workloads lack the application labels needed by the monitor to report their image digest snapshots. Kube-quadlet needs separate delta e2e coverage. The standalone and fleet cases also update a VM app and retain its expected `Running`/`Healthy` assertions. A separate Compose case pauses the agent, waits for the server-generated hint, removes that delta artifact, and then verifies full-pull fallback reporting. Another case applies the same source-to-target delta in a later prepare and checks that the rendered spec reuses the existing delta artifact without a second generation event.
- The Helm specs migrate the device to the V12 MicroShift-capable image and wait for MicroShift readiness before installing the V1 Helm application. The V1 to V2 update increments the chart version and changes six workload image refs. The delta chart renders a Deployment with app, sidecar, and init container images, plus standalone Pod, Job, and suspended CronJob images. The test checks a rendered hint for every target image, requires an application-level `lastDelta.outcome` of `Applied`, verifies reported digests for workloads that create Pods, and confirms all target refs are present in CRI storage.

Both groups use the auxiliary OCI registry and test fixtures. The non-Helm application specs also need a VM image with `oci-delta`, `skopeo`, and `crictl` available. The suite clears delta preparation/generation state and old delta/target tags before each spec while preserving the bundled fixture tags. Rebuild agent images after the base Containerfile change:

```bash
make e2e-agent-images
```

Run the full suite:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/delta
```

Run only the application delta specs without Helm or MicroShift setup:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_LABEL_FILTER='delta && vm && !microshift'
```

Run a specific non-Helm application spec:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_LABEL_FILTER='delta && vm && !microshift' GINKGO_FOCUS="standalone device updates container, Compose, Quadlet, and VM applications"
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_LABEL_FILTER='delta && vm && !microshift' GINKGO_FOCUS="fleet updates container, Compose, Quadlet, and VM applications"
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_LABEL_FILTER='delta && vm && !microshift' GINKGO_FOCUS="nested application delta artifact is unavailable"
make run-e2e-test GO_E2E_DIRS=test/e2e/delta GINKGO_LABEL_FILTER='delta && vm && !microshift' GINKGO_FOCUS="later application prepare requests an already generated image delta"
```

The application specs run serially. They clean up test-owned target and delta tags; shared application fixture tags remain intact.
