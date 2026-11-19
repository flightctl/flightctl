# Mixed OS-mode fleet template migration E2E

This suite covers the one-shot migration that guards a fleet template OS image
so a fleet can hold both image-mode and package-mode devices.

It drives the whole path: the deployed periodic worker runs its registered
migration task, the task rewrites the persisted fleet, the rendered device specs
diverge by OS mode, and both agents reconcile. The task is never called directly
from the test.

## What the suite builds

| Member | How it runs | Role |
|--------|-------------|------|
| Image-mode device | Agent VM from the pool | Keeps receiving and applying the fleet's OS target |
| Package-mode device | `flightctl-agent` helper container | Must stop being held back by a fleet OS image it cannot apply |

The fleet starts with an unguarded OS image, which is the state a fleet created
before the guard existed is in.

## This suite requires a deployment it owns outright

It discards the fleet template migration's checkpoint and stops the periodic
worker. Neither is scoped to one test: re-running the migration rewrites every
eligible fleet in the organization, and the worker is a single
deployment-wide service. Organizations here are provisioned from identity
claims and there is no create-organization API, so the suite cannot carve out
one of its own; it takes the whole deployment instead, and proves it may.

Three gates, all in code:

1. **Opt-in.** `E2E_PACKAGE_MODE_MIGRATION_EXCLUSIVE=true` must be set, or
   `BeforeSuite` skips before any setup runs. This keeps a wildcard
   `GO_E2E_DIRS=./test/e2e/...` run from activating the suite.
2. **A global census, enforced before any mutation.** The first thing the spec
   does is count every live fleet, every live device and every organization in
   the database, and refuse to continue unless all of those are zero, zero and
   one — with that one organization being the one it authenticated as. The
   count deliberately does not go through the API: `ListFleets` and
   `ListDevices` are filtered to the caller's organization, so an empty answer
   there says nothing about other tenants, while stopping the periodic worker
   affects all of them. The opt-in alone is not accepted as proof.
3. **The periodic worker must already be running.** The suite stops and
   restarts it, and will not start one somebody else deliberately stopped.

Only after all three pass does anything get stopped, deleted or created. The
query, its parser and the decision are `exclusivity.go`, covered by
`exclusivity_test.go`; an inventory that cannot be read is never treated as an
empty deployment.

**This is a precondition, not a lock.** The census is a point-in-time
observation; it cannot stop another writer that arrives afterwards. Use a
deployment nobody else is touching for the duration of the run.

## Prerequisites

In addition to the usual e2e prerequisites, this suite needs:

- test runner support for privileged containers with read-write `/sys/fs/cgroup`
  bind mounts for the nested Podman helper container
- prepared agent config and certs under `bin/agent/etc/flightctl`
- a package-mode agent image, preloaded by `test/scripts/run_e2e_tests.sh`
- an agent VM pool, as used by the rollout suite
- a deployment whose service lifecycle can be driven by the infra providers, so
  the periodic worker can be stopped and started around the fixture

Prepare the environment first with:

```bash
make prepare-e2e-test
```

Run the suite with:

```bash
E2E_PACKAGE_MODE_MIGRATION_EXCLUSIVE=true \
  make in-cluster-e2e-test GO_E2E_DIRS=test/e2e/package_mode_migration
```

## Determinism

The migration is one-shot per organization. A deployment completes it on an
early poll, and a completed checkpoint makes every later poll return without
reading any fleet, so a fleet created afterwards would never be migrated. On a
genuinely fresh deployment the opposite risk applies: the task would run over
the fixture while it is still being built.

The suite therefore gates the migration before the fixture exists, in this
order:

1. The exclusivity gates above, which are read-only.
2. The periodic worker is stopped. Restoration is registered *before* the stop
   is asked for, so a stop that half succeeds is still undone, and a failure to
   restore fails the run rather than being logged and ignored. Only a worker
   this suite stopped is restarted.
3. The migration's checkpoint is discarded.
4. A label-scoped sweep of this test's own resources is registered before the
   first creation, so anything created from that point on is collected even if
   the call that would have registered its own cleanup never returns — an
   enrollment approved moments before a wait times out included.
5. Only now are the fleet and the two agents created, each with its own
   cleanup registered as soon as it exists.
6. The pre-migration template is read back, so the starting state is observed
   rather than assumed.
7. The periodic worker is started again and the suite waits for the rewritten
   template.

Rolling a fleet out to its devices is driven by the worker from resource events
(`internal/tasks/fleet_rollout.go`), not by the periodic worker, so both devices
still receive the legacy template in step 5 while the migration stays parked.

There are no fixed sleeps: every wait is a poll with a timeout.

## Rendered versions

The migration spec requires a rendered-version advance only from the
package-mode member. Its rendered spec really changes — the OS image it could
not apply becomes empty — whereas the guard renders the same image string for
the image-mode member and the configuration is untouched, so its rendered spec
is byte-identical and `deviceRender` leaves the version alone on purpose
(`internal/tasks/device_render.go`: "If the device spec hasn't changed … the
rendered version is not bumped"). Demanding an advance there would fail every
run. The configuration spec that follows changes real content, so it requires
both members to advance and reads the file off both devices.

## Supported invocation

The opt-in reaches the suite through the ordinary environment: `make` exports
environment-origin variables to its recipes and `test/scripts/run_e2e_tests.sh`
runs ginkgo in the same process tree without filtering. The remote-VM path
(`E2E_SSH_HOST`) has not been verified to forward it; run this suite from the
host that drives the deployment.

## CI

The default `GO_E2E_DIRS` is `./test/e2e/...` (`test/test.mk`), so this
directory *is* selected by a wildcard run. That is why activation is gated in
code rather than by a CI exclusion list: a row that picks the directory up runs
`BeforeSuite`, finds no opt-in, and skips. `test/scripts/run_e2e_tests.sh`
preloads the package-mode agent image for this directory as it does for
`test/e2e/package_mode`. Both behaviours are regression-tested against the real
script and the real gate in `test/ci/package_mode_migration_selection_test.go`.
