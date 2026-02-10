# Integrating Flight Control with Greenboot

## Motivation

Serviceability of edge devices is often limited or non-existent, making it challenging to troubleshoot problems following a failed software or operating system upgrade.

To mitigate these problems, Flight Control integrates with [greenboot](https://github.com/fedora-iot/greenboot), the Generic Health Check Framework for `systemd` on `bootc` based systems. If a failure is detected, the system boots into the last known working configuration using `bootc` rollback facilities.

This reduces the risk of being locked out of an edge device when upgrades fail.

## How It Works

### Greenboot Configuration

Flight Control includes the `20_check_flightctl_agent.sh` health check script to validate that the agent service is running properly. The script is installed into `/usr/lib/greenboot/check/required.d` and calls `flightctl-agent health` to perform the checks.

The `40_flightctl_agent_pre_rollback.sh` pre-rollback script is installed into `/usr/lib/greenboot/red.d`, to be executed right before system rollback. It collects diagnostic information (service status, recent journal entries) for post-mortem analysis.

Greenboot redirects all script output to the system log:

* `journalctl -u greenboot-healthcheck.service` for health check logs
* `journalctl -u redboot-task-runner.service` for pre-rollback logs

### Agent Validation

Exiting the health check with a non-zero status declares the boot as failed. The following validations are performed:

| Validation | Pass | Fail |
|------------|------|------|
| Check script runs with root permissions | Next | exit 1 |
| Check `flightctl-agent.service` is enabled | Next | exit 1 |
| Wait for service to become active (up to 150s) | Next | exit 1 |
| Monitor service stability for 60 seconds | Next | exit 1 |

> If the system is not booted using `bootc`, the health check still runs, but no rollback is possible.

### Third-Party Health Check Management

Flight Control is designed to be the sole controller of OS rollback decisions. The `flightctl-configure-greenboot.service` runs before `greenboot-healthcheck.service` on every boot, automatically disabling third-party health checks (e.g., MicroShift) by setting `DISABLED_HEALTHCHECKS` in `/etc/greenboot/greenboot.conf`. Core greenboot scripts and Flight Control's own health checks are preserved.

> [!WARNING]
> Do not manually edit `DISABLED_HEALTHCHECKS` in `/etc/greenboot/greenboot.conf` it is replaced on every boot by `flightctl-configure-greenboot.service`.

## The `systemd` Journal Service Configuration

The default `systemd` journal configuration stores data in volatile `/run/log/journal`, which does not persist across reboots. To monitor greenboot activities across boots, enable persistent storage:

```bash
sudo mkdir -p /etc/systemd/journald.conf.d
cat <<EOF | sudo tee /etc/systemd/journald.conf.d/flightctl.conf &>/dev/null
[Journal]
Storage=persistent
SystemMaxUse=1G
RuntimeMaxUse=1G
EOF
```
