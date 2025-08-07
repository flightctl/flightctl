# Installing the Flight Control CLI

To install `flightctl` on Linux, macOS and Windows operating systems you have to download and install the `flightctl` binary file.

## Download archive

1. Click the **❔** (question mark) icon in the upper part of the web console page and select "Command Line Tools" to be taken to the CLI downloads page.

2. Choose a downloadable archive using proper architecture and OS. Click the file to download it.

## Installation

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
