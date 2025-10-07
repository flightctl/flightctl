# Flight Control service Quadlets

## Overview

Flight Control services can be deployed as systemd-managed container Quadlets, providing a declarative way to manage containerized services using native systemd tooling.

## What are Quadlets?

Quadlets are a systemd feature that allows you to define containerized services using systemd unit files. Instead of running containers manually with `podman run`, you define the container configuration in a `.container` file that systemd can manage directly.

A Quadlet file uses the systemd unit file format with container-specific directives:

```ini
[Unit]
Description=A containerized service

[Container]
ContainerName=some-service
Image=quay.io/some-org/some-service:1.0.0
```

When systemd processes this file, it automatically generates the appropriate service unit and manages the container lifecycle.

More info about systemd units using Podman Quadlet including definitions of fields available in the `.container` files can be found in the [Podman systemd documentation](https://docs.podman.io/en/latest/markdown/podman-systemd.unit.5.html)

> **Note**
> Flight Control Quadlets are configured to run rootful containers and rootless is not supported

## Flight Control Service Architecture

The Flight Control Quadlets are organized in the `deploy/podman/` directory with the following general structure:

```text
deploy/podman/
├── flightctl-api/                      # Directory for each service
│   ├── flightctl-api.container         # API service definition
│   ├── flightctl-api-init.container    # Init container to manage creating application configuration
│   └── flightctl-api-config/           # Configuration templates and scripts
...
├── flightctl.network                   # Container network definition
├── flightctl.target                    # Service group definition
└── service-config.yaml                 # Global configuration template
```

### Service Dependencies

Services are configured with proper startup ordering through systemd dependencies:

```ini
[Unit]
After=flightctl-db-wait.service flightctl-kv.service
Wants=flightctl-db-wait.service flightctl-kv.service
```

The dependency graph is configured between the individual `.container` files.

> **Note:** The `flightctl-db-wait.service` is preferred over depending directly on `flightctl-db.service` because it ensures that not only the database container is up, but also verifies the database is ready by running a basic query.

## Configuration Management

### Global Configuration

All services share a common configuration file `service-config.yaml`:

```yaml
global:
  baseDomain: example.com
  auth:
    type: none # aap, oidc or none
    insecureSkipTlsVerify: false
    aap:
      apiUrl: https://aap.example.com
      # ... other AAP settings
    oidc:
      oidcAuthority: https://oidc.example.com
      # ... other OIDC settings
```

### Service Configuration

Services have configuration requirements such as files or env variables they need to run.  The config can largely be broken down into two buckets:

1. Static configuration - Files that are the same for all instances of a service
2. Dynamic configuration - Files that must be rendered based upon installation specific state or user provided config

An example configuration directory for a service looks like:

```text
deploy/podman/
├── flightctl-service-name/                      # Directory for each service
│   └── flightctl-service-name-config/           # Configuration templates and scripts
|       ├── env.template                         # Template for env variables the service needs
|       ├── config.ini                           # Static .ini file for configuring the service
|       └── init.sh                              # Script that is run by the init-container

```

#### Static Configuration

Services such as `flightctl-kv` can have static configuration files, though the current implementation generates Redis configuration dynamically using environment variables within the container.

#### Dynamic Configuration

Many services use init containers to template configuration files before the main service starts.  These init containers run configuration scripts, overlay values into rendered configuration the associated service needs, and are configured to run before the main service container starts.

For example `flightctl-api` makes use of an init container to load values from the global `service-config.yaml` file and apply them to a `config.yaml` and an env file for populating variables needed by the service.  The resulting configuration is consumed like:

```ini
# Load env variables written by the init service
EnvironmentFile=/etc/flightctl/flightctl-api/env
# Load config.yaml written by the init service
Volume=/etc/flightctl/flightctl-api/config.yaml:/root/.flightctl/config.yaml:ro,z
```

#### Secrets

A subset of sensitive data is managed through Podman secrets:

```ini
[Container]
Secret=flightctl-postgresql-master-password,type=env,target=DB_PASSWORD
```

Secrets are automatically generated during deployment and injected as environment variables to the running containers.

## Local Deployment

Deploy all services:

```bash
make deploy-quadlets
```

Deploy individual services:

```bash
make deploy-db
make deploy-kv
```

> **NOTE**
> Deploying individual services makes use of service-name-standalone.container files
> The -standalone files are handled as a special case and currently used for integration testing 

### Deployment Flow

At a high level running `make deploy-quadlets` performs the following steps:

1. Installs files in appropriate locations
2. Generates underlying systemd units and config by running `systemctl daemon-reload`
3. Starts a `flightctl.target` systemd target that spins up all the services

#### Installation

Install in the context of the `make deploy-quadlets` involves taking files found in the Flight Control repository and moving them to various directories on the host system.  The directories in question are:

1. A configuration directory intended for read-only use - namely predefined config files or scripts that should not be editable
    - `/usr/share/flightctl/`
2. A configuration directory intended for dynamic use and populated by init containers and containing application state generated by the running services.
    - `/etc/flightctl/`
3. A systemd directory where `.container` files should be placed so that systemd will generate the underlying units appropriately.
    - `/usr/share/containers/systemd/`
4. A systemd directory where native systemd (e.g. the `flightctl.target` file) should be placed
    - `/usr/lib/systemd/system`

### Cleanup

Remove all services and data:

```bash
make clean-quadlets
```

This stops services, removes configuration files, deletes volumes, and cleans up secrets.

## Service Management

### Starting/Stopping Services

```bash
# Start all services
sudo systemctl start flightctl.target

# Stop all services
sudo systemctl stop flightctl.target

# Restart individual service
sudo systemctl restart flightctl-api.service

# Check service status
sudo systemctl status flightctl-api.service

# List all flightctl units and their statuses
systemctl list-units 'flightctl*' --all
```

### API Health Check

A basic API health check can be performed by calling `/readyz` via the API that will verify that the API is up and running and the connection with database and key-value store is established. A status 200 is returned when healthy.

```bash
# Using localhost
curl -fk https://localhost:3443/readyz && echo OK || echo FAIL

# Using domain from config file
DOMAIN="$(yq -r '.global.baseDomain // "localhost"' /etc/flightctl/service-config.yaml)"
curl -fk "https://${DOMAIN}:3443/readyz" && echo OK || echo FAIL
```

### Viewing Logs

```bash
# View API service logs
sudo journalctl -u flightctl-api.service -f

# View all Flight Control logs
sudo journalctl -u "flightctl-*" -f
```

The `--no-pager` option can also be helpful to wrap text depending on terminal settings.

### Container Management

```bash
# List running containers
sudo podman ps

# Execute command in container
sudo podman exec -it flightctl-api ls

# View container logs
sudo podman logs flightctl-api
```

## RPM Installation, Upgrade and Deployment

The service Quadlets are also available to install via an RPM.  Installation steps for the latest release:

```bash
sudo dnf config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo
sudo dnf install -y flightctl-services
sudo systemctl start flightctl.target
sudo systemctl enable flightctl.target # To enable starting on reboot
```

### Upgrading Flight Control Services

To upgrade Flight Control services via DNF:

```bash
# Update to the latest version
sudo dnf update flightctl-services

# Or upgrade using a specific RPM file
sudo rpm -Uvh flightctl-services-*.rpm
```

The RPM upgrade process includes:

1. **Pre-upgrade checks** - A database migration dry-run is performed to verify compatibility
2. **Service restart** - Flight Control services are automatically restarted with the new version
3. **Configuration preservation** - Existing configuration files are preserved during upgrade

> [!NOTE]
> Database migration dry-run can be enabled/disabled by editing `/etc/flightctl/flightctl-services-install.conf` and setting `FLIGHTCTL_MIGRATION_DRY_RUN=1`. This is recommended to catch potential migration issues before they affect production.

> [!NOTE] 
> Downgrades are not supported. Be sure to back up your system before upgrading. If an upgrade fails, follow the [Flight Control Restore Operations](restore.md#flight-control-restore-operations).

### Running the Services Container

A containerized approach is offered to run all services within a single container.  This is particularly useful for testing RPM builds and service integration without affecting the host system.

Start all Flight Control services in a container:

```bash
make run-services-container
```

This command:

- Automatically builds the container if it doesn't exist
- Runs the container with privileged access (required for systemd)
- Starts the `flightctl.target` systemd target inside the container


### Managing the Services Container

```bash
# Check if container is running
sudo podman ps | grep flightctl-services

# Access container shell, helpful in debugging the rpm install or service issues
sudo podman exec -it flightctl-services bash
# Then see what is running inside the container
sudo podman ps

# Stop and clean up container
make clean-services-container
```

## Contributing: Adding a New Quadlet

Follow these steps to add a new service to the Flight Control Quadlets:

### 1. Create Service Directory

```bash
mkdir -p deploy/podman/flightctl-myservice/flightctl-myservice-config
```

### 2. Create Container Quadlet

Create `deploy/podman/flightctl-myservice/flightctl-myservice.container`:

```ini
[Unit]
Description=Flight Control My Service
PartOf=flightctl.target
# Update After= and Requires= as needed for your needs
After=
Requires=

[Container]
ContainerName=flightctl-myservice
Image=quay.io/flightctl/flightctl-myservice:latest
Network=flightctl.network
# !Important!
# Because the containers are run using rootful podman host port definitions
# will by default will automatically bypass configured firewalls and expose the port to the outside world.
# See https://access.redhat.com/solutions/7081860
PublishPort=8080:8080

# Add Volume / Secret / EnvironmentFile definitions as needed
Volume=/etc/flightctl/flightctl-myservice/config.yaml:/root/.flightctl/config.yaml:ro,z
Secret=flightctl-postgresql-master-password,type=env,target=DB_PASSWORD
EnvironmentFile=/etc/flightctl/flightctl-myservice/env

[Service]
Restart=always
RestartSec=30

[Install]
WantedBy=flightctl.target
```

### 3. Setup Configuration (Optional)

#### Static Configuration

Add files to a `deploy/podman/flightctl-myservice/flightctl-myservice-config` directory.

#### Dynamic Configuration

Add a template to a `deploy/podman/flightctl-myservice/flightctl-myservice-config` directory.

**`deploy/podman/flightctl-myservice/flightctl-myservice-config/config.yaml.template`**:

```yaml
database:
  hostname: flightctl-db
  port: 5432
service:
  baseUrl: https://{{BASE_DOMAIN}}:8080/
```

Then write an `init.sh` file to also place in `deploy/podman/flightctl-myservice/flightctl-myservice-config`.
**`deploy/podman/flightctl-myservice/flightctl-myservice-config/init.sh`**:

```bash
#!/usr/bin/env bash
set -eo pipefail

source "/utils/init_utils.sh"

# Write service specific initialization logic as needed
```

Create `deploy/podman/flightctl-myservice/flightctl-myservice-init.container`:

```ini
[Unit]
PartOf=flightctl.target
After=flightctl-db.service
Wants=flightctl-db.service

[Container]
Image=registry.access.redhat.com/ubi9/ubi-minimal
ContainerName=flightctl-myservice-init
Network=flightctl.network
Volume=/usr/share/flightctl/flightctl-myservice:/config-source:ro,z
Volume=/usr/share/flightctl/init_utils.sh:/utils/init_utils.sh:ro,z
Volume=/etc/flightctl/flightctl-myservice:/config-destination:rw,z
Volume=/etc/flightctl/service-config.yaml:/service-config.yaml:ro,z
Exec=/bin/sh /config-source/init.sh

[Service]
Type=oneshot
RemainAfterExit=true
Restart=on-failure
RestartSec=5s

[Install]
WantedBy=flightctl.target
```

### 4. Add Volume (If Needed)

Create `deploy/podman/flightctl-myservice/flightctl-myservice.volume`:

```ini
[Volume]
VolumeName=flightctl-myservice
```

### 5. Update Install Scripting

Edit `deploy/scripts/install.sh` to include your service in the `render_files()` function:

```bash
render_files() {
    # ... existing services ...
    render_service "myservice" "${SOURCE_DIR}"
    # ... rest of function ...

    # If the service writes config ensure the location where those files are placed exists
    mkdir -p "${CONFIG_WRITEABLE_DIR}/flightctl-cli-artifacts"
}
```

Edit `deploy/scripts/deploy_quadlets.sh` so the polling waits for the new service to spin up.

If your service creates volume or other artifacts, edit `deploy/scripts/clean_quadlets.sh` so they are properly removed.

### 6. Update Target File

Edit `deploy/podman/flightctl.target` to include your service:

```ini
[Unit]
# ... existing content ...
Requires=flightctl-myservice.service

After=flightctl-myservice.service
```

### 7. Test Your Service

```bash
# Deploy and test
make deploy-quadlets

# Check service status
sudo systemctl status flightctl-myservice.service

# View logs
sudo journalctl -u flightctl-myservice.service -f --no-pager
```

After sufficient testing, move onto the RPM steps below.

## Contributing: Updating the RPM

When adding new Quadlets to Flight Control, the RPM spec file needs to be updated to include the new service files in the `flightctl-services` sub-package. The spec file handles packaging and installation of Quadlet files through the system package manager.

### 1. Update the %files services Section

Edit `packaging/rpm/flightctl.spec` to include your new service files. Add entries for both the configuration directory and any specific files:

```text
%files services
    # ... existing content ...
    
    # Add directory for dynamic config
    %dir %{_sysconfdir}/flightctl/flightctl-myservice
    
    # Add directory for read-only config
    %dir %attr(0444,root,root) %{_datadir}/flightctl/flightctl-myservice
    
    # Add specific config files (if any)
    %{_datadir}/flightctl/flightctl-myservice/config.yaml.template
    %{_datadir}/flightctl/flightctl-myservice/env.template
    %attr(0755,root,root) %{_datadir}/flightctl/flightctl-myservice/init.sh
    
    # Quadlet files are included via wildcard: %{_datadir}/containers/systemd/flightctl*
```

### 2. Test RPM Build

Build the RPM locally:

```bash
rm -rf bin/rpm
make build rpm
```

The above will place the built services rpm in `bin/rpm/flightctl-services-*`

The rpm can be installed on the host system, or preferably a Fedora / CentOS Stream / RHEL VM.

Install locally:

```bash
sudo dnf install -y bin/rpm/flightctl-services-*.rpm
```

Install in a VM:

```bash
# Sample command to move rpm file to VM
scp -r bin/rpm/flightctl-services-* user@192.168.122.172:/home/user

# ssh into VM

# Install RPM
cd /home/user
sudo dnf install flightctl-services-*
```

After installation services can be spun up by starting the `.target`:

```bash
sudo systemctl start flightctl.target
```
