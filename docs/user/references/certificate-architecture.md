# Flight Control Certificate Architecture

Flight Control uses X.509 certificates with mTLS for all agent-service communication. All certificates use ECDSA P-256/SHA-256 and require TLS 1.3+.

## Certificate Chain of Trust

```text
Flight Control Root CA (10yr)
├── Server Certificates (2yr)
├── Client-Signer CA (10yr, intermediate)
│   └── Client Certificates (7d - 1yr)
└── PAM Issuer Token Signer CA (10yr, intermediate)
```

For TPM attestation certificates, see [Configuring Device Attestation](../installing/configuring-device-attestation.md).

## Certificate Matrix

| Certificate                   | Purpose                  | Validity | Signed By                |
| ----------------------------- | ------------------------ | -------- | ------------------------ |
| Flight Control Root CA        | Root of trust            | 10 years | Auto generated (self-signed) |
| Client-Signer CA              | Signs client certs       | 10 years | Root CA                  |
| [API Server](../../../internal/crypto/signer/signer_server_svc.go) | API TLS                  | 2 years  | Root CA                  |
| [Telemetry Gateway](../../../internal/crypto/signer/signer_server_svc.go)             | Metrics TLS              | 2 years  | Root CA                  |
| [Alertmanager Proxy](../../../internal/crypto/signer/signer_server_svc.go)            | Alerts TLS               | 2 years  | Root CA                  |
| [Device Enrollment](../../../internal/crypto/signer/signer_device_enrollment.go)                    | Device enrollment        | 1 year   | Client-Signer CA         |
| [Device Management](../../../internal/crypto/signer/signer_device_management.go)             | Device operations        | 1 year   | Client-Signer CA         |
| [Device Services](../../../internal/crypto/signer/signer_device_svc_client.go)    | Device services    | 1 year   | Client-Signer CA |
| UI Server *                   | UI TLS                   | 2 years  | Root CA                  |
| CLI Artifacts Server *        | CLI Artifacts TLS        | 2 years  | Root CA                  |
| PAM Issuer Token Signer CA *  | Signs JWT tokens         | 10 years | Root CA                  |
| PAM Issuer Server *           | PAM Issuer TLS           | 2 years  | Root CA                  |

\* on Linux only

## File Locations

### Kubernetes/OpenShift

| Secret                                      | Contents                |
| ------------------------------------------- | ----------------------- |
| `flightctl-ca`                            | Root CA                 |
| `flightctl-client-signer-ca`              | Intermediate CA         |
| `flightctl-api-server-tls`                | API server cert         |
| `flightctl-telemetry-gateway-server-tls`  | Telemetry gateway cert  |
| `flightctl-alertmanager-proxy-server-tls` | Alertmanager proxy cert |
| `flightctl-ca-bundle`                     | CA bundle               |

Generation controlled by `global.generateCertificates` in Helm `values.yaml`: `auto`, `cert-manager`, `builtin`, or `none`

### Linux

| Path                                                      | Contents                        |
| --------------------------------------------------------- | ------------------------------- |
| `/etc/flightctl/pki/ca.crt`                             | Root CA                         |
| `/etc/flightctl/pki/ca.key`                             | Root CA key                     |
| `/etc/flightctl/pki/ca-bundle.crt`                      | CA bundle                       |
| `/etc/flightctl/pki/flightctl-api/client-signer.crt`    | Client-Signer CA                |
| `/etc/flightctl/pki/flightctl-api/client-signer.key`    | Client-Signer CA key            |
| `/etc/flightctl/pki/flightctl-api/server.crt`           | API server cert                 |
| `/etc/flightctl/pki/flightctl-api/server.key`           | API server key                  |
| `/etc/flightctl/pki/flightctl-ui/server.crt`            | UI server cert                  |
| `/etc/flightctl/pki/flightctl-ui/server.key`            | UI server key                   |
| `/etc/flightctl/pki/flightctl-cli-artifacts/server.crt` | CLI Artifacts server cert       |
| `/etc/flightctl/pki/flightctl-cli-artifacts/server.key` | CLI Artifacts server key        |
| `/etc/flightctl/pki/flightctl-pam-issuer/token-signer.crt` | PAM Issuer Token Signer CA   |
| `/etc/flightctl/pki/flightctl-pam-issuer/token-signer.key` | PAM Issuer Token Signer CA key |
| `/etc/flightctl/pki/flightctl-pam-issuer/server.crt`   | PAM Issuer server cert          |
| `/etc/flightctl/pki/flightctl-pam-issuer/server.key`   | PAM Issuer server key           |

> [!NOTE]
> In standalone deployments, the API server certificate is shared across services. The telemetry gateway uses a separate installation process; see [Standalone Observability](../using/standalone-observability.md) for details.

Generation controlled by `global.generateCertificates` in `service-config.yaml`: `builtin` or `none`

Certificates are auto-generated on first start when using `builtin`. To use your own CA, see [Custom Certificates](../installing/installing-service-on-linux.md#custom-certificates).

### Agent

Agent certificates can be provided via file paths or embedded directly in the config as PEM-encoded data.

**Default file locations:**

| Path                                               | Contents                                            |
| -------------------------------------------------- | --------------------------------------------------- |
| `/etc/flightctl/certs/ca.crt`                    | CA bundle (trusted CAs)                             |
| `/var/lib/flightctl/certs/client-enrollment.crt` | Enrollment certificate                              |
| `/var/lib/flightctl/certs/client-enrollment.key` | Enrollment key                                      |
| `/var/lib/flightctl/certs/agent.crt`             | Device management cert (generated after enrollment) |
| `/var/lib/flightctl/certs/agent.key`             | Device management key                               |

**Embedded certificates in config.yaml:**

Certificates can be embedded directly in the agent config. Use the CLI to generate a config with embedded enrollment credentials:

```bash
flightctl certificate request --signer=enrollment --expiration=365d --output=embedded > config.yaml
```

See [Provisioning Devices](../using/provisioning-devices.md) for details on using this with cloud-init.

## Configuration Overrides

Default certificate validity periods can be overridden:

**Enrollment certificates** - Use `--expiration` flag when requesting:

```bash
flightctl certificate request --signer=enrollment --expiration=30d --output=embedded > config.yaml
```

**Server certificates** - Configure in service config (`ca` section):

| Setting                      | Default  | Description                    |
| ---------------------------- | -------- | ------------------------------ |
| `certValidityDays`           | 3650     | CA certificate validity        |
| `clientBootStrapValidityDays`| 365      | Enrollment certificate validity|
| `serverCertValidityDays`     | 365      | Server certificate validity    |

## Certificate Rotation

> [!IMPORTANT]
> Certificates are **not** automatically rotated. Administrators must track expiration dates and manually renew certificates or re-enroll devices before they expire.

## Backup and Recovery

> [!NOTE]
> CA private keys (`ca.key`, `client-signer.key`) are critical for disaster recovery. If lost, all issued certificates become unverifiable and devices must be re-enrolled. Include certificate directories in your backup strategy alongside database backups.

For database backup procedures, see [Performing Database Backup](../installing/performing-database-backup.md).

## See Also

* [Security Guidelines](security-guidelines.md)
* [Installing the Agent](../installing/installing-agent.md)
* [Installing on Kubernetes](../installing/installing-service-on-kubernetes.md)
* [Installing on Linux](../installing/installing-service-on-linux.md)
* [Device Observability](../using/device-observability.md)
