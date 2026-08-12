# EDM-4774: Encryption at Rest — QA Test Plan

## Context

All sensitive fields in the database (clientSecret, passwords, SSH keys, tokens, TLS certs/keys) are now encrypted at rest. Data is encrypted automatically on save and decrypted transparently when used. QA needs to verify that all credential flows still work end-to-end across both deployment types.

## Environments

Test on **both**:
- **Quadlet (Podman/systemd)**
- **OpenShift (Kubernetes)**

---

## 1. AuthProvider — Login Flows

### 1.1 OAuth2 Provider
- [ ] Create an OAuth2 AuthProvider with clientId and clientSecret
- [ ] Log in to the UI using OAuth2 — verify login succeeds
- [ ] After logging in, make API calls (e.g. `flightctl get fleets`) — verify they succeed (this exercises server-side token validation using the clientSecret)
- [ ] Update the clientSecret on the provider — verify login still works with the new secret
- [ ] Verify the old secret no longer works after update

### 1.2 OIDC Provider
- [ ] Create an OIDC AuthProvider with clientId and clientSecret
- [ ] Log in to the UI using OIDC — verify login succeeds
- [ ] Make API calls after login — verify they succeed

### 1.3 OpenShift Provider
- [ ] Create an OpenShift AuthProvider with clientSecret
- [ ] Log in using OpenShift credentials — verify login succeeds
- [ ] Make API calls after login — verify they succeed

### 1.4 AAP Provider
- [ ] Create an AAP AuthProvider with clientSecret
- [ ] Verify authentication through the AAP gateway works

### 1.5 Provider Stability
- [ ] Create an AuthProvider, wait a minute
- [ ] Check API server logs — verify no repeated "provider changed" messages when nothing was changed
- [ ] Update the provider spec — verify the change is picked up

### 1.6 Sensitive Data Not Exposed
- [ ] GET an AuthProvider via CLI or API — verify clientSecret shows `****`, not the real value or encrypted gibberish

---

## 2. Repository — Credential Flows

### 2.1 OCI Repository with Auth
- [ ] Create a Repository with OCI type and docker auth (username/password)
- [ ] Verify the repository status shows Accessible=True
- [ ] Update the OCI password — verify accessibility still works with the new password
- [ ] Create an ImageBuild that pulls from a private source registry using this repository — verify the build succeeds
- [ ] Create an ImageBuild that pushes to a private destination registry — verify the push succeeds
- [ ] If SBOM is enabled, verify SBOM export to the registry succeeds

### 2.2 Git Repository with HTTP Auth
- [ ] Create a Repository with HTTP config (password or token)
- [ ] Verify the repository status shows Accessible=True
- [ ] Create a Fleet referencing this repository with a git config provider
- [ ] Enroll a device into the fleet
- [ ] Verify the device receives the correct rendered config from the repository

### 2.3 Git Repository with SSH Auth
- [ ] Create a Repository with SSH config (private key + passphrase)
- [ ] Verify the repository status shows Accessible=True
- [ ] Create a Fleet referencing this repository
- [ ] Verify device rendering succeeds

### 2.4 Git Repository with TLS Client Certs
- [ ] Create a Repository with HTTP config including TLS cert and key
- [ ] Verify accessibility and device rendering

### 2.5 Sensitive Data Not Exposed
- [ ] GET a Repository via CLI or API — verify password, token, sshPrivateKey, privateKeyPassphrase, tlsKey, tlsCrt all show `****`

---

## 3. Device Rendering End-to-End

- [ ] Create a Fleet with a config provider pointing to a private repository (requires credentials)
- [ ] Enroll a device into the fleet
- [ ] Verify the device receives the correct rendered config
- [ ] Update the repository credentials — verify the device gets updated config on the next render cycle

---

## 4. Image Builder (OCI Auth)

- [ ] Create an ImageBuild that pulls from a private (authenticated) source registry
- [ ] Verify the build succeeds
- [ ] Verify the push to a private destination registry succeeds
- [ ] If SBOM is enabled, verify SBOM generation and push succeeds

---

## 5. Encrypt-on-Save

Verify that sensitive data is actually encrypted in the database:

