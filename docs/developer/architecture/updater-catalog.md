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

A release can also declare an optional, closed `extraction` object. Its field
names are budget keys for extracting the payload on the device:

```yaml
extraction:
  maxExtractedManifestBytes: 65536
  maxExtractedFiles: 64
  maxExtractedFileBytes: 67108864
  maxExtractedTotalBytes: 134217728
  extractionScratchBytes: 134283264
  maxExtractionSeconds: 300
```

All six values must be positive integers. `maxExtractedFileBytes` cannot exceed
`maxExtractedTotalBytes`, `extractionScratchBytes` must hold
`maxExtractedTotalBytes` plus `maxExtractedManifestBytes`, and
`maxExtractionSeconds` is at most 86400. Every extraction key must be listed in
`budgetKeySet`, and extraction keys are valid budget keys only when the object
is present. Releases without the object stay valid, but the agent extracts only
releases that declare it. `extractionScratchBytes` is the free space required
before extraction starts; it is not a reservation.

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

An agent without firmware work or retained policy does not issue policy requests.
The sequential engine checks retained current, desired, and rollback specifications
at startup and observes published targets before download and update scheduling.
Firmware intent or a retained attempt starts delivery. Unreadable retained targets
also start delivery because they cannot establish that firmware is unused.
Once started, delivery continues even if a target is removed, invalid, or blocked.
Retained policy, including unreadable policy history, resumes delivery after restart.
Stopping delivery on previously active devices requires a future attempt-finalization
contract; target removal alone does not establish that an attempt cannot resume.

The agent polls policy independently of rendered specifications, using the same
reloadable management client. File-backed and TPM-backed HTTP clients enable HTTP/2
so concurrent policy and rendered-spec requests can share a connection when HTTP/2
is negotiated. The delivery goroutine accepts policy through a policy-specific lock that serializes
validation, durable retention, and publication independently of device reconciliation.
Long-running downloads, installation, or activation do not delay revocation receipt
or subsequent policy reads. An invalid, unchanged, or unavailable target cannot skip receipt.
Existing bootstrap and enrollment gates remain in place.

Policy receipt is independent of the download schedule: authorization metadata,
including revocations, must reach a device while artifact downloads are blocked.
This path never downloads updater executables or firmware artifacts. Policy size
limits still apply; a changed revision transfers the complete policy document.

HTTP long polling remains the delivery mechanism and does not depend on AgentLink.
The receiver's `Receive` operation returns a revision/digest receipt only after it
validates and durably retains the policy under that lock. A future AgentLink
transport can reuse this acceptance path and reconcile from retained revision/digest
after reconnecting. It must acknowledge only a successful receipt and replace the
HTTP polling owner rather than start a competing delivery loop.

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

## Agent updater code fetch and verification

The agent package `internal/agent/device/firmware` contains a fetcher that
obtains and authenticates the code of one approved updater release. Firmware
holds, unmanaged devices and support reporting are unchanged.

The fetcher never executes image content. It runs only `podman pull`,
`podman image inspect`, `podman image exists`, `podman image mount` and
`podman image unmount`. It never runs an image entrypoint or an updater
container, and it does not extract, publish or run payload files.

### Fetch sequence

`Fetch(name, version)` performs these checks in order:

1. The release must be bound in the accepted policy and approved. A missing
   binding, a tombstone or unavailable retained policy stops the fetch.
2. The descriptor is decoded strictly: exact-case keys, no unknown or
   duplicate keys and no trailing content. It must match its binding digest
   and identity and carry a `sha256` package digest. Its publisher policy must
   be a version 1 keyed `sigstoreSigned` requirement with an `exactRepository`
   identity for a canonical repository without tag or digest, matching its
   policy digest. Every schema digest must cover its exact bytes, and the
   requested access must satisfy the catalog's access rules.
3. Before any podman command, the fetcher rejects an unsupported protocol
   (only protocol 1 is supported), any declared runtime dependency, and
   requested access outside the local `firmware.updater-access` policy.
4. A verified record is reused when it still matches the release and the image
   is present. Otherwise the fetcher pulls `<repository>@<packageDigest>` under
   a per-call signature policy, bounded by the agent `pull-timeout` setting,
   and confirms that the local image is the pinned manifest: podman reports the
   package digest as the image digest or, when local storage keeps a rewritten
   manifest, as the repository digest `<repository>@<packageDigest>`. The
   package digest can name an image manifest or a manifest list. For a manifest
   list, the publisher signature must cover the list digest, and the fetcher
   reads the manifest of the image that podman selects for the host platform.
