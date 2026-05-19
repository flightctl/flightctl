# Auto-syncing external dependencies

Flight Control automatically detects changes in external dependencies referenced by fleet and device templates — git repositories, HTTP endpoints, and Kubernetes secrets. When a change is detected, the service creates a new template version and rolls it out to affected devices, or re-renders standalone devices directly.

## How auto-sync works

Auto-sync is enabled by default for any fleet or device that references external configuration providers (git, HTTP, or Kubernetes secret). No opt-in is required.

A component called the **Dependency Sync Controller** runs inside the `flightctl-periodic` service. It periodically checks each dependency for upstream changes by comparing the current state against a stored fingerprint. When the fingerprint changes, the controller emits a `DependencyChangeDetected` event on the affected fleet or device, which triggers the standard rollout pipeline.

### What triggers a new template version

For fleet-owned devices, a detected change creates a new template version named `v{generation}-{short-hash}`, where `short-hash` is the first 8 characters of the new fingerprint. The rollout policy then governs how the update reaches devices.

For standalone devices (not owned by a fleet), a detected change triggers a direct re-render of the device specification without creating a template version.

### Parameterized references

Fleet templates can contain placeholders (for example, `{{ .metadata.labels.branch }}`) in fields such as git `targetRevision` or HTTP `suffix`. When a fleet references a parameterized field, the system resolves the concrete value per device during rollout and tracks each device's resolved dependency independently. Only devices whose specific resolved reference changed receive an update — the entire fleet is not re-rolled.

## Detection mechanisms

### Git repositories

The controller uses `git ls-remote` to resolve the current commit SHA for each referenced branch or tag. If the SHA differs from the stored fingerprint, a change is detected.

### HTTP endpoints

The controller sends a conditional HEAD request using `If-None-Match` (for ETags) or `If-Modified-Since` headers:

* If the endpoint responds with `304 Not Modified`, no change occurred.
* If the endpoint responds with `200 OK` and a new ETag or Last-Modified value, a change is detected.
* If the endpoint does not return ETag or Last-Modified headers, the probe skips active detection for that endpoint.

> [!NOTE]
> Endpoints that do not support conditional requests (no ETag or Last-Modified headers) cannot be actively monitored by the sync controller. However, the device render path always fetches the full response body and computes a `sha256` hash as the fingerprint. Changes to these endpoints are detected passively — the next time a device re-renders for any reason (for example, another dependency changes or the fleet template is updated), the new body hash is captured and reflected in the sync status.

### Kubernetes secrets

The controller uses a Kubernetes SharedInformer to watch labeled secrets in real time. This is push-based — there is no polling interval for secrets.

To enable secret watching:

1. Label each secret that Flight Control should watch:

   ```console
   kubectl label secret <secret-name> flightctl.io/sync-<release-namespace>=true
   ```

   Replace `<release-namespace>` with the namespace where Flight Control is deployed. For example, if Flight Control is deployed in the `flightctl` namespace:

   ```console
   kubectl label secret my-secret flightctl.io/sync-flightctl=true
   ```

2. Enable cluster-level secret access in your Helm values:

   ```yaml
   periodic:
     clusterLevelSecretAccess: true
   worker:
     clusterLevelSecretAccess: true
   ```

The `periodic` setting enables the informer to watch secrets. The `worker` setting enables embedding secret data into device configurations during rendering.

> [!IMPORTANT]
> The secret informer starts only when `flightctl-periodic` runs inside a Kubernetes cluster. Out-of-cluster deployments (such as Podman/Quadlet) do not support secret watching.

## Viewing sync status

Each device reports the fingerprints of its external dependencies in its status. Inspect them with:

```console
flightctl get device <device-name> -o yaml
```

Under `.status.dependencySync.configRefs`, each entry shows:

| Field | Description |
| ----- | ----------- |
| `configProviderName` | The name of the config provider from the device or fleet template. |
| `fingerprint` | The fingerprint captured at render time. For git: the commit SHA. For HTTP: `sha256:<hash>` of the response body. For secrets: the Kubernetes `ResourceVersion`. |
| `lastUpdatedAt` | The last time the fingerprint changed. Preserved across re-renders if the content has not changed. |

Example output:

```yaml
status:
  dependencySync:
    configRefs:
      - configProviderName: app-config
        fingerprint: "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2"
        lastUpdatedAt: "2026-05-18T10:30:00Z"
      - configProviderName: tls-certs
        fingerprint: "48291"
        lastUpdatedAt: "2026-05-17T08:15:00Z"
```

> [!NOTE]
> Only config providers that reference external dependencies (git, HTTP, or Kubernetes secret) produce `dependencySync` entries. Inline configuration providers do not appear.

