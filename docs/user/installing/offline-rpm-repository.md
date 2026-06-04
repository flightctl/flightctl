# Setting up a local RPM repository for offline installation

When you need to install Flight Control components on machines without internet access,
you must first create a local RPM repository on a connected machine and then transfer
it to the target environment.

This document covers two approaches:

- **Full repository mirror using `dnf reposync`** — copies the entire FlightCtl repository,
  including dependency metadata, so the target machine can resolve packages itself.
  Best for deploying to many machines or when you want a self-contained offline repo source.

- **Targeted download using `dnf download`** — downloads only the specified packages and
  their transitive dependencies. Best for one-off installs or when disk space is limited.

## Prerequisites

- A RHEL 9 or RHEL 10 prep machine with internet access
- `dnf` version 4 or later
- Sufficient disk space on the prep machine (full mirror: ~500 MB; targeted: ~50–200 MB
  depending on packages selected)
- `createrepo_c` (optional, for Method 2 — install with `sudo dnf install -y createrepo_c`)

## Adding the FlightCtl repository on the prep machine

Add the FlightCtl RPM repository so `dnf` can find the packages:

```bash
sudo dnf config-manager --add-repo https://rpm.flightctl.io/flightctl-epel.repo
```

Verify that the repository is enabled:

```bash
dnf repolist | grep flightctl
```

```console
flightctl    FlightCtl EPEL Repository
```

## Method 1: Mirroring the full repository with dnf reposync

`dnf reposync` downloads all packages in the FlightCtl repository along with their
`repodata/` metadata. The result is a complete offline repo that the target machine
can use as a normal `dnf` source — no internet required.

1. Create a local directory to hold the mirrored repository:

   ```bash
   mkdir -p ~/flightctl-repo
   ```

2. Mirror the FlightCtl repository:

   ```bash
   dnf reposync --repoid=flightctl --download-path=~/flightctl-repo --download-metadata
   ```

   > [!NOTE]
   > `--download-metadata` ensures that `repodata/` is included so the target machine
   > can use the directory as a proper dnf source without running `createrepo_c`.

3. Verify that repository metadata was downloaded:

   ```bash
   ls ~/flightctl-repo/flightctl/repodata/
   ```

   The directory should contain files such as `repomd.xml`, `primary.xml.gz`, and
   `filelists.xml.gz`.

4. Transfer the mirrored repository to the target machine using your preferred
   [portable media](offline-portable-media.md).

### Using the mirrored repository on the target machine

After transferring the repository, configure `dnf` to use it as a local source:

1. Create a repository configuration file:

   ```bash
   sudo tee /etc/yum.repos.d/flightctl-local.repo << 'EOF'
   [flightctl-local]
   name=FlightCtl Local Mirror
   baseurl=file:///home/<user>/flightctl-repo/flightctl
   enabled=1
   gpgcheck=0
   EOF
   ```

   Replace `<user>` with the actual user path where you transferred the repository.

2. Install the desired packages:

   ```bash
   sudo dnf install -y flightctl-agent flightctl-cli
   ```

   `dnf` resolves all dependencies from the local mirror without network access.

## Method 2: Downloading targeted packages with dnf download

`dnf download --resolve --alldeps` downloads only the specified packages and their
transitive runtime dependencies as individual `.rpm` files. This is faster and uses
less disk space than a full mirror, but all dependency resolution happens on the prep
machine, which must run the same RHEL version as the target.

> [!IMPORTANT]
> Always include `--alldeps` alongside `--resolve`. Without it, `dnf download` skips
> dependencies that are already installed on the prep machine. The target air-gapped
> machine may not have those packages, causing the install to fail with unresolved
> dependency errors.

1. Create a local directory to hold the downloaded RPMs:

   ```bash
   mkdir -p ~/flightctl-rpms
   ```

2. Download the agent and CLI packages with their dependencies:

   ```bash
   dnf download --resolve --alldeps --destdir ~/flightctl-rpms flightctl-agent flightctl-cli
   ```

   For a server deployment that uses Podman quadlets, use `flightctl-services` instead:

   ```bash
   dnf download --resolve --alldeps --destdir ~/flightctl-rpms flightctl-services
   ```

3. Verify the downloaded files:

   ```bash
   ls ~/flightctl-rpms/*.rpm
   ```

4. Transfer the directory to the target machine using your preferred
   [portable media](offline-portable-media.md).

### Installing from the downloaded RPMs on the target machine

The simplest approach is to install directly from the `.rpm` files:

```bash
sudo dnf install -y ~/flightctl-rpms/*.rpm
```

`dnf` resolves the installation order from the downloaded files. If the target machine
is missing any system dependencies that were not downloaded, the install fails. In that
case, re-run `dnf download --resolve --alldeps` on a prep machine with matching RHEL
version to capture any missing packages.

### Creating repository metadata (optional)

If you want the downloaded RPMs to be usable as a proper `dnf` repository source
(for example, to allow `dnf update` to work against the local mirror), generate
`repodata/` from the downloaded files:

```bash
createrepo_c ~/flightctl-rpms
```

Then create a repository configuration file and install as described in
[Method 1](#using-the-mirrored-repository-on-the-target-machine).

## Next steps

- [Packaging artifacts for portable media](offline-portable-media.md) — how to transfer
  the repository to an air-gapped machine
- [Installing the Flight Control agent offline on RHEL](installing-agent-offline.md) —
  end-to-end agent installation procedure using the local repository