5. The fetcher mounts the image and reads `updater/manifest.json` beneath the
   mount root without following a symbolic link in any path component. The
   read must be a regular file of at most 1 MiB. The image is then unmounted.
6. The manifest must satisfy the closed updater manifest contract and bind the
   approved release. Only then is a verified record written and a verified
   package returned.

### Trust layering

Three checks apply, and none substitutes for another:

- The approved descriptor authorizes the release.
- The immutable package digest pins the content. The fetcher never pulls a tag.
- A keyed sigstore signature for the exact repository authenticates the
  publisher. The key and repository come only from the approved descriptor.

Registry credentials only authenticate the transport. They never establish
trust.

For each pull the fetcher writes a containers signature policy to
`<DataDir>/firmware/signature-policy/<descriptor-digest-hex>.json` with mode
`0600` and removes it after the pull. It contains public key data only. It
rejects every image by default and admits only the approved key for the exact
repository:

```json
{
  "default": [{"type": "reject"}],
  "transports": {
    "docker": {
      "quay.io/example/updater": [
        {
          "type": "sigstoreSigned",
          "keyData": "<base64-encoded PEM public key>",
          "signedIdentity": {"type": "exactRepository", "dockerRepository": "quay.io/example/updater"}
        }
      ]
    }
  }
}
```

### Host prerequisites

The device administrator provides these prerequisites. The agent never writes
host container configuration.

- The agent runs rootful podman. Image mounts require root.
- Podman supports `podman pull --signature-policy`. Supported device podman
  versions remain a qualification item.
- Sigstore attachment discovery is enabled for each approved updater
  repository, for example in `/etc/containers/registries.d/updaters.yaml`:

  ```yaml
  docker:
    quay.io/example/updater:
      use-sigstore-attachments: true
  ```

- An authenticated registry needs credentials in the root containers auth file,
  `/root/.config/containers/auth.json`. The agent resolves this path through its
  pull configuration resolver: an inline file at this path in the desired
  device specification is used through a temporary copy, otherwise the file on
  disk is used. Without a file no `--authfile` is passed. Podman's own default
  credential lookup then applies as for other agent pulls, but it is not a
  supported credential source for updater code. The fetcher never reads,
  copies, logs or records credential content. Credentials resolve only while
  the resolver is initialized for an update, so the fetch belongs inside that
  update lifecycle.
- Native overlay storage mounts images read-only. `fuse-overlayfs`, which
  podman uses when container storage lies on overlayfs, mounts them read-write.
  The fetcher does not depend on a read-only mount: it never writes beneath the
  mount and reads only through the link-refusing, bounded read.

### Failure reasons

Failures are typed with the reasons used in firmware status:

- `UpdaterUnavailable`: no binding, an unapproved or revoked release,
  unavailable policy, a descriptor that fails strict decoding or a catalog
  access rule, a signature policy rejection, a digest mismatch, a missing,
  symbolic link, oversized, malformed or mismatched manifest, and registry or
  authentication failures.
- `FirmwareNotSupported`: an unsupported protocol, any declared runtime
  dependency, or requested access outside the local maxima, including the
  deny-all policy left by invalid local configuration.

Only pull failures other than a signature policy rejection are retryable,
such as a registry outage or an authentication failure. The agent's existing
retry classification reads the same result: a retryable fetch error matches
the retryable error marker and any other matches the no-retry marker. The
element of every error is the release `<name>/<version>`.

A pull rejected by the signature policy reports exactly this message:

```text
updater <name>/<version>: publisher signature verification failed for <repository>@<digest>; the image must be signed by the approved publisher key for this exact repository, and the host must enable sigstore attachment discovery (use-sigstore-attachments) for <repository>
```

It names both remedies: sign the image with the approved key for this exact
repository, and enable sigstore attachment discovery for the repository on the
host. The podman reason is logged and kept as the wrapped cause, not in the
message.

### Updater manifest

An updater image carries `/updater/manifest.json` and its payload beneath
`/updater/payload/`. The manifest is a closed `updater-manifest/v1` document:

