# Updater catalog authorization

The updater catalog supplies trusted release descriptors for firmware admission.
Product and platform administrators own this installation-wide configuration.
Device and Fleet writers can select releases but cannot approve executable code,
access grants, or publisher policies. The application Catalog API has no role in
updater authorization.

Catalog installation enables admission against accepted descriptors. It does not
enable updater execution or firmware target delivery. Agent support, independent
policy receipt, access enforcement, armed-attempt handling, and platform
qualification remain separate execution requirements.

## Startup configuration

The API, agent API, worker, and periodic runtimes accept this configuration field:

```yaml
firmwareUpdaterCatalogFile: /etc/flightctl/firmware/updaters.yaml
```

An empty path uses previously accepted database state. If no state has been
accepted, firmware admission fails closed; resources without firmware intent
continue through their existing paths. There is no file watcher or public upload
endpoint. A changed administrator file takes effect when a runtime starts.

A configured file must exist and validate in full. Invalid or stale input fails
that runtime's initialization without replacing the accepted catalog. Concurrent
startup with identical input is idempotent. Authorization lookups read current
database state, so a running process does not retain approval after another
replica activates a revocation.

## Closed catalog contract

The input document has `apiVersion: firmware-catalog/v1`, a positive integer
`revision`, `releases`, and optional `revocations`. Unknown or duplicate keys,
YAML aliases, fractional numeric fields, and multiple documents are rejected.

Each immutable name/version binds the package digest, publisher policy,
protocol, schemas, dependencies, phase access, finite execution limits, budget
keys, and permitted configuration paths. Changes to these bindings require a new
release identity. A descriptor by itself never grants current authorization.

Publisher policy version 1 supports keyed `sigstoreSigned` with embedded base64
PKIX PEM public-key data and an `exactRepository` signed identity. The repository
must be explicit and contain no tag or digest. Key paths, keyless policy, and
permissive fallback rules are unsupported. The catalog policy version is an
envelope discriminator; an execution consumer constructs its signature
requirement from the accepted fields. Catalog validation does not verify package
signatures, and code-publisher approval does not replace firmware artifact trust.

Schema documents carry a SHA-256 digest and a `bytes` string containing exact JSON
text. JSON and YAML representations preserve those bytes, including whitespace.
Configuration, observation, request, result, and checkpoint schemas must be closed
objects. References remain local; validation never fetches a remote schema.

Access grants are explicit for prepare, apply, and verify. Only prepare can use
network repositories and source credentials, and it can write only its private
workspace. Apply can request approved host or device writes. Verify is read-only.
No grant authorizes a host reboot, OS deployment, command string, or shell.

Execution limits require positive integer bounds for request, result, stderr,
checkpoint, scratch, memory, processes, invocation time, cycles, reboots, and
attempts. Reported exceeded-budget keys come from the immutable approved key set.

## Revision and history rules

Accepted input has its own canonical digest, separate from the materialized
policy digest. Under the catalog-head lock:

1. Reject a lower revision.
2. Accept the same revision and input digest without another refresh record.
3. Reject the same revision with different input.
4. For a higher revision, validate immutable history before accepting any change.

Omitting an approved release at a higher revision creates a retained revocation
tombstone. Existing bindings and observation evidence remain available. All
previous tombstones must be explicitly retained in a newer input document;
revoked identities cannot be reactivated.

For example, revision 1 approves A. Revision 2 omits A and creates its tombstone.
Replaying that exact revision-2 file succeeds even though the file has no
explicit tombstone. Revision 3 must retain the tombstone. This ordering prevents
idempotent startup from being mistaken for loss of history.

Accepted state and refresh work commit together. Failed validation or persistence
leaves the previous state intact. Delivery work remains durable until recipient
fanout completes; a database commit alone is not proof of agent receipt.

## Retained evidence

The retained lookup key is updater name plus schema digest. A pinned attempt uses
its configuration-schema digest; component observations use their
observation-schema digest. Both map to the same immutable observation schema and
budget-key evidence. The returned digest matches the requested lookup identity;
the configuration identity is an explicit alias, not the observation-byte hash.

Shared keys must return identical schema bytes and budget-key sets, including
across revoked history. Evidence survives revocation and restart but never
restores execution authorization. Canonical ordering in activation and reads is
independent of database collation.

## Bounds and recovery

