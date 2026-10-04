# Using enrollment hooks

Use enrollment hooks to prepare devices for management and connect enrollment to external services. For example, a hook can discover device labels before enrollment or configure network access after approval.

## Choosing an enrollment hook

The agent supports two enrollment hooks:

| Hook | When it runs | Example use |
|------|--------------|-------------|
| `BeforeEnrolling` | Before the agent submits an enrollment request | Discover hardware and add device labels |
| `AfterEnrolling` | After the enrollment request is approved | Configure access to services before the device receives managed configuration |

`AfterEnrolling` hooks require an `EnrollmentHookPolicy` in the organization when enrollment is approved. Without a policy, the agent skips these hooks and starts normal device management.

When a policy is present, Flight Control completes any configured webhook notifications before the agent runs `AfterEnrolling` hooks. Fleet matching and configuration delivery wait for the hooks to complete or for the configured failure policy to allow enrollment to continue.

## Before you begin

- Use Flight Control service and agent version 1.4 or later. Upgrade every agent that might enroll in the organization before creating an enrollment hook policy.
- Prepare the hook commands and rule files before enrolling devices. For image-based devices, include them when [building the OS image](../building/building-images.md).
- Log in to the Flight Control CLI as an administrator or operator to manage enrollment hook policies.

## Configuring hooks on the device

Install the commands and their dependencies on the device. Define the actions in YAML files in the following directories:

| Hook | Rule directories |
|------|------------------|
| `BeforeEnrolling` | `/usr/lib/flightctl/hooks.d/beforeenrolling/` and `/etc/flightctl/hooks.d/beforeenrolling/` |
| `AfterEnrolling` | `/usr/lib/flightctl/hooks.d/afterenrolling/` |

Files must use the `.yaml` extension and run in alphabetical order by file name. For `BeforeEnrolling`, a file under `/etc` replaces a file with the same name under `/usr/lib`. `AfterEnrolling` has no `/etc` override directory.

Each file contains a list of actions. For example, install an executable setup script at `/usr/local/bin/configure-vpn.sh` and save this rule as `/usr/lib/flightctl/hooks.d/afterenrolling/10-setup.yaml`:

```yaml
- run: /usr/local/bin/configure-vpn.sh
  timeout: 60s
```

Actions run in order. If one fails, the remaining actions do not run. Use `timeout` to limit how long an individual action can run.

By default, a failed `BeforeEnrolling` hook still allows the agent to submit the enrollment request with an unsuccessful hook result. To require success before submitting the request, set the following in `/etc/flightctl/config.yaml`:

```yaml
enrollment:
  preEnrollment:
    failurePolicy: Block
```

With `Block`, the agent retries failed hooks with backoff. The default value is `Continue`.

## Enabling hooks after approval

Each organization can have one enrollment hook policy, named `default`. It applies to every subsequent enrollment approval in that organization and cannot target an individual fleet.

1. Save the following policy as `enrollment-hooks.yaml`:

   ```yaml
   apiVersion: flightctl.io/v1beta1
   kind: EnrollmentHookPolicy
   metadata:
     name: default
   spec:
     afterEnrolling:
       failurePolicy: Block
   ```

2. Apply the policy:

   ```console
   flightctl apply -f enrollment-hooks.yaml
   ```

`Block` is the default failure policy for `AfterEnrolling` hooks and webhook delivery. It prevents device management from starting if a webhook or device hook fails. Set it to `Continue` to allow device management despite those failures.

Flight Control saves the policy settings when it approves enrollment. Changing or deleting the policy later does not change those settings for devices already approved. Hook commands and rule files remain on the device; the policy does not distribute them.

For command syntax, flags, and examples to inspect, edit, or delete the policy, see [Managing enrollment hook policies](../references/cli-commands.md#managing-enrollment-hook-policies).

## Notifying external services

You can notify an external service when enrollment is approved. Add HTTPS actions to `spec.afterEnrolling.controlPlaneActions` in the policy, for example:

```yaml
spec:
  afterEnrolling:
    failurePolicy: Block
    controlPlaneActions:
      - url: https://hooks.example.com/enrollment
        timeout: 30s
        auth:
          bearerToken: "<bearer_token>"
```

Replace `<bearer_token>` with the credential for your endpoint, or omit `auth` if it does not require a bearer token. Apply the updated policy before approving the devices that should use it.

Flight Control sends an `EnrollmentApproved` JSON payload with `apiVersion`, `kind`, and `deviceName`. It also includes `labels` and `certificateSerial` when available. The endpoint must return a `2xx` status code to confirm delivery.

Use the `X-Flightctl-Delivery-Id` header to prevent duplicate processing of requests. The `X-Flightctl-Delivery-Attempt` header identifies the delivery attempt. By default, delivery uses up to five attempts with exponential backoff and a deadline of 10 minutes.

Actions run in order and stop at the first failed delivery. With `Block`, a delivery failure prevents device hooks from running. With `Continue`, the agent can still run device hooks after a delivery failure.

For timeout and retry options, see the [enrollment hook policy API schema](../../../api/core/v1beta1/openapi.yaml).

## Using device information in hooks

Commands can read JSON device information from `/run/flightctl/hook-context.json` or the `FLIGHTCTL_HOOK_CONTEXT` environment variable. The file is readable only by its owner. It contains the hook name, device name, system information, and labels. After approval, it also includes management certificate metadata when available, such as expiry and fingerprint, without private key material.

To choose an action based on a label, read this context inside your command. Hook YAML conditions do not support templates that access enrollment labels.

`BeforeEnrolling` commands can add labels by writing a JSON object of string values to `/run/flightctl/hook-labels.json`. The file can be at most 16,384 bytes. The agent includes these labels in the enrollment request.

Obtain any required credentials from your secret service. Keep credentials out of command output: the agent records `BeforeEnrolling` command output in the enrollment request.

## Checking enrollment progress

Inspect a device with the CLI:

```console
flightctl get device/<device_name> -o yaml
```

Find the `EnrollmentHooks` entry in `status.conditions`. While its status is `False`, the device remains excluded from fleet matching and configuration delivery.

| Reason | Meaning |
|--------|---------|
| `NotifyPending` | Flight Control is delivering webhook notifications |
| `Pending` | The agent can run device hooks, including when a notification failure was allowed by `Continue` |
| `Failed` | A failure blocked enrollment; an operator must resolve it |
| `Succeeded` | Device hooks completed successfully |
| `Continued` | Device hooks failed and the policy allowed enrollment to continue |
| `ManualOverride` | An operator cleared a blocked enrollment |

## Handling failures and restarts

Make hook commands safe to run more than once and able to recover from partial completion. If the agent restarts before Flight Control records the result, the condition can remain `Pending` and the agent runs the hooks again.

Once the result is recorded as `Succeeded`, `Continued`, or `ManualOverride`, the agent skips the hooks on restart. A recorded `Failed` result blocks device management; restarting the agent does not rerun the hooks.

Investigate the failed command or webhook before clearing a failure. An authorized administrator or operator can [override a failed enrollment hook](managing-devices.md#overriding-a-failed-enrollment-hook). The override allows device management to start without rerunning the failed hooks or webhook.

## Further information

- [Enrolling devices](managing-devices.md#enrolling-devices)
- [Device lifecycle hook rules](managing-devices.md#using-device-lifecycle-hooks)
- [Installing and configuring the agent](../installing/installing-agent.md)
- [Enrollment hook architecture](../../developer/architecture/enrollment-hooks.md)
