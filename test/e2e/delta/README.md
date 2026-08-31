# OS delta e2e

Ginkgo suite for control-plane OS delta generation hold, opt-out, deadline, standalone render delay, and missing writable target.

Rebuild agent images after the base Containerfile change so the VM has `oci-delta`:

```bash
make e2e-agent-images
```

Run:

```bash
make run-e2e-test GO_E2E_DIRS=test/e2e/delta
```
