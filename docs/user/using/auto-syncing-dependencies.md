# Auto-syncing external dependencies

Flight Control automatically monitors external configuration references (git repositories, HTTP endpoints, and Kubernetes secrets) for changes. When a referenced dependency changes upstream, affected devices are updated automatically through the standard rollout pipeline.

## How auto-sync works

Any fleet or device that references an external config provider is automatically monitored for upstream changes. When a change is detected, fleet-owned devices receive a new template version and the rollout policy governs how the update reaches them. Standalone devices (not owned by a fleet) are re-rendered directly.

Fleet templates can contain placeholders (for example, `{{ .metadata.labels.branch }}`) in fields such as git `targetRevision` or HTTP `suffix`. When placeholders are used, each device's resolved reference is tracked independently — only devices whose specific dependency changed receive an update.

## Setting up auto-sync for each source type

### Git repositories

Git-based config providers are monitored automatically. No additional setup is required beyond defining the git config provider in your fleet or device template. Flight Control checks each referenced branch or tag for new commits at a configurable polling interval (default: 15 minutes).

### HTTP endpoints

HTTP config providers are monitored automatically. For best results, use an endpoint that returns **ETag** or **Last-Modified** response headers — this enables efficient change detection without downloading the full response body each cycle.

To verify whether your endpoint supports these headers:

```console
curl -I <endpoint-url>
```

Look for `ETag` or `Last-Modified` in the response headers.

> [!NOTE]
> If the endpoint does not return ETag or Last-Modified headers, changes are still detected, but only passively — the next time the device re-renders for any reason (for example, another dependency changes or the fleet template is updated), the new content is picked up and reflected in the sync status.

### Kubernetes secrets

To enable automatic change detection for Kubernetes secrets:

1. Label each secret that Flight Control should monitor:

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

   The `periodic` setting allows Flight Control to watch for secret changes. The `worker` setting allows embedding secret data into device configurations during rendering.

> [!IMPORTANT]
> Secret watching requires `flightctl-periodic` to run inside a Kubernetes cluster. Out-of-cluster deployments (such as Podman/Quadlet) do not support secret change detection.

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

The polling interval does not affect Kubernetes secrets, which are watched in real time.

> [!NOTE]
> Newly added dependencies are discovered within approximately 3 minutes. The `pollInterval` controls how long each individual dependency waits between checks.

## Events

Auto-sync emits two event types. View them with:

```console
flightctl get events --field-selector="reason=DependencyChangeDetected"
flightctl get events --field-selector="reason=DependencySyncProbeFailed"
```

### DependencyChangeDetected

* **Type:** Normal
* **Involved object:** Fleet or Device (depending on ownership)
* **Meaning:** An external dependency changed. For fleets, this triggers a new template version. For standalone devices, this triggers a re-render.
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

If the HTTP endpoint does not return ETag or Last-Modified headers, changes are only detected passively when a device re-renders for another reason.

To verify whether an endpoint supports active change detection:

```console
curl -I <endpoint-url>
```

Look for `ETag` or `Last-Modified` in the response headers. If neither is present, consider configuring your HTTP server to return one of these headers for reliable active sync.

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

   A value of `1` indicates the watcher is connected. A value of `0` indicates a disconnect. For more details on available metrics, see [Metrics Configuration](../references/metrics.md).

### Git authentication failures

Git probe failures are reported through `DependencySyncProbeFailed` events. Verify that:

* The repository resource has valid credentials configured.
* The repository is reachable from the `flightctl-periodic` service.
* The target branch or tag exists in the remote repository.

## Security considerations

* **Credential reuse:** Auto-sync uses the same credentials configured on Repository resources. No additional secret storage is required.
* **Error sanitization:** Error messages in events and logs have credential-like patterns (passwords, tokens, bearer values) automatically redacted.
* **Secret watching scope:** Only secrets explicitly labeled with `flightctl.io/sync-<release-namespace>=true` are monitored. Unlabeled secrets are invisible to Flight Control.
* **RBAC:** Secret watching requires an opt-in `ClusterRole` enabled through the `clusterLevelSecretAccess` Helm value. Without it, secret change detection is disabled.
* **In-cluster only:** Secret watching is available only when Flight Control runs inside a Kubernetes cluster. Deployments outside a cluster do not attempt to watch secrets.