Input is limited to 4 MiB and 256 combined release/revocation records. Each schema
is limited to 256 KiB with bounded nesting. Retained state permits at most 256
release identities and 4 MiB of descriptor bytes. The materialized policy budget
is 8 MiB, reserving room to revoke accepted releases when descriptor capacity is
exhausted. Oversized state is rejected rather than truncated.

Backups must retain the catalog head, immutable bindings, schema evidence,
tombstones, and refresh work together. Restoring an older database can discard
trusted history; revision checks cannot recover state an administrator removed
from every trusted copy. Offline devices retain their last accepted policy, so
revocation takes effect after receipt rather than instantaneously offline.

## Durable refresh fanout

The worker resumes refresh work at startup and polls for newly accepted policy.
It scans active device identities in bounded pages across organizations without
reading desired specifications. This conservative recipient set includes devices
with invalid firmware intent, pinned attempts, and no firmware intent. Refresh
never resolves a target or changes retry generation, rendered version, or status.

Each scan cursor commits atomically with its recipient rows. One recipient row
per device retains monotonic pending and published revisions. Newer policy can
coalesce older pending work; repeated publication is safe. Failed publication or
failure to record publication leaves work available for retry after restart.
Deleting a device removes its recipient row.

Revision high-water marks live in PostgreSQL. The separate
`firmware_policy_notifier` Redis channel carries refresh hints only, preserving
full integer revision precision and isolating rendered-target subscribers. A
successful publish records notification, not agent receipt. Authenticated policy
reads and durable receiving state must establish delivery independently.

## Private policy delivery

Enrolled devices read `/api/v1/devices/{name}/firmwarepolicy` on the agent
endpoint using their management certificate. The authenticated device and
organization must match the request. The public resource API exposes no
writable authorization view. Policy reads check device identity without decoding
desired intent, rendering a target, or waiting for a rendered version. A paused
or enrollment-hook-gated device can receive revocation while its mutation target
remains gated.

A first GET returns the complete view. Later reads send `knownRevision` and
`knownDigest`, representing durably retained state. An unchanged view waits up to
a bounded timeout and returns 204. The API registers for refresh hints before
reading the database head and rechecks the head after hints or timeout, so losing
a hint does not lose policy. Multiple requests for one device remain independent.
An accepted head older than the supplied revision, or the same revision with a
different digest, returns conflict.

The private response contains revision, digest, and base64 document bytes. The
canonical `firmware-policy/v1` document binds the revision, every retained binding
(including exact descriptor bytes and descriptor digest), and every tombstone.
Identity ordering is deterministic; empty collections are arrays. Its SHA-256
matches the database delivery digest. Migration normalizes older materialization
digests without changing accepted input, revision, or immutable history. The
receiver validates the complete document and its history before accepting it;
notification and HTTP success alone are insufficient.

PUT to the same private path acknowledges only the revision and digest after
durable retention. Acknowledging the current view is idempotent. An older or
conflicting acknowledgement returns 409 and requires a fresh read. Receipt
revision/digest are stored separately from publication bookkeeping and never
modify Device spec/status, retry generation, or rendered version. None of these
operations invokes an updater or grants reboot permission.

## Agent policy retention

The agent polls policy independently of rendered specifications, using its
reloadable management client. Network I/O places a complete view into a bounded
queue. The sequential device engine accepts queued policy before desired-spec
processing, so an invalid, unchanged or unavailable target cannot skip receipt.
Existing bootstrap and enrollment gates remain in place.

Accepted policy is separate from target and attempt data at
`<DataDir>/firmware/policy.json` (normally
`/var/lib/flightctl/firmware/policy.json`). The agent uses a private directory and
file, bounded reads, and an atomic file replacement that syncs file bytes,
its directory, and every ancestor directory, including on retries after failed
directory creation or syncing.
Only after durable write success does the accepted in-memory view change and an
acknowledgement become eligible.

Corrupt or unreadable retained state withholds firmware authorization. A write or
sync failure also withholds authorization, preserves prior accepted evidence,
and prevents acknowledgement. While running, the validated newer view remains
a revision/history floor so replaying an older approval cannot clear that hold.
A successful retry can retain the newer view. Failed acknowledgements do not
prevent fresh reads and revocation receipt. Restart reloads and validates the
complete retained view; offline requests preserve the last accepted policy.

Exact release name, version and immutable descriptor digest must be currently
approved. A retained tombstone removes that authorization, including for later
read-only invocations; retained evidence does not restore approval. The receiver
never changes spec, retry generation, rollback or pinned attempt data and never
executes code or arms a reboot. Mutation remains fenced pending execution and
post-arm hold integration and qualification.
