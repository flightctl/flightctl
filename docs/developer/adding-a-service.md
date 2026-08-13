# Adding a service

There is no generator for new Flight Control services. You hand-write the binary, containers, Helm, and quadlets, then register the service so CI can catch forgotten wiring.

## 1. Register the service

Add an entry to [`hack/services.yaml`](../../hack/services.yaml):

```yaml
- name: my-service
  profile: backend-external   # or backend-internal / image / images-yaml-only
```

Profiles expand to the membership checks below. Use overrides only when something is exceptional (for example `helmValuesKey: imageBuilderApi` or `makeContainerTarget: multiarch-cli`).

| Profile | Typical use |
|---|---|
| `backend-internal` | Worker-style services (Helm + quadlet, internal namespace) |
| `backend-external` | API-style services (also OpenShift Route, Service, nginx hostname, TLS cert wiring) |
| `image` | Published container without a full backend stack |
| `images-yaml-only` | Third-party or out-of-repo images listed in `images.yaml` |

ServiceAccounts are **not** required by profile. If a service needs one, add `requireServiceAccount: true` and ship the Helm template. Missing SAs are treated as intentional unless that override is set.

## 2. Hand-write the service

Implement as needed for the profile:

- `cmd/flightctl-<name>/`
- Containerfiles under `packaging/images/el9/` and `el10/`
- Helm under `deploy/helm/flightctl/templates/<name>/` (Deployment; ServiceAccount only when needed; for external also Route + Service)
- Quadlet under `deploy/podman/flightctl-<name>/`
- For external services: nginx upstream in `deploy/podman/flightctl-gateway/.../nginx.conf.template`, and TLS SAN wiring in cert generators

## 3. Run verification

```bash
make verify-services
```

This runs unit tests for [`tools/verify-services`](../../tools/verify-services) and checks the live tree against `hack/services.yaml`. Fix every reported gap before merging. CI runs the same target via `.github/workflows/verify-services.yaml`.

### What CI verifies (membership / existence)

- Publish matrix, `images.yaml` / `local-images.yaml` (el9 + el10), Containerfiles, Make build/container lists, quadlet `podman save`, collect-logs, tag override
- Helm values/schema/templates dir/deployment; ServiceAccount when `requireServiceAccount: true`; for external also Route + Service; `helm-chart-opts` image keys
- Quadlet directory, `manifest.go`, `flightctl.target` Wants=
- For external: `flightctl-<name>` mentioned in nginx.conf.template
- For TLS: `--*-san` in `generate-certificates.sh`; Helm openssl/cert-manager when Helm-enabled; `init_certs.sh` only when the quadlet unit mounts `pki/flightctl-<name>/server.crt` (gateway-terminated HTTP upstreams like imagebuilder-api do not need this)

### What you still finish by hand

- ClusterRole verbs and other RBAC rule contents
- Gateway API HTTPRoutes, NetworkPolicy, SCC
- Exact nginx path prefixes and ports
- Exact certificate SAN hostname lists
- Config defaults, pprof ports, Prometheus scrapes, RPM `%files`, redeploy helpers

## Related

- Quadlet details: [service-quadlets.md](service-quadlets.md)
- Deploy layout: [deploy/AGENTS.md](../../deploy/AGENTS.md)
