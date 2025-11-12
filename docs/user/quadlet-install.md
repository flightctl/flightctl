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
dnf config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo
dnf install -y flightctl-services
```

Install with dnf 5:

```bash
sudo dnf config-manager addrepo --from-repofile=https://rpm.flightctl.io/flightctl-epel.repo
dnf install -y flightctl-services
```

### Installing a specific version

Search for available versions:

```bashss
dnf list --showduplicates flightctl-services
```

Install a specific version by appending the desired version to the package name:

```bash
dnf install flightctl-services-0.9.4-1.fc42
```

## Quickstart

To spin up services quickly for testing or development purposes, services can be started and spun up without authentication and with self-signed certificates.

Services can be started by running a single .target file that specifies all required Flight Control services

```bash
sudo systemctl start flightctl.target
```

Services can be monitored by checking systemd units

```bash
sudo systemctl list-units flightctl-*.service
```

Or podman

```bash
sudo podman ps
```

Once the UI service has spun up, find the automatically set baseDomain

```bash
grep baseDomain /etc/flightctl/service-config.yaml
```

And visit the UI at https://<baseDomain>

## Configuring Services

Service configuration is largely managed by a file installed at `/etc/flightctl/service-config.yaml`.  The service config file is a unified location to update configuration that is then propagated to underlying services.

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

Certs are generated and stored in the `/etc/flightctl/pki` directory. These include:

```bash
/etc/flightctl/pki/ca.crt
/etc/flightctl/pki/ca.key
/etc/flightctl/pki/client-enrollment.crt
/etc/flightctl/pki/client-enrollment.key
/etc/flightctl/pki/server.crt
/etc/flightctl/pki/server.key
```

The `server.crt` and `server.key` are self-signed and automatically generated unless otherwise specified.  To use custom certificates, replace (or populate before first starting the services) `server.crt` and `server.key` files.

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

### Podman Artifacts Cleanup

As part of running the services a number of podman artifacts are created - including things like pulled images, volumes, secrets, and networks.  They are not directly managed by the rpm install process and will remain even if the services are all stopped and the rpm uninstalled.  The rpm is shipped with a script to aid in cleaning up these artifacts.

> [!NOTE]
> Running the cleanup script is an irreversible destructive action that will result in data loss
> Never run the script while the Flight Control services are running.  Doing so will result in potentially unrecoverable states and some artifacts may be re-created by podman.

Run with a --dry-run flag to see what will be deleted:

```bash
/usr/bin/flightctl-services-cleanup --dry-run
```

Run the cleanup script - requires user confirmation to delete artifacts:

```bash
/usr/bin/flightctl-services-cleanup
```
