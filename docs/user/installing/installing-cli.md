# Installing the Flight Control CLI

To install `flightctl` on Linux, macOS and Windows operating systems you have to download and install the `flightctl` binary file.

## Download archive

The Command Line Tools page offers two CLI tools:

- **flightctl** — main CLI for managing Flight Control fleets, devices, and resources
- **flightctl-restore** — utility for preparing devices after [database restoration](performing-database-restore.md)

1. Click the **❔** (question mark) icon in the upper part of the web console page and select "Command Line Tools" to be taken to the CLI downloads page.

2. Choose the CLI (flightctl or flightctl-restore), then pick the archive for your OS and architecture. Click the file to download it.

## Download from the CLI artifacts service (without the web console)

You can download the CLI directly from the CLI artifacts service.

**Note:** Set `<CLI_ARTIFACTS_URL>` based on your deployment:

- Standalone (Quadlet/Podman): `https://BASE_DOMAIN:8090`
- OpenShift (Route): `https://cli-artifacts.BASE_DOMAIN`
- Kubernetes/OpenShift (node port): `http://BASE_DOMAIN:PORT` or `https://BASE_DOMAIN:PORT`

The service root returns a JSON index listing the available archives and checksum files:

  ```bash
  curl -k "<CLI_ARTIFACTS_URL>/"
  ```

Each artifact entry includes `os`, `arch`, and `filename`. The download URL pattern is:

  ```text
  <CLI_ARTIFACTS_URL>/<arch>/<os>/<filename>
  ```

For example, to download the Linux `arm64` archive:

  ```bash
  curl -k -O "<CLI_ARTIFACTS_URL>/arm64/linux/flightctl-linux-arm64.tar.gz"
  ```

## Installation

The steps below apply to `flightctl`; for `flightctl-restore`, use the same steps with the `flightctl-restore` binary (or `flightctl-restore.exe` on Windows).

1. Install `flightctl`

* For **Linux** OS

  * Decompress the archive file

    ```shell
    tar -xvf <flightctl-os-arch>.tar.gz
    ```

  * Run the following command to make the `flightctl` binary executable

    ```shell
    chmod +x flightctl
    ```

  * Move the `flightctl` binary to a directory in your `PATH` environment variable. Usually it would be `/usr/local/bin` or `~/bin`.

    Don't forget to update `PATH` in the case you want to move the binary to another directory.

    To check `PATH` use:

    ```shell
    echo $PATH
    ```

  * For **Windows** OS

    * Decompress the archive file

    * Move the `flightctl.exe` binary to a directory in your `PATH` environment variable. Usually it would be `C:\Program Files\flightctl\`.

      Don't forget to update `PATH` in the case you want to move it to another directory.

      To check `PATH` use:

      ```shell
      path
      ```

  * For **macOS**

    * Decompress the archive file

    * Move the `flightctl` binary to a directory in your `PATH` environment variable. Usually it would be `/usr/local/bin` or `~/bin`.

      Don't forget to update `PATH` in the case you want to move the binary to another directory.

      To check `PATH` use:

      ```shell
      echo $PATH
      ```

## Verification

After installation, verify that `flightctl` is working correctly:

```shell
flightctl version
```

> Note: If you get a "command not found" error, ensure the binary is in your `PATH`.

## Next Steps

After installing the CLI, you'll need to authenticate to the Flight Control service:

* [Using the CLI](../using/cli/overview.md) - CLI usage guide
* [Logging in to the Service](../using/cli/logging-in.md) - How to authenticate to Flight Control
* [Configuring Authentication](configuring-auth/overview.md) - Learn about authentication methods