## Configuring the polling interval

Git and HTTP dependencies are checked at a configurable global polling interval. The default is 15 minutes.

**For Helm deployments**, the polling interval is set in the service configuration that gets mounted into the `flightctl-periodic` pod. Edit the `periodic` section of the Helm-generated ConfigMap (typically `flightctl-periodic-config`) or add the setting to your values override that populates `service-config.yaml`:

```yaml
dependenciesSync:
  pollInterval: 15m
```

**For Podman (Quadlet) deployments**, edit `/etc/flightctl/service-config.yaml`:

```yaml
dependenciesSync:
  pollInterval: 15m
```

The polling interval does not affect Kubernetes secrets, which use a push-based watch.

> [!NOTE]
> The sync task runs on a fixed internal interval (every 3 minutes, not user-configurable) to discover newly added dependencies and retry partial failures. The `pollInterval` controls how long each individual dependency must wait since its last successful probe before being checked again.

## Events

Auto-sync emits two event types. View them with:

```console
flightctl get events --field-selector="reason=DependencyChangeDetected"
flightctl get events --field-selector="reason=DependencySyncProbeFailed"
```

### DependencyChangeDetected

* **Type:** Normal
* **Involved object:** Fleet or Device (depending on ownership)
* **Meaning:** An external dependency fingerprint changed. For fleets, this triggers a new template version. For standalone devices, this triggers a re-render.
* **Event details:**
  * `resourceKey` — the dependency identifier (for example, `git:my-repo/main` or `http:my-config/api/v1/config`)
  * `fingerprint` — the new fingerprint value

### DependencySyncProbeFailed

* **Type:** Warning
* **Involved object:** Fleet or Device
* **Meaning:** A probe failed while checking a dependency. The stored fingerprint is not updated and no rollout is triggered.
* **Event details:**
  * `resourceKey` — the dependency that failed
  * `errorMessage` — a sanitized description of the error (credentials are automatically redacted)

## Troubleshooting

### A device does not pick up upstream changes

1. Verify the device has external dependencies:

   ```console
   flightctl get device <device-name> -o yaml
   ```

   Check that `.status.dependencySync.configRefs` contains entries.

2. Check for probe failure events:

   ```console
   flightctl get events --field-selector="reason=DependencySyncProbeFailed,involvedObject.name=<fleet-or-device-name>"
   ```

3. Verify the repository is accessible:

   ```console
   flightctl get repository <repo-name>
   ```

   Confirm that the `ACCESSIBLE` status is `True`.

### An HTTP endpoint is not triggering updates

If the HTTP endpoint does not return ETag or Last-Modified headers, the sync controller cannot actively detect changes. Changes are only detected passively when a device re-renders for another reason.

To verify whether an endpoint supports conditional requests:

```console
curl -I <endpoint-url>
```

Look for `ETag` or `Last-Modified` in the response headers.

### A Kubernetes secret is not being tracked

1. Verify the secret has the correct label:

   ```console
   kubectl get secret <secret-name> -n <namespace> --show-labels
   ```

   The secret must have the label `flightctl.io/sync-<release-namespace>=true`.

2. Verify that `clusterLevelSecretAccess` is enabled for the periodic service.

3. Check the informer health metric. For Helm deployments, forward the periodic metrics port first:

   ```console
   kubectl port-forward svc/flightctl-periodic-metrics 15690:15690
   ```

   Then query the metric:

   ```console
   curl http://localhost:15690/metrics | grep flightctl_dependency_sync_informer_connected
   ```

   A value of `1` indicates the informer is connected. A value of `0` indicates a disconnect. For more details on available metrics, see [Metrics Configuration](../references/metrics.md).

### Git authentication failures

Git probe failures are reported through `DependencySyncProbeFailed` events. Verify that:

* The repository resource has valid credentials configured.
* The repository is reachable from the `flightctl-periodic` service.
* The target branch or tag exists in the remote repository.

## Security considerations

* **Credential reuse:** Auto-sync uses the same credentials configured on Repository resources. No additional secret storage is required.
* **Error sanitization:** Error messages in events and logs have credential-like patterns (passwords, tokens, bearer values) automatically redacted.
* **Secret informer scope:** The informer watches only secrets that are explicitly labeled with `flightctl.io/sync-<release-namespace>=true`. Unlabeled secrets are invisible.
* **RBAC:** The informer requires an opt-in `ClusterRole` enabled through the `clusterLevelSecretAccess` Helm value. Without it, secret watching is disabled.
* **In-cluster only:** The secret informer starts only when Kubernetes in-cluster configuration is available. Deployments outside a Kubernetes cluster do not attempt to watch secrets.
