# Testing Flight Control Integration with Greenboot

## Motivation

[Integrating Flight Control with Greenboot](../../docs/user/installing/configuring-device-greenboot.md) allows for automatic software upgrade rollbacks in case of a failure.

The current document describes a few techniques for:
* Understanding how the flightctl-agent health check works
* Simulating software upgrade failures in a development environment

These guidelines can be used by developers for testing Flight Control integration with Greenboot in CI/CD pipelines.

## Prerequisites

Create an agent VM with greenboot installed:

```bash
make agent-vm
```

Connect to the VM console:

```bash
sudo virsh console flightctl-device-default
```

To exit the console, press `Ctrl + ]`.

## Health Check Implementation

The script utilizes the `flightctl-agent health` command that is part of the `flightctl-agent` binary to verify the agent service is running properly.

The script starts by running sanity checks to verify that it is executed from the `root` account.

Then, it executes `flightctl-agent health` command with following options:
- `--timeout="${FLIGHTCTL_HEALTH_CHECK_TIMEOUT}s"` to override default 150s timeout value
- `--verbose` to increase verbosity of the output

Internally, `flightctl-agent health` performs a two-phase health check:

| Phase | Duration | Description |
|-------|----------|-------------|
| Phase 1 | Up to timeout | Wait for `flightctl-agent.service` to become active |
| Phase 2 | 60s | Monitor service stability (detect crash loops) |

The health checker verifies:
- The service unit file is **enabled**
- The service becomes **active** within the timeout
- The service **remains stable** for the stability window

### Testing

Run the health check manually without rebooting:

```bash
# Run the Go health checker directly
sudo flightctl-agent health --verbose

# Or run the greenboot script
sudo /usr/lib/greenboot/check/required.d/20_check_flightctl_agent.sh
```

View the health command help:

```bash
$ flightctl-agent health --help
Usage of health:
  Performs health checks on the flightctl-agent service.

Checks performed:
  - Service status (enabled/active)
  - Reports agent's self-reported connectivity status (informational)

Exit codes:
  0  Service is active
  1  Service check failed

  -timeout duration
        Maximum time to wait for checks. (default 2m30s)
  -verbose
        Print detailed check results.
```

## Agent Service Failure

To simulate a situation where the agent service fails after an upgrade, disable the service and reboot.

```bash
sudo systemctl disable flightctl-agent
sudo reboot
```

Examine the system log to monitor the upgrade verification procedure that will fail and reboot a few times before rolling back into the previous state. Note the values of `boot_success` and `boot_counter` GRUB variables:

```
$ sudo journalctl -o cat -u greenboot-healthcheck.service
...
Running Required Health Check Scripts...
[20_check_flightctl_agent.sh] INFO: === flightctl-agent greenboot health check started ===
[20_check_flightctl_agent.sh] INFO: GRUB boot variables:
boot_success=0
boot_counter=2
...
time="..." level=debug msg="health: Waiting for flightctl-agent.service to become active (timeout: 2m30s)..."
time="..." level=error msg="health: Service check failed: service is not enabled (state: disabled)"
[20_check_flightctl_agent.sh] ERROR: flightctl-agent health check failed
...
```

> Run `sudo journalctl -o cat -u redboot-task-runner.service` to see pre-rollback script output.

After a few failed verification attempts, the system should rollback into the previous state. Use `bootc` to verify the current deployment:

```
$ bootc status
● Booted
  Image: quay.io/flightctl/flightctl-device:v1
  Digest: sha256:abc123...

● Rollback
  Image: quay.io/flightctl/flightctl-device:v2
  Digest: sha256:def456...
```

Finish by re-enabling the agent:

```bash
sudo systemctl enable flightctl-agent
```

## Agent Crash During Stability Window

To simulate a situation where the agent crashes during the 60-second stability window, create a broken OS image that causes the agent to fail after starting.

See `test/scripts/agent-images/` for example broken image configurations that can be used to build a test image.

Examine the system log to monitor the upgrade verification procedure that will fail during the stability monitoring phase:

```
$ sudo journalctl -o cat -u greenboot-healthcheck.service
...
Running Required Health Check Scripts...
[20_check_flightctl_agent.sh] INFO: === flightctl-agent greenboot health check started ===
[20_check_flightctl_agent.sh] INFO: GRUB boot variables:
boot_success=0
boot_counter=2
...
time="..." level=debug msg="health: Service is active"
time="..." level=debug msg="health: Monitoring service stability for 1m0s..."
time="..." level=debug msg="health: Service stable, 55s remaining..."
...
time="..." level=error msg="health: Stability check failed: service failed during stability window"
[20_check_flightctl_agent.sh] ERROR: flightctl-agent health check failed
...
```

> Run `sudo journalctl -o cat -u redboot-task-runner.service` to see pre-rollback script output.

After a few failed verification attempts, the system should rollback into the previous state. Use `bootc` to verify the current deployment:

```bash
$ bootc status
```