- [ ] Create a new Repository with credentials via CLI/API
- [ ] Query the database directly (`psql`) — verify the sensitive fields in the `spec` column are stored as `enc:v1:...` ciphertext, **not** plaintext
- [ ] Update a credential field on the Repository (e.g. change the password)
- [ ] Query the DB again — verify the field is re-encrypted (new ciphertext value)
- [ ] Do the same for AuthProvider clientSecret — create, check DB, update, check DB again
- [ ] After each save, verify the resource still works (repo accessible, login works)

---

## 6. Encryption Key Rotation

### Setup
- [ ] Create Repositories and AuthProviders with credentials, verify they work
- [ ] Query DB and note the ciphertext format — it should look like `enc:v1:default:<base64>` (the `default` part is the key ID)

### Generate a new key

The encryption key is a base64-encoded 32-byte AES-256 key. Generate one with:

```bash
openssl rand -base64 32 > /tmp/key2
```

### Add the new key

**Quadlet (Podman):**

The existing key file is at `/etc/flightctl/encryption/key` on the host. Copy the new key next to it:

```bash
sudo cp /tmp/key2 /etc/flightctl/encryption/key2
sudo chmod 600 /etc/flightctl/encryption/key2
```

**OpenShift (K8s):**

The existing key is in the `flightctl-encryption-key` Secret. Add the new key as a second data entry:

```bash
# Get the namespace (use the namespace where flightctl is deployed)
NS=flightctl

# Patch the secret to add key2 alongside the existing key
oc get secret flightctl-encryption-key -n $NS -o json \
  | jq --arg key2 "$(base64 < /tmp/key2)" '.data.key2 = $key2' \
  | oc apply -f -
```

Verify both keys are in the secret:

```bash
oc get secret flightctl-encryption-key -n $NS -o jsonpath='{.data}' | jq 'keys'
# Should show: ["key", "key2"]
```

### Update the service config