| Field | Type | Rule |
|-------|------|------|
| `apiVersion` | string | exactly `updater-manifest/v1` |
| `name` | string | equals the approved release name |
| `version` | string | equals the approved release version |
| `protocol` | integer | equals the approved protocol; only `1` is supported |
| `entrypoint` | string | payload path naming a listed file marked executable |
| `dependencies` | array | must be `[]` |
| `schemas` | object | exactly `config`, `observation`, `request`, `result` and `checkpoint`, each equal to the approved schema digest |
| `files` | array | at least one object with exactly `path`, `sha256`, `size` and `executable` |
| `files[].path` | string | payload path, unique, not a parent directory of another listed path |
| `files[].sha256` | string | 64 lowercase hexadecimal digits without a prefix |
| `files[].size` | integer | from `0` to `9223372036854775807` |
| `files[].executable` | boolean | `true` or `false` |

Every field is required and `null` is rejected. Schema digests use the
`sha256:<64 lowercase hexadecimal digits>` form. Integers must be plain
non-negative integer tokens: no sign, fraction, exponent or leading zero.

Payload paths are relative to `/updater/`. A payload path is a clean relative
path without empty, `.` or `..` components or a NUL byte, and it lies strictly
beneath `payload/`. For example, `payload/bin/updater` names
`/updater/payload/bin/updater`. A path that is only a string prefix of another
path, such as `payload/bin` and `payload/binary`, is allowed.

The document must be a single JSON object followed only by whitespace. Unknown
keys, duplicate keys, keys that differ only by case, and values of the wrong
JSON type are rejected at every level. The manifest digest is the SHA-256 of
the exact bytes read.

This example binds a release named `vendor-uefi` version `1.4.2` whose schema
documents are `{"title":"<key>","type":"object"}`:

```json
{
  "apiVersion": "updater-manifest/v1",
  "name": "vendor-uefi",
  "version": "1.4.2",
  "protocol": 1,
  "entrypoint": "payload/bin/updater",
  "dependencies": [],
  "schemas": {
    "config": "sha256:30f8846d7c81099cd8df2bc46ff49e8773141872fd5f3b945c0daff540c18aff",
    "observation": "sha256:c27ee3be6dbff3e2ea17796af0cc2c87e5efb40d177ca205ee2929923f54669e",
    "request": "sha256:1c5318c38d3cc8e1a6805576610151adf4cd8146444942e885a9daa3adda179a",
    "result": "sha256:d43b7a6fa394712f500bd5f8ff4488f0943fcad506d1efa5eb332921538db563",
    "checkpoint": "sha256:5382edc26276a0bedb506412928054659ef9d95c05f9d9083afe1cd916c1afac"
  },
  "files": [
    {
      "path": "payload/bin/updater",
      "sha256": "905820073c735d475b79b03ae8617e4a495027dba3c83f5df6fe6536a3d1ed8b",
      "size": 23,
      "executable": true
    },
    {
      "path": "payload/share/vendor/README",
      "sha256": "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
      "size": 0,
      "executable": false
    }
  ]
}
```

The path rules are syntax checks only. The fetcher does not compare payload
bytes with `files`, and it does not walk, open or copy anything beneath
`/updater/payload/`.

### Local updater access policy

The agent configuration section `firmware.updater-access` sets the maximum
access an approved release may declare in each phase. It denies all access by
default. Each of `prepare`, `apply` and `verify` accepts these fields:
`network-repositories`, `credential-repositories`, `read-paths`, `write-paths`,
`read-devices`, `write-devices` and `workspace-write`. An omitted phase permits
no access.

The catalog's mandatory phase restrictions also apply to the local policy, and
a local policy that grants more is invalid:

- Only `prepare` may use network repositories and source credentials, and each
  credential repository must also be a network repository of that phase.
- Only `apply` may write host paths or devices.
- `verify` must be read-only, including its workspace.
- Repository entries are logical repository names such as `vendor-firmware`,
  not image references or URLs.
- Paths are explicit clean absolute paths without wildcards. Device entries
  are paths under `/dev/`. Entries are unique within each list.

Use a dedicated drop-in, for example `/etc/flightctl/conf.d/50-firmware.yaml`:

