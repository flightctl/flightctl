# Installing the Flight Control CLI

To install `flightctl` on Linux, macOS and Windows operating systems you have to download and install the `flightctl` binary file.

## Download archive

There are two primary ways to download the CLI archive:

1. **From the web console**

   1. Click the **❔** (question mark) icon in the upper part of the web console page and select **Command Line Tools** to be taken to the CLI downloads page.
   2. Choose a downloadable archive using the proper architecture and OS, then click the file to download it.

2. **From the CLI artifacts service**

   You can download the CLI directly from the CLI artifacts service running on port `8090`.

   > **Note:** Podman/Quadlet and OpenShift deployments use HTTPS, while Kubernetes deployments without TLS use HTTP.

   - The available archives and checksums are listed in a JSON index at the root:

     ```bash
     curl -k "https://<baseDomain>:8090"
     ```

   - Each artifact entry includes `os`, `arch`, and `filename`. The download URL pattern is:

     ```text
     https://<baseDomain>:8090/<arch>/<os>/<filename>
     ```

   - For example, to download the Linux `arm64` archive:

     ```bash
     curl -k -O "https://<baseDomain>:8090/arm64/linux/flightctl-linux-arm64.tar.gz"
     ```

## Installation

1. Install `flightctl`

- For **Linux** OS

  - Decompress the archive file

    ```shell
    tar -xvf <flightctl-os-arch>.tar.gz
    ```

  - Run the following command to make the `flightctl` binary executable

    ```shell
    chmod +x flightctl
    ```

  - Move the `flightctl` binary to a directory in your `PATH` environment variable. Usually it would be `/usr/local/bin` or `~/bin`.

    Don't forget to update `PATH` in the case you want to move the binary to another directory.

    To check `PATH` use:

    ```shell
    echo $PATH
    ```

- For **Windows** OS

  - Decompress the archive file

  - Move the `flightctl.exe` binary to a directory in your `PATH` environment variable. Usually it would be `C:\Program Files\flightctl\`.

    Don't forget to update `PATH` in the case you want to move it to another directory.

    To check `PATH` use:

    ```shell
    path
    ```

- For **macOS**

  - Decompress the archive file

  - Move the `flightctl` binary to a directory in your `PATH` environment variable. Usually it would be `/usr/local/bin` or `~/bin`.

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

- [Using the CLI](../using/cli/overview.md) - CLI usage guide
- [Logging in to the Service](../using/cli/logging-in.md) - How to authenticate to Flight Control
- [Configuring Authentication](configuring-auth/overview.md) - Learn about authentication methods