By default, the services use a single key with ID `default` at `/root/.flightctl/encryption/key` (this is built into the code — there's no explicit `encryption:` section in the config).

To rotate, you need to **add** an `encryption:` section to each service's ConfigMap that lists both the old and new keys, with the new key as active.

**Quadlet (Podman):** Edit each service's config YAML (e.g. `/etc/flightctl/config.yaml`) and add:

```yaml
encryption:
  keys:
    - id: default
      path: /etc/flightctl/encryption/key
    - id: rotated
      path: /etc/flightctl/encryption/key2
  activeKeyID: rotated
```

**OpenShift (K8s):** Edit each service's ConfigMap. For example, for the API server:

```bash
NS=flightctl
oc edit configmap flightctl-api-config -n $NS
```

Add the `encryption:` block at the top level of the `config.yaml` data:

```yaml
encryption:
  keys:
    - id: default
      path: /root/.flightctl/encryption/key
    - id: rotated
      path: /root/.flightctl/encryption/key2
  activeKeyID: rotated
```

Repeat for all service ConfigMaps: `flightctl-worker-config`, `flightctl-periodic-config`, `flightctl-imagebuilder-api-config`, `flightctl-imagebuilder-worker-config`, `flightctl-alert-exporter-config`, `flightctl-alertmanager-proxy-config`, `flightctl-remote-access-config`.

### Restart and verify
- [ ] Restart all services: flightctl-api, flightctl-worker, flightctl-periodic, flightctl-imagebuilder-api, flightctl-imagebuilder-worker, flightctl-alert-exporter, flightctl-alertmanager-proxy, flightctl-remote-access
- [ ] Verify existing resources still work (old data encrypted with key `default` still decrypts)
- [ ] Create a new Repository with credentials — verify it works
- [ ] Query DB — the new resource's ciphertext should now show `enc:v1:rotated:<base64>` instead of `enc:v1:default:<base64>`
- [ ] Update an existing Repository credential — query DB and verify the value changed from `enc:v1:default:...` to `enc:v1:rotated:...`
- [ ] Verify all flows work end-to-end after rotation

### Old key still required

While data encrypted with the old key still exists in the database, the old key **must** remain in the config. Removing it won't crash the services, but any operation that reads old-key data will fail at the point of use.

- [ ] Remove the old key (`default`) from the config — keep only the new key (`rotated`)
- [ ] Restart all services — verify they start successfully (startup does not fail)
- [ ] Try to access a Repository that was encrypted with the old key — verify it shows `Accessible=False` with an error
- [ ] Try to log in via an AuthProvider whose `clientSecret` was encrypted with the old key — verify login fails
- [ ] Restore the old key back into the config (add it back to the `keys` list, keep `activeKeyID: rotated`)
- [ ] Restart services — verify the Repository becomes `Accessible=True` again and login works

---

## 7. Backup and Restore

Test on both Quadlet and OpenShift:

- [ ] Set up a working environment with AuthProviders and Repositories that have credentials
- [ ] Verify all flows work (login, repo access, device rendering)
- [ ] Run `flightctl-backup`
- [ ] Run `flightctl-restore`
- [ ] Verify all services are up and running (api, worker, periodic, imagebuilder)
- [ ] Verify devices show "Awaiting Reconnect" state
- [ ] Reconnect a device — verify it gets its rendered config
- [ ] Verify login still works after restore
- [ ] Verify repository access still works after restore

---

## 8. Logs, Metrics, and Error Handling

### 8.1 Logs

- [ ] On service startup, check logs for `Encryption initialized: active=v1/default` — confirms encryption is active
- [ ] After key rotation, verify the log shows the new key ID (e.g. `active=v1/rotated`)
- [ ] Verify logs do **not** contain key material, plaintext, or full ciphertext values

### 8.2 Metrics

Encryption metrics are exposed by **flightctl-api** (port `15690`) and **flightctl-worker** (port `8080`). Metrics are enabled by default (`metrics.enabled: true` in config).

**Scrape metrics:**

Quadlet (from the host):
```bash
# API server metrics
curl -s http://localhost:15690/metrics | grep flightctl_encryption

# Worker metrics
curl -s http://localhost:8080/metrics | grep flightctl_encryption
```

OpenShift (port-forward into the pod):
```bash
# API server
oc port-forward deploy/flightctl-api -n $NS 15690:15690
curl -s http://localhost:15690/metrics | grep flightctl_encryption

# Worker
oc port-forward deploy/flightctl-worker -n $NS 8080:8080
curl -s http://localhost:8080/metrics | grep flightctl_encryption
```

**Expected metrics after performing encrypt/decrypt operations (e.g. creating or reading a Repository with credentials):**

| Metric | Description |
|--------|-------------|
| `flightctl_encryption_active_key_info{strategy="v1",key_id="default",algorithm="AES-256-GCM"}` | Should be `1` — confirms the active key |
| `flightctl_encryption_operations_total{operation="encrypt",strategy="v1",key_id="default",status="success"}` | Count of successful encryptions |
| `flightctl_encryption_operations_total{operation="decrypt",strategy="v1",key_id="default",status="success"}` | Count of successful decryptions |
| `flightctl_encryption_operation_duration_seconds` | Histogram of encrypt/decrypt latency |
| `flightctl_encryption_errors_total` | Should be `0` under normal operation |

- [ ] Scrape the API and worker metrics endpoints — verify the `flightctl_encryption_*` metrics appear
- [ ] Verify `flightctl_encryption_active_key_info` shows the correct active key ID
- [ ] Create a Repository with credentials and GET it back — verify `operations_total` counters increase for both `encrypt` and `decrypt`
- [ ] After key rotation, verify `active_key_info` shows the new key ID (e.g. `key_id="rotated"`)
- [ ] Verify `errors_total` is `0` under normal operation
- [ ] Trigger a decrypt error (e.g. remove a key from config) — verify `errors_total` increments

### 8.3 Error Handling

- [ ] Create a Repository with invalid credentials — verify a clear error message with no encryption internals leaked
- [ ] Try to start the API server without an encryption key configured — verify it fails clearly at startup
- [ ] Restore a backup to an environment with a different encryption key — verify errors are reported clearly
- [ ] Verify error messages do **not** expose plaintext, ciphertext, nonce, tag, or key material
