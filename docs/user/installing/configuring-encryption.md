# Configuring encryption at rest

## Overview

Flight Control encrypts sensitive control plane data before storing it in PostgreSQL. This protects repository credentials, authentication provider secrets, and rendered device configuration data from unauthorized access if database contents, database backups, or storage media are exposed.

Encryption is enabled by default. During the first deployment, Flight Control generates an AES-256-GCM encryption key automatically. Default deployments do not require a manually provisioned key.

A custom key can be added later and configured as the active key. After rotation, new sensitive data is encrypted with the active key, and existing encrypted data is re-encrypted with the active key by the background migration process. The previous key can be retired after migration confirms that live data no longer depends on it.

Sensitive fields remain encrypted in PostgreSQL and in database backups.

Key points:

* **Enabled by default:** Encryption is active on new deployments. Services fail to start if the encryption key is missing, malformed, or invalid.
* **Automatic key generation:** The initial key is generated during deployment.
* **Custom key support:** A custom key can be added and made active through key rotation.
* **Key migration:** After rotation, existing encrypted data is re-encrypted with the active key by the background migration process.
* **Safe key retirement:** Old keys can be retired only after migration confirms that live data no longer depends on them.
* **Backup critical:** Losing the encryption key makes encrypted data permanently unreadable. The key is included in the standard backup and restore process, so backup archives must be protected accordingly.
* **Selective encryption:** Only documented sensitive fields are encrypted. Do not store sensitive information in fields that are not documented as protected.

## What is protected

### Protected fields

The following fields are encrypted before being stored in PostgreSQL:

| Resource | Field | Description |
|----------|-------|-------------|
| Repository | `spec.httpConfig.password` | HTTP basic authentication password |
| Repository | `spec.httpConfig.token` | HTTP bearer token |
| Repository | `spec.httpConfig.tls.key` | Client TLS private key |
| Repository | `spec.httpConfig.tls.crt` | Client TLS certificate |
| Repository | `spec.sshConfig.sshPrivateKey` | SSH private key |
| Repository | `spec.sshConfig.privateKeyPassphrase` | SSH key passphrase |
| Repository | `spec.ociAuth.password` | OCI registry password |
| AuthProvider | `spec.clientSecret` | Client secret for OIDC, OAuth2, OpenShift, and AAP authentication providers |
| Device | `renderedConfig` | Rendered device configuration |
| Device | `renderedApplications` | Rendered device applications |

### Unprotected fields

Fields that are not listed in the protected-fields table are not encrypted by Flight Control application-level encryption at rest.

Examples include:

* Resource names, labels, annotations, and metadata
* Repository URLs and repository types
* Authentication provider issuer URLs and client IDs
* Device status, system information, and conditions
* Fleet selectors, templates, and rollout policies

These fields remain visible in PostgreSQL and in database backups.

> [!WARNING]
> Do not store sensitive information such as passwords, tokens, private keys, or client secrets in fields that are not documented as protected. Only the fields listed in the protected-fields table are encrypted by Flight Control encryption at rest.

## Encryption key management

### Default generated key

During the first deployment, Flight Control generates an AES-256-GCM encryption key automatically. No manual key provisioning is required for initial setup.

The generated key uses the identifier `default`. Encrypted values include this identifier in their stored format:

```text
enc:v1:default:<base64 payload>
```

