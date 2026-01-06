# Uninstalling on Linux

Before you uninstall Flight Control services, remove all data and podman artifacts using the cleanup script.

## Uninstalling Flight Control from an RPM package

### Prerequisites

You are logged in as an administrator with root (sudo) access.

### Procedure

1. Clean all data and podman artifacts by running the following command:

   ```bash
   sudo /usr/bin/flightctl-standalone cleanup
   ```

   This script performs the following clean up actions:

   - Stops and disables `flightctl.target`
   - Remove Flight Control owned podman containers, images, volumes, secrets, and networks

2. Remove installed RPMs

   ```bash
   sudo dnf remove "flightctl-*"
   ```

3. Remove leftover configuration

   ```bash
   sudo rm -f /etc/flightctl
   ```
