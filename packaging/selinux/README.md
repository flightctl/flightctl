> **Note:** These steps are intended to be executed against a running `flightctl-agent` instance.
> This allows you to test workflows and iteratively refine the SELinux policy in a live environment.
> When developing the policy its important to not upgrade the OS image as the local overlay changes
> made via dnf install -y --transient <package-name> will be lost. Once you have a policy ready for
> upgrade testing copy the policy back to your repo commit and redeploy based on the directions below

# Developing the SELinux Policy for `flightctl-agent`

## Prerequisites

To create an agent VM, run:

```sh
make agent-vm
```
When redeploying, clean up the previous build first:

```sh
make clean-agent-vm
rm -rf bin/rpm
make agent-vm
```

Install required development tools on the agent:

```sh
sudo dnf install -y --transient policycoreutils-devel setools-console make audit rsync
```

## Copy Policy To Agent

Use your current policy as the base for development.

```
rsync -avz ./packaging/selinux/ user@<agent-ip>:~/selinux/
```

## Start Audit Logging
Enable and start `auditd` so policy denials are recorded:

```sh
sudo systemctl enable --now auditd
```

## Temporarily Set the Agent to Permissive Mode

To allow the agent to run without SELinux denials while you gather AVC logs or test workflows:

### Set permissive mode for the agent domain:

```sh
sudo semanage permissive -a flightctl_agent_t
```

### Revert to enforcing mode once policy is updated:

```sh
sudo semanage permissive -d flightctl_agent_t
```

## Reproduce and Inspect AVCs

Restart the service, perform actions against the agent then review recent denials and explanations:

```sh
sudo systemctl restart flightctl-agent
sudo ausearch -m avc -ts recent -c flightctl-agent | audit2allow
sudo ausearch -m avc -ts recent -c flightctl-agent | audit2why
```

## Build the Policy 

Use a containerized build on the agent to ensure we do not install
dependencies such as `selinux-policy-devel` which will not be available on the
agent included in the RPM.

```sh
make USE_CONTAINER=1
```

## Install the Policy

```sh
sudo make install-policy
```

## Ensure Selinux Policy Is Applied

After installing you should ensure that the policy is correctly applied.

```sh
ls -Z /usr/bin/flightctl-agent 
system_u:object_r:flightctl_agent_exec_t:s0 /usr/bin/flightctl-agent
```

## Important Areas Of Concern

The agent operates under a broad SELinux scope, so it's critical to test as many execution paths as possible. Key areas to focus on:

- Console: Ensure execution of subsystem commands (bootc status, podman ps, systemctl status) works as expected.

- Applications: Validate behavior for both declared and embedded applications, add and remove.

- Hooks: Test execution of declared and embedded lifecycle hooks.

## References

- https://access.redhat.com/articles/6999267

- https://pages.cs.wisc.edu/~matyas/selinux-policy/

- https://linux.die.net/man/1/audit2allow

- https://linux.die.net/man/8/audit2why