```yaml
firmware:
  updater-access:
    prepare:
      network-repositories:
        - vendor-firmware
      credential-repositories:
        - vendor-firmware
      read-paths:
        - /etc/vendor
      read-devices:
        - /dev/mtd0
      workspace-write: true
    apply:
      read-paths:
        - /etc/vendor
      write-paths:
        - /opt/vendor
      read-devices:
        - /dev/mtd0
      write-devices:
        - /dev/mtd0
      workspace-write: true
    verify:
      read-paths:
        - /etc/vendor
        - /opt/vendor
      read-devices:
        - /dev/mtd0
```

Requested access is compared per phase and field:

- Repository names and devices must match exactly and case-sensitively.
- A requested path must equal an allowed path or lie beneath it by path
  component, so `/opt/vendor` permits `/opt/vendor/fw` but not
  `/opt/vendor-other` or `/opt`.
- A read grant never permits a write, and `workspace-write` must be allowed
  explicitly.

A configuration file or drop-in that sets the `firmware` key replaces the whole
inherited firmware section. `firmware: null`, `firmware: {}` and an empty
`updater-access` clear it and deny all access without a warning. A file that
does not set the key keeps the inherited section. Firmware settings from
different files are never merged.

An invalid firmware section does not stop the agent. This includes a value of
the wrong type, an unknown key, a mistyped entry and a rule violation. Loading
records a warning that names the file and denies all updater access, and the
rest of the configuration loads as before. A later valid section replaces the
invalid one, and the earlier warning remains. A skipped drop-in also denies all
updater access, because it may have changed the policy. YAML syntax errors in
the main configuration file still stop the agent. Quote entries that look like
numbers or booleans, such as `"123"` or `"on"`, because they are not converted
to strings. A configuration reload applies the same rules, and
`Fetcher.ReloadConfig` applies the reloaded policy and pull timeout to the next
fetch.

This comparison decides only whether a release may be prepared on the device.
Declared access is not runtime confinement: invoking updater code will require
qualified runtime enforcement of the declared access.

### Runtime dependencies

Only dependency-free releases are supported. A descriptor that declares any
runtime dependency is rejected with `FirmwareNotSupported` before any network
use, whatever its constraint. The manifest must declare `dependencies: []`.
Matching declared dependencies against the host requires a dependency
vocabulary and probe design that do not exist yet. The agent never installs
packages or changes the operating system.

### Verified packages, cache and revocation

A verified package identifies updater distribution content that passed the
authorization, publisher signature, image digest, manifest, binding,
dependency and access checks. It does not authorize execution. Payload bytes
have not been verified against the manifest file list, nothing has been
extracted, and declared access is not runtime confinement. Before updater code
runs, extraction must verify payload bytes and invocation must check approval
again.

The verified record is written to
`<DataDir>/firmware/packages/<descriptor-digest-hex>.json` with mode `0600`,
using the same durable replacement as the retained policy. It is written only
after every check passed. A record is reused only while all of these hold:

- it is a well-formed `updater-package-record/v1` record;
- it names the approved descriptor, package digest and publisher policy;
- it binds the approved manifest identities;
- the image is still present by digest.

Otherwise the fetcher pulls and verifies the package again and rewrites the
record, so a corrupt record is ignored. Approval and local access are checked
before a record is used. A revoked release or an access request that the local
policy no longer admits is refused even when a record exists. A new agent
process reuses valid records after a restart.