On upgrades, the existing key is preserved automatically to prevent accidental key loss. See [Upgrading existing deployments](#upgrading-existing-deployments).

### Key locations

The encryption key is loaded from a read-only mounted file inside each service container:

```text
/root/.flightctl/encryption/key
```

The source of this file depends on the deployment method.

#### OpenShift / Kubernetes

The key is stored in a Kubernetes Secret named `flightctl-encryption-key` in the deployment namespace. A Helm pre-install/pre-upgrade hook Job generates the key on first deployment and preserves the existing key on upgrades.

The Secret is mounted read-only at:

```text
/root/.flightctl/encryption/
```

in Flight Control service pods.

To verify that the Secret exists:

```bash
kubectl get secret flightctl-encryption-key -n <namespace>
```

To verify that the key is mounted inside a service pod:

```bash
kubectl exec deploy/flightctl-api -n <namespace> -- ls -la /root/.flightctl/encryption/key
```

For key rotation, additional key files are added to the same Secret. See [Rotating encryption keys](#rotating-encryption-keys).

#### Quadlet / Podman

The key file is stored on the host filesystem at:

```text
/etc/flightctl/encryption/key
```

During first deployment, Flight Control creates the key file if it does not already exist. On subsequent deployments and upgrades, the existing key is validated and preserved.

The host directory is mounted read-only into Flight Control service containers at:

```text
/root/.flightctl/encryption/
```

To verify that the key exists on the host:

```bash
ls -la /etc/flightctl/encryption/key
```

To verify that the key is mounted inside a service container:

```bash
podman exec flightctl-api ls -la /root/.flightctl/encryption/key
```

### Key file format

The key file contains a base64-encoded 32-byte, or 256-bit, random value.

Flight Control validates the key material at startup:

* The base64-decoded value must be exactly 32 bytes.
* All-zero keys are rejected as a configuration error.
* Missing, malformed, or incorrectly sized keys prevent services from starting.

To generate a key manually:

```bash
openssl rand -base64 32
```

### Providing your own key

#### Fresh installs

For fresh installations, you can provide a custom encryption key before the first deployment. If an encryption key already exists, Flight Control preserves it and does not generate a replacement key, so no rotation or migration is needed.

The key must be a base64-encoded 32-byte random value. See [Key file format](#key-file-format).

On **OpenShift / Kubernetes**, create the `flightctl-encryption-key` Secret before running `helm install`:

```bash
kubectl create secret generic flightctl-encryption-key \
  -n <namespace> \
  --from-file=key=<path-to-your-key-file>
```

On **Quadlet / Podman**, place the key file before starting Flight Control services:

```bash
sudo mkdir -p /etc/flightctl/encryption
sudo install -m 0600 <path-to-your-key-file> /etc/flightctl/encryption/key
```

#### Existing deployments

For deployments that already have encrypted data, use key rotation to switch to a custom key. Rotation adds the custom key alongside the existing key, marks the custom key as active for new encryptions, and migrates existing encrypted data to the active key. The original key remains configured for decryption until migration completes. See [Rotating encryption keys](#rotating-encryption-keys).

## Backup and restore requirements

Encryption keys are required to read encrypted data stored in PostgreSQL. Losing the required encryption keys makes the affected encrypted fields permanently unreadable.

Flight Control includes encryption keys in backup archives created by `flightctl-backup` when the encryption key Secret or key directory is available at backup time. If the encryption key Secret in Kubernetes, or the encryption key directory in Podman, cannot be found, the backup completes with a warning and the archive is created without key material.

After each backup, check the backup logs and verify that the archive contains the expected encryption key material before relying on the archive for disaster recovery.

Restoring from a backup archive with `flightctl-restore` restores the encryption keys alongside the database and other server state. Do not restore a database backup without restoring the matching encryption keys. A replacement key cannot decrypt data that was encrypted with a previous key.

For backup and restore procedures, archive contents, scheduling, and post-restore verification, see [Backup and restore](backup-restore.md).

> [!WARNING]
> Backup archives may contain encryption keys, database contents, CA private keys, and TLS certificates. Store backup archives on encrypted storage with restricted access.

## Rotating encryption keys

Key rotation configures a new active encryption key. After rotation, new sensitive data is encrypted with the new key, and existing encrypted data is re-encrypted by a background migration process.

The previous key must remain configured during migration so that Flight Control can continue to decrypt data that was encrypted before rotation.

### Before you rotate

Before rotating encryption keys:

* Back up the deployment, including the current encryption keys. See [Backup and restore](backup-restore.md).
* Verify that the current encryption configuration is healthy. See [Active key verification](#active-key-verification).
* Generate a new 32-byte key:

```bash
(umask 077 && openssl rand -base64 32 > key-2026-07)
```

### Add the new key and update the configuration

#### OpenShift / Kubernetes

For Helm deployments, add the new key to the `flightctl-encryption-key` Secret, update the encryption configuration so that both keys are listed, and make the new key active.

Add the new key to the existing `flightctl-encryption-key` Secret. The Secret must contain both the existing key and the new key.

Example:

```bash
kubectl patch secret flightctl-encryption-key -n <namespace> \
  --type merge \
  --patch-file <(cat <<EOF
stringData:
  key-2026-07: "$(cat key-2026-07)"
EOF
)
```

Update `values.yaml` so that the new key is the active key:

```yaml
encryption:
  activeKeyID: "key-2026-07"
  keys:
    - id: "default"
      file: "key"
    - id: "key-2026-07"
      file: "key-2026-07"
```

Run `helm upgrade` to apply the configuration changes, then restart the Flight Control deployments that initialize encryption. Encryption keys and the active key configuration are loaded once at process startup, so running pods do not pick up the new active key until they are restarted:

```bash
helm upgrade <release> deploy/helm/flightctl -n <namespace> -f values.yaml

kubectl rollout restart -n <namespace> \
  deployment/flightctl-api \
  deployment/flightctl-worker \
  deployment/flightctl-periodic \
  deployment/flightctl-alert-exporter \
  deployment/flightctl-alertmanager-proxy \
  deployment/flightctl-remote-access \
  deployment/flightctl-imagebuilder-api \
  deployment/flightctl-imagebuilder-worker
```

#### Quadlet / Podman

For Quadlet or Podman deployments, place the new key file on the host, update the encryption configuration, and restart Flight Control services.

Add the new key file:

```bash
sudo install -m 0600 key-2026-07 /etc/flightctl/encryption/key-2026-07
```

Update the `encryption` block in `/etc/flightctl/service-config.yaml` so that both keys are listed and the new key is active:

```yaml
encryption:
  activeKeyID: "key-2026-07"
  keys:
    - id: "default"
      path: "/root/.flightctl/encryption/key"
    - id: "key-2026-07"
      path: "/root/.flightctl/encryption/key-2026-07"
```

Restart Flight Control services:

```bash
sudo systemctl restart flightctl.target
```

### Verify the new active key

After services restart, verify that the new key is reported as active. See [Active key verification](#active-key-verification).

### Migrate existing data to the active key

After rotation, the background migration process re-encrypts existing protected data with the active key. No manual action is required to start the migration.

Flight Control emits an `EncryptionMigrationStarted` event when migration begins and an `EncryptionMigrationCompleted` event when migration finishes.

Monitor migration progress using migration events. For more information about events, see [Encryption Events](../references/events.md#encryption-events). Encryption operation metrics can also be used to observe encryption, decryption, and error activity during migration. See [Encryption metrics](#encryption-metrics) in [Observability and validation](#observability-and-validation) for details.

Migration is complete when Flight Control reports that all protected records have been processed and no persisted encrypted values reference the previous key.

### Retire old keys

After migration completes, Flight Control prepares non-active keys for retirement by removing their canaries. The old key can then be removed from the encryption configuration and from the Secret or host filesystem.

To retire an old key:

1. Confirm that `EncryptionMigrationCompleted` was emitted for the migration.
2. Remove the old key entry from the `keys` list in the encryption configuration.
3. Apply the updated configuration and restart the Flight Control deployments or services that initialize encryption.
4. Remove the old key file from the Secret or host filesystem.

For OpenShift or Kubernetes deployments, run `helm upgrade` and then restart the Flight Control deployments that initialize encryption:

```bash
helm upgrade <release> deploy/helm/flightctl -n <namespace> -f values.yaml

kubectl rollout restart -n <namespace> \
  deployment/flightctl-api \
  deployment/flightctl-worker \
  deployment/flightctl-periodic \
  deployment/flightctl-alert-exporter \
  deployment/flightctl-alertmanager-proxy \
  deployment/flightctl-remote-access \
  deployment/flightctl-imagebuilder-api \
  deployment/flightctl-imagebuilder-worker
```

For Quadlet or Podman deployments, restart Flight Control services:

```bash
sudo systemctl restart flightctl.target
```

> [!WARNING]
> Do not remove an old key before migration completes. If an existing canary still depends on that key, affected services fail startup. If any encrypted data still references the removed key, that data cannot be decrypted until the key is restored. If the old key material is lost, values encrypted with that key become permanently unreadable.

## Upgrading existing deployments

When upgrading from a version that did not support encryption at rest, encryption is enabled automatically.

During the upgrade, Flight Control generates the initial encryption key if one does not already exist. Existing plaintext protected values remain readable during migration, and new or updated protected values are written in encrypted form.

A background migration process then encrypts existing protected data.

For deployments where encryption at rest is already enabled, Flight Control preserves the existing encryption keys and continues to read existing encrypted data. Existing data is re-encrypted only when required by key rotation, migration, or normal write-time processing.

No service downtime is required for the plaintext-to-encrypted transition.

## Observability and validation

### Active key verification

Services that initialize encryption log the active encryption configuration during startup. Look for a startup log entry similar to:

```text
Encryption initialized: active=v1/default
```

After key rotation, the startup log should report the new key ID, for example:

```text
Encryption initialized: active=v1/key-2026-07
```

The `flightctl_encryption_active_key_info` metric also reports the active configuration:

```text
flightctl_encryption_active_key_info{algorithm="AES-256-GCM",key_id="default",strategy="v1"} 1
```

Each metrics-emitting process reports exactly one active encryption configuration. After key rotation, the `key_id` label should reflect the new active key.

### Canary validation

Flight Control validates encryption canaries during startup. A canary is a known value encrypted and stored in PostgreSQL for a specific strategy and key ID.

During startup, Flight Control ensures that a canary exists for the active strategy and key, then validates all persisted canaries. Existing canaries must be decryptable with the configured keys and must match the expected value.

If an existing canary cannot be decrypted or does not match the expected value, the affected service fails startup. The absence of a canary for a newly configured key is not treated as a startup failure.

Canary validation failures are reported through service logs and service status. Startup canary validations are not reflected in `flightctl_encryption_canary_validations_total`.

Canaries validate that configured keys can decrypt known values previously written by Flight Control. They do not validate every encrypted database record.

### Encryption metrics

Flight Control exposes Prometheus metrics for encryption operations. Metric collectors are registered in `flightctl-api` and `flightctl-worker`.

| Metric | Type | Description |
|--------|------|-------------|
| `flightctl_encryption_active_key_info` | Gauge | Active strategy, key ID, and algorithm |
| `flightctl_encryption_operations_total` | Counter | Encryption and decryption operations by operation, strategy, key, and status |
| `flightctl_encryption_operation_duration_seconds` | Histogram | Encryption and decryption operation latency |
| `flightctl_encryption_errors_total` | Counter | Encryption and decryption errors by operation, strategy, key, and error type |
| `flightctl_encryption_canary_validations_total` | Counter | Canary validation attempts and outcomes |

For the full metrics reference, see [Flight Control Metrics](../references/metrics.md#encryption-metrics). For setting up metrics collection and access, see [Deploying observability on Kubernetes](deploying-observability-kubernetes.md) or [Deploying observability on Linux](deploying-observability-linux.md).

### Tracing

When tracing is enabled, Flight Control emits OpenTelemetry spans under the `flightctl/encryption` tracer.

Encryption spans include:

* `process`
* `encrypt`
* `decrypt`
* `canary-ensure`
* `canary-validate`
* `canary-validate-all`

Span attributes include the operation type, strategy, key ID, result, and processing action where applicable.

Traces do not contain plaintext values, ciphertext, encryption keys, nonces, authentication tags, or protected resource contents.

## Troubleshooting

| Symptom | Possible cause | Action |
|---------|----------------|--------|
| Service fails to start after installation or upgrade | Missing, malformed, or incorrectly sized encryption key | Check that the key file is mounted and that it decodes to exactly 32 bytes. |
| Service fails to start after key rotation | A required historical key was removed too early, or canary validation failed | Check service logs, restore the previous key, and restart the service. |
| Old key cannot be retired | Migration has not completed, or data still references the old key | Wait for `EncryptionMigrationCompleted` and confirm no persisted encrypted values reference the old key. |
| Migration does not complete | Migration worker is failing or cannot decrypt existing data | Check migration events, service logs, and encryption error metrics. |

## Security considerations

Encryption at rest protects sensitive Flight Control data if PostgreSQL contents, database backups, or storage media are exposed without the encryption keys.

Encryption at rest does not protect data while it is being used by a running Flight Control service. It also does not protect against compromise of an authorized Flight Control service that has access to the encryption key.

Only the documented protected fields are encrypted. Do not store passwords, tokens, private keys, client secrets, or other sensitive values in unprotected fields.

Backup archives contain encryption keys and encrypted data. Protect backup archives with the same level of access control as production secrets.

Do not delete historical encryption keys until migration confirms that no persisted encrypted values depend on them. Historical keys may also be required to restore older backups.
