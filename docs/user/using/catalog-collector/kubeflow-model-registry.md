# Importing a Kubeflow Model Registry

The catalog collector includes a built-in source that polls a Kubeflow Model Registry, including instances running in Red Hat OpenShift AI. Used with the `flightctl` destination, it synchronizes model metadata into a Flight Control catalog.

Each imported model becomes a `data` CatalogItem. Device and fleet specifications can reference its versions through `catalogItemRef` in image-based volumes. The collector imports metadata and artifact references; it does not copy model images.

This guide describes how the source maps registry content to Flight Control resources, how to configure it, and how registry changes affect synchronization.

For configuration concepts, see [Catalog collector overview](overview.md). For source settings and defaults, see the [Kubeflow Model Registry configuration reference](../../references/catalog-collector.md#kubeflowmodelregistry).

## How the source works

During each collection cycle, the source:

1. Lists every page of registered models matching the model filter.
2. Lists every page of versions matching the version filter for each selected model.
3. Lists every page of artifacts for each selected version.
4. Selects exactly one eligible model artifact per selected version.
5. Builds a snapshot containing the configured Catalog and one CatalogItem per selected model with at least one matching version, then computes a deterministic revision.

Models with no versions matching the version filter are omitted.

If collection or mapping fails, the source does not deliver a snapshot to its pipelines. That failed collection therefore causes no destination writes or pruning. During runtime, the source waits according to its bounded exponential backoff and retries. Downstream pipeline failures also trigger retry backoff.

A successful collection with no matching models is different from a failed collection: it produces a snapshot containing the configured Catalog and no CatalogItems. With the `flightctl` destination, this requests removal of the pipeline-managed CatalogItems, subject to Flight Control API protections.

## Mapping registry content to catalog resources

The source emits one Catalog, named by the `catalog` configuration field, and one CatalogItem per selected registered model with at least one selected version.

The following tables describe the source’s output before processors run.

### Catalog

| Catalog field | Value |
|---|---|
| `metadata.name` | The `catalog` field from the source configuration |
| `spec.displayName` | The same value |

Choose a stable `catalog` value to keep the Flight Control catalog name unchanged when the registry is renamed or replaced.

For pipeline-specific renaming, the `catalogname` processor can change the Catalog name and update the CatalogItems’ catalog references. It leaves the Catalog’s `spec.displayName` unchanged.

### Catalog item

| CatalogItem field | Value |
|---|---|
| `metadata.name` | The registered model name, normalized to a valid DNS subdomain |
| `metadata.catalog` | The configured catalog name |
| `spec.type` | `data` |
| `spec.category` | `application` |
| `spec.displayName` | The registered model name, unchanged |
| `spec.shortDescription` | The registered model description, unchanged, when present and not blank |
| `spec.provider` | The first non-blank value from the model’s `owner` and `provider` fields, in that order. Surrounding whitespace is trimmed. Omitted if both are blank. |
| `spec.artifacts` | One entry of type `container`, whose `uri` is the OCI repository without a tag or digest |
| `spec.versions` | One entry per selected model version |

All selected versions of a model must use the same OCI repository. The repository is stored once in `spec.artifacts`, and each version’s digest is stored in its references. Different repositories within one model cause the collection cycle to fail.

### Versions

Each version entry contains:

| Version field | Value |
|---|---|
| `version` | The model version name, unchanged |
| `channels` | `["stable"]` |
| `references.container` | The immutable `sha256` digest of the version’s eligible artifact |

The source assigns every imported version to the `stable` channel. It does not populate `replaces`, `skips`, or `skipRange`, so the imported versions have no upgrade edges between them.

### Name normalization

Registered model names can contain free-form text, while a CatalogItem’s `metadata.name` must be a valid DNS subdomain. The source normalizes model names using these rules:

1. Uppercase ASCII letters become lowercase.
2. Lowercase ASCII letters and digits are preserved.
3. Dots separate DNS labels.
4. Each run of other characters, including non-ASCII characters, becomes a single hyphen within its label. Non-ASCII text is not transliterated.
5. Empty labels are removed, and leading and trailing hyphens are stripped.
6. Each label is truncated to at most 63 bytes, and the complete name is truncated to at most 253 bytes. Trailing dots or hyphens left by truncation are removed.

The original model name is preserved unchanged in `spec.displayName`.

| Registered model name | CatalogItem `metadata.name` |
|---|---|
| `Fraud Detector V2` | `fraud-detector-v2` |
| `Team.AI_Model` | `team.ai-model` |
| `..Model--Name..` | `model-name` |
| `模型` | Rejected: normalization produces an empty name |

If normalization produces an empty name, the collection cycle fails.

If two selected models normalize to the same name, the collection cycle fails with an error naming both models. For example, `Model A` and `Model_A` both become `model-a`. Truncation can also cause collisions.

Rename one of the models in the registry so their normalized names differ.

### Version names

Version names are published unchanged and validated using the same rules as the Flight Control API.

Accepted names include `1.0`, `1.0.0`, `2.0.0-rc.1`, and `2.0.0+build.7`. Names such as `v1.0.0` and `latest` are rejected.

An invalid version name fails the collection cycle with an error identifying the model and version. Correct the name in the Model Registry; the collector does not rewrite it.

Versions are published in ascending precedence order, independently of the order returned by the registry:

* Numeric components are compared numerically, so `1.9.0` comes before `1.10.0`.
* Build metadata is ignored when comparing precedence.
* A missing patch component is treated as zero for comparison, so `1.0` and `1.0.0` have equal precedence.
* When precedence is equal, the complete original version strings are compared lexicographically to determine their order.

Sorting keeps the output deterministic without changing the published version strings.

Two selected versions of the same model with exactly the same name fail the collection cycle. Different names with equal precedence, such as `1.0` and `1.0.0`, are allowed and remain separate version entries.

## Artifact eligibility and OCI references

Each selected model version must have exactly one eligible artifact. An artifact is eligible when:

* Its `artifactType` is `model-artifact`.
* Its lifecycle state is `LIVE`, `UNKNOWN`, or absent.
* Its URI is non-blank and contains a valid, digest-pinned OCI reference.

An absent artifact state is treated as `UNKNOWN`. This supports ModelCar artifact records that omit the field. Artifacts in other states, such as `ABANDONED`, are ignored.

Artifact lifecycle states are separate from the model and version states used by the selection filters.

### OCI reference requirements

| Rule | Requirement |
|---|---|
| Form | A fully qualified OCI image reference containing a registry host and repository path. |
| Scheme | No scheme is required. An optional `oci://` prefix is accepted and removed. Other schemes are rejected. |
| Digest | Exactly one `@` separator, followed by `sha256:` and exactly 64 lowercase hexadecimal characters. |
| Tag | An optional tag is accepted alongside the required digest. The tag is discarded. |
| Whitespace | Leading or trailing whitespace is rejected. |

For example:

```text
oci://quay.io/example/iris:1.0.0@sha256:bd62d86fc106e620c60f3905ebb1f8d102dc2470d049d845eada1c9b823801ba
```

The source stores `quay.io/example/iris` in `spec.artifacts[0].uri` and the `sha256:...` digest in the version’s `references.container`. The scheme and tag are not retained.

All selected versions of one model must use the same repository. Versions pointing at different repositories cause the collection cycle to fail.

### Excluded artifacts and collection failures

Recognized non-model artifacts and model artifacts in excluded lifecycle states are ignored. A model artifact with a missing or blank URI is also ignored.

The entire collection cycle fails when:

* A selected version has no eligible artifact or more than one eligible artifact.
* A model artifact in an accepted lifecycle state has a non-blank but malformed URI, even if another artifact is valid.
* An artifact record has a missing or unrecognized `artifactType` and cannot be decoded into a supported artifact type.

OCI reference validation errors describe the problem without echoing the supplied URI, which could contain sensitive information.

## Selecting what to import

The source sends both filters to the registry as server-side `filterQuery` values.

| Field | Default | Effect |
|---|---|---|
| `selection.modelFilter` | `state='LIVE'` | Selects registered models. |
| `selection.versionFilter` | `state='LIVE'` | Selects versions of each selected model. |

Omit the `selection` block to use the defaults. Set either filter to `""` to omit `filterQuery` for that endpoint. All returned records are then considered for import, subject to artifact eligibility and mapping rules.

A selected model with no versions matching the version filter is omitted from the snapshot. With the default version filter, this includes models whose versions are all archived.

A filtered-out version is absent from the desired snapshot. A selected version without an eligible artifact instead causes collection to fail.

Startup preflight checks that the registry accepts both queries. It accepts empty result lists, so it does not verify that the filters select the intended content. Check the selected models and versions before applying filter changes to a pipeline that synchronizes with Flight Control.

### Publishing with a custom property

If your registry supports filtering by custom properties, you can select content using an explicit publication marker instead of lifecycle state.

For example, add a custom string property named `publish_target`, with the value `flightctl`, to both:

* Each registered model you want to publish.
* Each version of that model you want to publish or retain.

Configure the following `selection` block in the source:

```yaml
selection:
  modelFilter: "publish_target.string_value='flightctl'"
  versionFilter: "publish_target.string_value='flightctl'"
```

Filter syntax is defined by the registry. Verify that your deployed registry accepts these queries and returns the intended records.

Before switching an existing pipeline to these filters, add the property to every model and version that must remain in its catalog.

With these filters, archiving a marked model or version does not exclude it from selection. Its artifact must still meet the eligibility requirements. Combining the property filter with `AND state='LIVE'` would continue to exclude archived content.

Keep the property on the model and its referenced versions until those versions can be retired. Removing it excludes content from the desired snapshot; the collector does not automatically retain versions based on fleet or device usage.

For retirement and recovery procedures, see [Retaining versions that are in use](#retaining-versions-that-are-in-use).

## Polling and failure handling

| Field | Default | Description |
|---|---|---|
| `pollInterval` | `5m` | Delay after collection and all downstream pipelines succeed. |
| `requestTimeout` | `30s` | Timeout for each Model Registry HTTP request. |
| `collectionTimeout` | `5m` | Timeout for collecting a complete snapshot, including pagination and normalization. Excludes downstream pipeline processing. |
| `pageSize` | `100` | Number of items requested per paginated API call. A value of `0` uses the default. |
| `backoff.initialInterval` | `1s` | Base retry delay after the first failed cycle, before jitter is applied. |
| `backoff.maxInterval` | `5m` | Maximum actual retry delay, including jitter. |
| `backoff.multiplier` | `2` | Growth factor applied to the base retry delay after each consecutive failure. |
| `backoff.randomizationFactor` | `0.5` | Factor controlling symmetric jitter around the base retry delay. |

The first collection cycle starts immediately when the source starts, after startup preflight succeeds. Cycles for the same source instance do not overlap.

After collection and all downstream pipelines succeed, the source resets backoff and waits for `pollInterval` before starting another collection. This delay begins after pipeline processing finishes.

A collection or downstream pipeline failure triggers retry backoff. Each retry starts a fresh collection from the registry. Successful collection alone does not reset backoff if downstream processing fails.

With the defaults, the base retry delays grow from `1s` to `2s`, `4s`, and so on. Jitter varies the actual wait; the initial wait is approximately `0.5–1.5s`. The actual delay never exceeds `backoff.maxInterval`.

Shutdown cancellation interrupts the polling loop and its waits.

Retry backoff applies to runtime cycles. Startup preflight failures stop collector startup.

## Update and removal behavior

When the source is used with the `flightctl` destination, each successfully collected snapshot defines the complete desired catalog content for its pipeline.

The destination attempts the following changes, subject to Flight Control API protections:

| Change in selected registry content | Effect in Flight Control |
|---|---|
| A new model meets the import requirements | A CatalogItem is created. |
| A new version meets the import requirements | The version is added to the existing CatalogItem. |
| A mapped metadata value changes | The corresponding CatalogItem field is updated. Provider changes follow the owner/provider precedence described above. |
| A version stops matching the version filter, while other versions remain selected | The destination attempts to remove the version. If a device or fleet references it, the API rejects the entire CatalogItem update. |
| A model is deleted, stops matching the model filter, or has no matching versions | The destination attempts to delete its CatalogItem. If a device or fleet references one of its versions, the API rejects deletion. |
| A selected version violates artifact eligibility or mapping rules | Collection fails without delivering a snapshot. That attempt causes no destination writes or pruning. |
| The mapped catalog content is unchanged | The revision remains unchanged. Reconciliation still runs to repair drift, while resources that already match are left unchanged. |

Archiving requests removal only when it excludes content from selection. The default `state='LIVE'` filters exclude archived models and versions. Publication-property filters can retain archived content while it remains selected.

Synchronization is not transactional: earlier successful writes or deletions are not rolled back after a failure. Pruning starts only after all desired writes succeed and the complete managed-resource lists are retrieved and validated. A failure before pruning prevents deletion; a failure during pruning stops the remaining deletions.

Runtime failures trigger retry backoff. Each retry collects a fresh snapshot and attempts synchronization again.

### Retaining versions that are in use

The Flight Control API returns `409 Conflict` when an update removes or modifies a CatalogItem version referenced by a device or fleet, or when deletion would remove a CatalogItem containing referenced versions.

The referenced content remains in Flight Control, but synchronization keeps failing while the desired snapshot excludes it. An update adding a replacement version to the same CatalogItem can also fail if it simultaneously removes the referenced version.

Retire a version in the following order:

1. Keep the old version selected, with its published version entry unchanged.
2. Publish the replacement version and let synchronization succeed.
3. Update fleet and device specifications to reference the replacement. Wait until no fleet or device specification references the old version.
4. Exclude the old version from selection, for example by removing its publication property or archiving it when using the default filters.

If synchronization is already blocked, restore the referenced content to selection with its eligible artifact and original image reference intact. With the default filters, restore the relevant model and version to `LIVE`. Alternatively, adjust the filters to include them and restart the collector.

Once synchronization succeeds, follow the retirement sequence above.

For management labels, ownership protections, and detailed pruning rules, see [Flight Control destination](../../references/catalog-collector.md#flightctl).

## Configuring the source

Start with the Model Registry example supplied with your collector deployment. Adapt its source configuration to your registry:

```yaml
sources:
  kubeflowmodelregistry/rhoai:
    endpoint: https://model-registry.example.com
    catalog: ai-models
    auth:
      authenticator: bearertokenauth/model-registry
    certificateAuthority: /etc/flightctl/catalog-collector/certs/model-registry-ca.crt
```

Set `endpoint` to the registry base URL. The collector appends the Model Registry API path. The URL must not contain user information, query parameters, a `#` character, or surrounding whitespace.

Set `catalog` to the name you want to publish in Flight Control. You can choose that name directly; a `catalogname` processor is optional.

The `auth.authenticator` value must reference an authentication extension declared under `extensions`. Use credentials authorized to read the selected models, versions, and artifacts. Configuring authentication requires an HTTPS endpoint.

For Red Hat OpenShift AI, the token must identify an account authorized to access the model registry. For ServiceAccount permissions, token mounting, and credentials for a registry in another cluster, see [Authorizing against Red Hat OpenShift AI](../../../../deploy/helm/flightctl-catalog-collector/README.md#authorizing-against-red-hat-openshift-ai) in the collector Helm chart README. For a collector running on a standalone host, use a credential accepted by the registry and arrange rotation before it expires.

Set `certificateAuthority` when the registry certificate requires a custom CA bundle. Omit it to use the system trust store. File paths must be accessible inside the collector container.

Reference this source in a pipeline with the `flightctl` destination. Adapt the filters as described in [Selecting what to import](#selecting-what-to-import).

For complete deployment examples, credential and certificate mounts, and instructions for applying configuration changes, see [Installing the catalog collector](../../installing/installing-catalog-collector.md). For all source settings and defaults, see [Kubeflow Model Registry source](../../references/catalog-collector.md#kubeflowmodelregistry).

## Verifying the import

After a synchronization cycle completes, use the Flight Control CLI to check the imported content. Authenticate against the target service and organization first.

1. List the imported catalog items:

   ```console
   flightctl get catalogitems --catalog ai-models
   ```

2. Inspect an imported item:

   ```console
   flightctl get catalogitems <item_name> --catalog ai-models -o yaml
   ```

   Replace `<item_name>` with a name from the list. Confirm that `spec.type` is `data` and that the versions, repository, and digests match the selected registry content.

Replace `ai-models` with your published catalog name if it differs.

To use an imported version in a device or fleet specification, reference it through `catalogItemRef` in an image-based application volume. See [Application volumes](../managing-catalogs.md#application-volumes) for an example.

For collection and synchronization monitoring, see [Metrics](../../references/catalog-collector.md#metrics).

## Troubleshooting

| Symptom | Check or action |
|---|---|
| The collector never becomes ready and logs a preflight failure | Identify the failing component in the log. Check connectivity, CA bundles, credentials, and list permissions. For the registry source, also check registry access and filter syntax. |
| The collector is ready, but the expected catalog is missing | Readiness does not confirm a successful import. Check collection and synchronization errors, and confirm the target organization and published catalog name. |
| The catalog exists, but contains no items | Check both selection filters. A successful collection produces no items when no models are selected or the selected models have no matching versions. |
| Two model names normalize to the same name | Case folding, character replacement, or truncation produced a collision. Rename one of the models in the registry. |
| Artifact eligibility or URI validation fails | Each selected version must have exactly one eligible model artifact with a valid, digest-pinned OCI reference. Check [Artifact eligibility and OCI references](#artifact-eligibility-and-oci-references). |
| Versions point at different OCI repositories | All selected versions of one model must use the same repository. Correct the artifact references or publish them as separate models. |
| A version name is rejected | Rename the version in the registry to a name accepted by Flight Control. The source does not rewrite version names. See [Version names](#version-names). |
| Synchronization reports a conflict naming versions in use | The snapshot removes or modifies a referenced version, or omits its CatalogItem. Restore the referenced content unchanged, then follow [Retaining versions that are in use](#retaining-versions-that-are-in-use). |
| Catalog items disappear unexpectedly | Check whether deletion, archiving, or filter changes excluded them from a successful snapshot. See [Update and removal behavior](#update-and-removal-behavior). |

## Further reading

* [Catalog collector overview](overview.md)
* [Catalog collector configuration reference](../../references/catalog-collector.md)
* [Installing the catalog collector](../../installing/installing-catalog-collector.md)
* [Software catalog](../managing-catalogs.md)