Images that fail checks after the pull are kept as evidence and never recorded
as verified. Image pruning protects verified updater images only while a
retention pin references them (see "Agent updater payload extraction and
retention"). If pruning removes an unpinned one, the next fetch pulls and
verifies it again.

## Agent updater payload extraction and retention

The agent extractor publishes the payload of a verified package into a managed
cache so that later lifecycle code can run it natively. Extraction never runs
updater code, image entrypoints or hooks, never creates a container and never
pulls by tag. Nothing in the agent calls the extractor yet; it is a library for
the firmware lifecycle.

### Trusted cache and threat model

The cache lives at `<DataDir>/firmware/updaters/`, with host evidence in
`<DataDir>/firmware/evidence/` and the retention registry in
`<DataDir>/firmware/retained.json`. Every path component from `/` down to these
directories must be a real directory, owned by root (or the agent's effective
user), without group or other write access; the packaged
`/var/lib/flightctl` mode `0755` owned by root satisfies this. Every write,
rename, removal and extended-attribute read happens relative to directory
handles opened without following symbolic links, so a symlinked or replaced
path component, including one planted by another privileged process, is
refused rather than followed. A cache directory is revalidated before a result
counts as usable. This does not protect against a malicious root process with
arbitrary system-call, ptrace or mount authority, a compromised kernel or
storage faults.

### Budgets and order of checks

Before anything is mounted, the extractor checks that the package reference is
pinned to its digest, takes the extraction budget from the authenticated
release descriptor (a release without the object is refused), compares it
with the agent ceilings (1 MiB manifest, 4096 files, 1 GiB per file, 2 GiB in
total, 3 GiB scratch, 1800 seconds and a path depth of 16 components), checks
the manifest file count, sizes and depth against the budget, and checks the
current approval of the release. A budget above a ceiling is rejected, not
clamped. It then checks the retention registry and the free space of the
cache.

### Extraction and publication

A fresh extraction mounts the digest-pinned image and reads it through a
read-only directory handle. The image must hold exactly `updater/manifest.json`
and `updater/payload/` with the files the manifest lists and their parent
directories; links, special files, extra or missing entries, set-ID or sticky
modes and file capabilities are refused. The manifest read from the image must
match the verified manifest exactly. Each file is copied in bounded chunks into
an exclusive `.staging-*` directory with its size and SHA-256 checked; files get
mode `0400`, executables `0500` and directories `0700`. Executables must be
static ELF binaries for the device architecture. Native overlay mounts are
read-only and fuse-overlayfs mounts are writable; the agent records which one it
saw as an observation only, and integrity comes from hashing the bytes copied.

After staging, the extractor closes the source and unmounts the image under its
own two-minute timeout, which still runs when the caller cancels or the
extraction deadline has expired. Only after a successful unmount does it rename
the staging directory to `<package-digest-hex>` without replacing anything, then
sync and revalidate the cache. A result is usable only after its host evidence
is durable:

| Outcome | Meaning |
|---------|---------|
| Not visible | No version directory was made visible. The call removes its own staging directory under a separate two-minute timeout. |
| Visible but not usable | A complete version directory is visible but its sync, revalidation or evidence write failed. It is kept, must not be used or pinned, and the next call verifies and reuses it. |
| Usable | The version directory is durable, revalidated and recorded. |

When the version name already exists, the extractor keeps its own staging and
verifies the existing directory. A valid one is used instead of the staging. An
invalid pinned one, or one that cannot be verified, is left untouched and the
call fails. An invalid unpinned one is renamed to `.quarantine-*` once and the
publication is retried once with the same staging; if the name is taken again
by anything but a valid copy, the call fails as contended. A valid copy is never
deleted, and nothing is mounted or copied again.

An unmount failure never publishes anything. The error names the image
reference and the mount point, and a warning is logged with both. Podman keeps
the leaked mount until an operator runs `podman image unmount <reference>` or
the host reboots; the agent does not unmount blindly on later calls. The
unmount timeout relies on the executor killing the podman process group; a
descendant that leaves the group while holding the output pipes can delay the
return.

### Retention, verification and cleanup

The retention registry pins published payloads per attempt and role (active or
recovery). Pins survive restarts, refuse a missing version directory or a
conflicting identity, and an unreadable or malformed registry fails closed for
every operation. A usable payload is leased in memory until it is pinned or its
lease is released, so cleanup cannot remove it in between; leases end with the
agent process.

A pinned payload is only verified and reused. Verification checks the exact
entries, modes, owner, single links, sizes and digests within the pinned limits
capped by the ceilings. A missing pinned payload reports
`ErrRetainedPayloadMissing` and a changed one `ErrRetainedPayloadChanged`, both
without mounting, extracting again or repairing anything, also after a restart;
a verification deadline is retryable. An unpinned verified copy is reused, and
an invalid unpinned copy is quarantined before a fresh extraction.

Image pruning treats the image references of pinned payloads as required root
podman references and records them with the other references; a registry read
failure skips that pruning cycle. Cleanup removes only `.staging-*` and
`.quarantine-*` directories, unpinned and unleased version directories and
evidence records of removed versions. It removes at most four trees per pass;
each removal is a bounded postorder walk that never follows links or crosses
devices, holds at most 16 directory handles and stops with a reported reason
when it reaches its limits on removed entries, total work, detached deep
directories or retries. Listing is capped at 8192 entries per directory and a
pass has a separate work cap; the next pass continues where it stopped.

### Host SELinux evidence

For each publication and reuse, and when a pinned payload is verified, the
agent records what the host reports: the SELinux mode from
`/sys/fs/selinux/enforce` (absent means disabled), the boot ID, and the
`security.selinux` label of the version directory and of its entrypoint, each
present, absent, unsupported or unknown. The record
`<DataDir>/firmware/evidence/<package-digest-hex>.json` has the closed format
`updater-host-evidence/v1`, is at most 16 KiB and keeps the first observation
after publication, the latest one, a counter and a flag for any mode or label
change. Extraction is allowed in every SELinux mode. The evidence is an
observation only: it neither admits nor denies execution, is not a
qualification, and a mode or label change never invalidates retained bytes or
forces extraction again. No SELinux type, policy or label is added.

### Reserved configuration path

The configuration controller rejects device configuration that writes any file
at or beneath `<DataDir>/firmware`, using the configured data directory. The
whole configuration is refused before any file is removed or written, and the
error names the path. Paths are compared after lexical cleaning with component
boundaries, so `<DataDir>/firmware-x` stays allowed; relative paths and
symbolic links that alias the data directory are not resolved. The controller
also never deletes an obsolete file inside that directory. This guard adds to,
and does not replace, the handle-based confinement and retained verification.

### Failure reasons

| Condition | Reason | Retryable |
|-----------|--------|-----------|
| No extraction budget, a changed binding or a revoked release | `UpdaterUnavailable` | no |
| A budget above an agent ceiling, a path deeper than 16 components, or a non-native executable | `FirmwareNotSupported` | no |
| File count, file size, total size, manifest size or extraction time above the admitted budget | `ResourceLimitExceeded`, with `exceededBudget` naming the budget key | no |
| Free space below `extractionScratchBytes`, or the disk filling up while copying | `InsufficientSpace` | no |
| A structure, link, mode or digest violation in the image | `UpdaterUnavailable` | no |
| A missing or changed pinned payload | `UpdaterUnavailable` | no |
| An unmount failure, an evidence write failure, a failed quarantine, a contended publication or a verification deadline | `UpdaterUnavailable` | yes |

A caller cancellation carries no firmware reason. When cleanup also fails after
a failure, the cleanup error is joined without changing the reason,
retryability or exceeded budget of the first failure. Every deadline is
enforced between bounded operations: a single read, write, sync, directory
read, unlink or rename that blocks in the kernel cannot be interrupted, so the
agent claims no hard wall-clock bound.

## Helm installation and recovery

Create an administrator-owned ConfigMap containing the whole catalog. This
initial example approves no updater releases:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: firmware-updaters-r1
  namespace: flightctl
immutable: true
data:
  updaters.yaml: |
    apiVersion: firmware-catalog/v1
    revision: 1
    releases: []
```

Select it through chart values:

```yaml
global:
  firmwareUpdaterCatalog:
    configMapName: firmware-updaters-r1
    key: updaters.yaml
```

The chart mounts the selected key read-only as
`/etc/flightctl/firmware/updaters.yaml` and sets `firmwareUpdaterCatalogFile` for
the API (including agent API), worker, and periodic runtimes. It does not create
or edit the administrator ConfigMap. Provide identical ConfigMaps in the release
namespace and `global.internalNamespace` when those namespaces differ.

For each update, validate a complete higher-revision document, retain previous
tombstones, create a new immutable ConfigMap name, and update the Helm value.
The changed pod volume source rolls the runtimes. Identical activation by several
starting replicas is safe. Changing data in a mutable ConfigMap alone does not
activate a running process: there is no file watcher.

A bad, conflicting, or stale configured document fails startup while the last
accepted database state remains intact. Recover by supplying the accepted
current document or a valid higher revision. Setting the ConfigMap name to empty
removes the startup-file selection and uses accepted database state; it does not
clear history or undo revocation. Do not restore an older database or delete an
agent policy file to reset authorization. Back up the complete trusted catalog
history and retain the administrator input that produced it.

For other deployments, mount the same administrator file read-only and set the
startup configuration field directly in each relevant runtime. Catalog input
contains public publisher trust and access metadata, not repository credentials.
Installing it does not enable firmware mutation; execution and post-arm handling
must still satisfy their integration and qualification gates.
