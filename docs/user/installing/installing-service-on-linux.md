# Flight Control quadlet-based installation

Containerized Flight Control services can be installed on a Fedora or RHEL host by running [Podman quadlet systemd units](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html).

## Installing the RPM

Services rpm files are hosted at [rpm.flightctl.io](https://rpm.flightctl.io/).  To install the latest release of flightctl-services enable the repo and install the rpm package.

Please note depending on your dnf version (4 or 5) the syntax for adding a repo changes:

Get dnf version:

```bash
dnf --version
```

Install with dnf 4:

```bash
sudo dnf config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo
sudo dnf install -y flightctl-services
```

Install with dnf 5:

```bash
sudo dnf config-manager addrepo --from-repofile=https://rpm.flightctl.io/flightctl-epel.repo
sudo dnf install -y flightctl-services
```

Flight Control services can be configured through a central configuration file located at `/etc/flightctl/service-config.yaml`.

To spin up services quickly for testing or development purposes, you can leave this file's defaults. This sets the base domain of the services to the host's fully qualified domain name (FQDN) (from `hostname -f`) and generates a self-signed certificate authority (CA) from which required certificates are issued.

For a production environment, set the base domain (`global.baseDomain`) to your own fully qualified domain name (FQDN) and configure certificates from your own PKI (see [Custom Certificates](#custom-certificates)).

You can then start the Flight Control services by running

```bash
sudo systemctl start flightctl.target
```

Monitor that all `systemd` services come up correctly by running

```bash
sudo systemctl list-units flightctl-*.service
```

or by checking the running containers with

```bash
sudo podman ps
```

## Configure Authentication

Before accessing Flight Control (via the UI or CLI), you need to configure authentication for the service. By default, the deployment includes an OIDC provider called [PAM Issuer](configuring-auth/auth-pam.md).

See the [Authentication Overview](configuring-auth/overview.md) for detailed information about available authentication methods and how to configure them for your deployment.

Once authentication is configured, you can access the UI at `https://BASE_DOMAIN` (where `BASE_DOMAIN` is what you configured in `global.baseDomain` or your hostname FQDN).

## Helpful Commands

### Service management and monitoring

Start all services

```bash
sudo systemctl start flightctl.target
```

Enable automatic restarts on reboot

```bash
sudo systemctl enable flightctl.target
```

Get systemd status of the .target

```bash
sudo systemctl status flightctl.target
```

Get systemd status of a specific service

```bash
sudo systemctl status flightctl-api.service --no-pager
```

View systemd logs for a specific service

```bash
sudo journalctl -u flightctl-api --no-pager
```

List service tree dependencies of the .target

```bash
systemctl list-dependencies flightctl.target
```

List related systemd units

```bash
sudo systemctl list-units "flightctl*"
```

Stop all services

```bash
sudo systemctl stop flightctl.target
```

### View generated Secrets

As a part of the service startup passwords are generated and stored as podman secrets.

View secrets

```bash
sudo podman secret ls | grep flightctl
```

View secret data (please note this outputs the secret in plain text)

```bash
sudo podman secret inspect flightctl-postgresql-user-password --showsecret | jq '.[] | .SecretData'
```

## Certificate Management

Required certificates are stored in the `/etc/flightctl/pki` directory. The certificate structure includes:

```bash
/etc/flightctl/pki/
├── ca.crt                                # Root CA certificate
├── ca.key                                # Root CA private key
├── ca-bundle.crt                         # CA bundle (ca.crt + client-signer.crt)
└── flightctl-api/
    ├── server.crt                        # API server TLS certificate
    ├── server.key                        # API server private key
    ├── client-signer.crt                 # Client certificate signing CA
    └── client-signer.key                 # Client signer private key
```

For general info on the certificate architecture, see [Certificate Architecture](../references/certificate-architecture.md).

### Automatic Certificate Generation

When `global.generateCertificates` is set to `builtin` certificates are generated with the following behavior:

- A self-signed root CA is created if not already present
- An intermediate client-signer CA is generated for managing client certificates
- The API server certificate is created with the configured `baseDomain` as a Subject Alternative Name (SAN)

> [!WARNING]
> Using builtin is a destructive action.  Doing so will result in previously issued certificates becoming unverifiable and devices must be re-enrolled.

### Custom Certificates

For production deployments or environments with existing PKI infrastructure, you can provide your own certificates instead of using automatically generated self-signed certificates.

#### Using all custom certificates

To use fully custom certificates, **all**  of the certificates specified above must be supplied or services will fail to start.

Ensure that the `global.generateCertificates` value in `service-config.yaml` is set to `none`

```yaml
global:
  generateCertificates: none
```

Populate `/etc/flightctl/pki` with the following certificates:

| File | Description |
|------|-------------|
| `ca.crt` | Root CA certificate |
| `ca.key` | Root CA private key |
| `flightctl-api/server.crt` | API server TLS certificate, signed by the root CA. **Must include a SAN** matching the API domain (must match the configured baseDomain) |
| `flightctl-api/server.key` | API server private key |
| `flightctl-api/client-signer.crt` | Intermediate CA for signing device/client certificates, signed by the root CA |
| `flightctl-api/client-signer.key` | Client signer CA private key |
| `ca-bundle.crt` | Concatenation of `ca.crt` + `client-signer.crt` |

Start the Services

```bash
sudo systemctl start flightctl.target
```

> [!NOTE]
> Services do not currently support automatic reloading of certificates and will need to be restarted to load new certificates.

#### Using an existing Certificate Authority

To use an existing CA instead of the automatically generated self-signed CA:

Ensure that the `global.generateCertificates` value in `service-config.yaml` is set to `builtin`

```yaml
global:
  generateCertificates: builtin
```

Place the existing CA key and cert files in the following locations, also ensuring they are readable:

- `/etc/flightctl/pki/ca.crt`
- `/etc/flightctl/pki/ca.key`

```bash
# BEFORE starting flightctl services, place your CA certificates
sudo cp your-ca.crt /etc/flightctl/pki/ca.crt
sudo cp your-ca.key /etc/flightctl/pki/ca.key
sudo chown root:root /etc/flightctl/pki/ca.*
sudo chmod 600 /etc/flightctl/pki/ca.key
sudo chmod 644 /etc/flightctl/pki/ca.crt
```

Start the services:

```bash
sudo systemctl start flightctl.target
```

The services will detect the existing CA certificates and use them to generate the intermediate client-signer CA and server certificates.

Set the `global.generateCertificates` value to none to prevent rewriting certificates in the future.

```yaml
global:
  generateCertificates: none
```

> [!WARNING]
> Using builtin is a destructive action. Doing so will result in previously issued certificates becoming unverifiable and devices must be re-enrolled.

### Authentication Provider CA

A custom CA certificate for use with configured authentication providers can be placed in the following location:

```bash
/etc/flightctl/pki/auth/ca.crt
```

## Troubleshooting

### Must-Gather Script

For troubleshooting and support purposes, the `flightctl-services-must-gather` script is available to collect comprehensive system information, logs, and configuration details.  This script is shipped in the rpm, and requires `sudo` privileges to run.

Run the must-gather script:

```bash
/usr/bin/flightctl-services-must-gather
```

The script will:

- Prompt for confirmation due to potentially large file generation
- Collect system information (OS, SELinux status, package versions)
- Gather systemd service status and logs from the last 24 hours
- Collect Podman container, image, volume, and network information
- Create a timestamped tarball with all collected data

The generated tarball (named `flightctl-services-must-gather-YYYYMMDD-HHMMSS.tgz`) contains all the diagnostic information and can be shared for troubleshooting assistance.
