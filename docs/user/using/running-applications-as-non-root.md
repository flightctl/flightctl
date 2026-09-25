# Running applications as non-root

When `runAs` is set to a non-root user, Flight Control runs the application with that user's rootless Podman and systemd instances. Configure the user and the host before deploying the application.

If `runAs` is omitted or set to `root`, the rootless prerequisites in this topic do not apply.

## Configure the application user

The `runAs` user must meet the following requirements:

- The user account exists on the device.
- The user has a home directory that is writable by that user.
- Lingering is enabled so the user's systemd instance runs without an interactive login:

  ```console
  sudo loginctl enable-linger flightctl
  ```

  Replace `flightctl` with the user specified by `runAs`.
- The user has subordinate UID and GID ranges in `/etc/subuid` and `/etc/subgid`. Configure these ranges before the first rootless Podman invocation for the user. See the [Podman rootless mode documentation](https://docs.podman.io/en/latest/markdown/podman.1.html#rootless-mode) for the required format and range selection.
- The `newuidmap` and `newgidmap` commands are installed. On Fedora and RHEL, these commands are provided by the `shadow-utils` package.

For example, verify the account and rootless prerequisites for the default `flightctl` user:

```console
run_as_user=flightctl
getent passwd "${run_as_user}"
grep "^${run_as_user}:" /etc/subuid /etc/subgid
command -v newuidmap newgidmap
```

The `flightctl-agent` package creates the `flightctl` user, its home directory, and enables lingering. Verify the configuration after installation. If you use a different user, create the account and configure its home directory, lingering, and subordinate ID ranges before deploying the application.

## Configure systemd cgroup delegation

Rootless applications that use CPU or memory limits, and VM applications, require cgroup delegation to the user systemd instance. Create a systemd drop-in before deploying these applications:

```console
sudo mkdir -p /etc/systemd/system/user@.service.d
sudo tee /etc/systemd/system/user@.service.d/delegate.conf > /dev/null <<'EOF'
[Service]
Delegate=cpu cpuset memory pids
EOF
```

The delegated controllers must match the resources used by the application. For more information, see the [Podman guidance for rootless containers with resource limits](https://github.com/containers/podman/blob/main/troubleshooting.md#26-running-containers-with-resource-limits-fails-with-a-permissions-error).

## Reload systemd after host configuration changes

After adding or changing the `user@.service` drop-in, reload the system manager and restart the affected user manager before deploying the application:

```console
sudo systemctl daemon-reload
```

```console
run_as_user=flightctl
run_as_uid="$(id -u "${run_as_user}")"
sudo systemctl restart "user@${run_as_uid}.service"
```

Restarting the user manager stops services owned by that user. Perform this step before deploying applications, or reboot the device instead.

If you manually change user-level Quadlet files, reload the user's systemd manager as well:

```console
sudo systemctl --user -M flightctl@ daemon-reload
```

Replace `flightctl` with the `runAs` user. Flight Control performs the user-manager reload when it deploys or updates application units.
