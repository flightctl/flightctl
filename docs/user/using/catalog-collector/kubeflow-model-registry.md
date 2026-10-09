# Importing a Kubeflow Model Registry

The catalog collector ships a built-in source that polls a Kubeflow Model Registry, as exposed by Red Hat OpenShift AI, and mirrors its registered models into a Flight Control catalog. Devices can then reference those models through a catalog item reference instead of a hard-coded image digest.

This guide describes how the source maps registry content onto Flight Control resources, how to configure it, and what happens when the registry changes. For the configuration model itself, see [Catalog collector overview](overview.md).

## How the source works

On every cycle the source performs the following steps:

1. Lists every page of registered models that match the model filter.
2. Lists every page of versions of each model that match the version filter.
3. Lists every page of artifacts of each version.
4. Selects exactly one eligible artifact per version.
5. Normalizes the result into one Catalog and one CatalogItem per model, and computes a deterministic revision for the snapshot.

Any error aborts the cycle. No partial snapshot is produced, so a registry outage never prunes catalog content that is still valid. The source then waits according to its bounded exponential backoff and tries again.

## Mapping registry content to catalog resources

The source emits one Catalog, named by the `catalog` configuration field, and one CatalogItem per eligible registered model.

### Catalog

| Catalog field | Value |
|---|---|
| `metadata.name` | The `catalog` field from the source configuration |
| `spec.displayName` | The same value |

Use the `catalogname` processor to publish under a different name, so that the Flight Control side does not change when the upstream registry is renamed or replaced.

### Catalog item

| CatalogItem field | Value |
|---|---|
| `metadata.name` | The registered model name, normalized to a DNS subdomain |
| `metadata.catalog` | The configured catalog name |
| `spec.type` | `data` |
| `spec.category` | `application` |
| `spec.displayName` | The registered model name, unchanged |
| `spec.shortDescription` | The registered model description, when it is set and not blank |
| `spec.provider` | The model owner, or the model provider when no owner is set |
| `spec.artifacts` | One entry of type `container` whose `uri` is the version-less OCI repository |
| `spec.versions` | One entry per eligible model version |

Each version entry has:

| Version field | Value |
|---|---|
| `version` | The model version name, unchanged |
| `channels` | `["stable"]` |
| `references.container` | The immutable `sha256` digest of that version's artifact |

The source does not produce `replaces`, `skips`, or `skipRange` edges. Versions are published as independent nodes in the `stable` channel.

### Name normalization

A registered model name is free-form text, while `metadata.name` must be a DNS subdomain. The source normalizes the name using these rules:

1. Uppercase ASCII letters become lowercase.
2. Lowercase ASCII letters and digits are preserved.
3. Dots remain label boundaries.
4. Every run of other characters, including every non-ASCII character, becomes a single hyphen inside its label. Non-ASCII text is never transliterated.
5. Empty labels are dropped, and leading and trailing hyphens are stripped.
6. Each label is limited to 63 bytes and the whole name to 253 bytes.

The original name is always preserved in `spec.displayName`.

A name that contains no ASCII letters or digits normalizes to nothing and fails the cycle. Two model names that normalize to the same result also fail the cycle, with an error naming both models. Rename one of them in the registry to resolve the collision.

### Version names

Version names are not normalized. A version name that the Flight Control API does not accept as a catalog item version fails the cycle, with an error naming the model and the version. Rename it in the Model Registry rather than relying on the collector to rewrite it.

Versions are published in precedence order rather than in the order the registry returns them, so that the snapshot revision does not depend on pagination. Build metadata is ignored for ordering, and `1.0` and `1.0.0` compare as equal, with the original string used as a tie-breaker. The published version string is never rewritten.

Two versions of one model with the same name fail the cycle.

## Artifact eligibility and OCI references

A model version contributes a catalog item version only when exactly one of its artifacts is eligible. An artifact is eligible when all of the following hold:

* Its `artifactType` is `model-artifact`.
* Its lifecycle state is `LIVE`, or the state is absent. An absent state is treated as `UNKNOWN`, which is accepted because model-car artifacts created by Red Hat OpenShift AI omit the field. Other states, such as `ABANDONED`, are ignored.
* Its URI is set and not blank.

The URI must be an immutable, digest-pinned OCI reference:

| Rule | Detail |
|---|---|
| Scheme | An `oci://` prefix is accepted and removed. Any other scheme is rejected. |
| Digest | Exactly one `@` separator, followed by `sha256:` and exactly 64 lowercase hexadecimal characters. |
| Tag | Allowed only when the reference is also pinned by digest. The tag is discarded. |
| Form | The whole value must be a fully qualified OCI image reference. |

The repository part is stored once in `spec.artifacts[0].uri`, and each version's digest is stored in `references.container`. All eligible versions of one model must therefore use the same repository; a model whose versions point at different repositories fails the cycle.

A version with no eligible artifact, or with more than one, is an actionable data error and fails the cycle. A model-artifact in an eligible state with a malformed URI also fails the cycle. Error messages never include the URI itself, because an invalid URI may contain secret material.

## Selecting what to import

Both filters are sent to the registry as server-side `filterQuery` values, so filtered-out content never reaches the collector.

| Field | Default | Effect |
|---|---|---|
| `selection.modelFilter` | `state='LIVE'` | Applied to the registered-models endpoint. |
| `selection.versionFilter` | `state='LIVE'` | Applied to the model-versions endpoint. |

Omit the `selection` block to keep the defaults. Set a filter to the empty string to send no filter for that endpoint and import everything it returns.

A registered model whose versions are all filtered out is omitted from the snapshot instead of failing the cycle. This covers both a model with no versions and a model whose versions are all archived. Filtering is therefore not the same as ineligibility: a version the registry does not return is simply absent, while a version the registry does return but that carries no eligible artifact is a data error that fails the cycle.

The source validates both filters during startup, before the collector reports ready, by issuing one query against each endpoint. A filter the registry rejects therefore fails fast rather than on the first poll.

## Polling and failure handling

| Field | Default | Description |
|---|---|---|
| `pollInterval` | `5m` | Wait between successive successful cycles. |
| `requestTimeout` | `30s` | Bounds a single HTTP request. |
| `collectionTimeout` | `5m` | Bounds a complete cycle, including pagination and normalization. |
| `pageSize` | `100` | Items per paginated request. |
| `backoff.initialInterval` | `1s` | Delay after the first failed cycle. |
| `backoff.maxInterval` | `5m` | Upper bound on the retry delay, including jitter. |
| `backoff.multiplier` | `2` | Growth factor after each consecutive failure. |
| `backoff.randomizationFactor` | `0.5` | Symmetric jitter around the base interval. |

The first cycle runs immediately. Cycles never overlap. After a cycle collects and reconciles successfully, backoff resets and the source waits `pollInterval`. Any collection failure or downstream failure advances backoff instead.

## Update and removal behavior

The snapshot carries the complete desired state of the configured catalog, so Flight Control follows the registry:

| Change in the registry | Effect in Flight Control |
|---|---|
| A new registered model becomes eligible | A new CatalogItem is created on the next cycle. |
| A new version is registered with an eligible artifact | The version is added to the existing CatalogItem. |
| A model description, owner, or provider changes | The corresponding CatalogItem field is updated. |
| A version is archived, or stops matching the version filter | The version is removed from the CatalogItem. If a device or fleet uses that version, the API rejects the whole update and the cycle fails. |
| A model is archived, deleted, or has no versions left that match the version filter | The CatalogItem is deleted. The model is omitted from the snapshot rather than treated as an error, because the version filter is applied by the registry and the source simply receives no versions for it. If a device or fleet uses one of its versions, the API rejects the deletion and the cycle fails. |
| A version still matches the version filter but has no eligible artifact, or more than one | The whole cycle fails during collection. No snapshot is produced, so nothing is written and nothing is pruned; the previous content stays in place. This is a data error in the registry, not a removal. |
| Nothing changes | The snapshot repeats the previous revision. Reconciliation still runs and still repairs drift, but a resource that already matches is not rewritten. |

Deletion is limited to resources that the same pipeline created, identified by the `flightctl.io/managed-by` and `flightctl.io/catalog-collector-pipeline` labels. Catalogs and catalog items that carry an owner, such as those created by a ResourceSync, are never touched, and neither is a resource created by hand under a different name. See [Ownership and pruning](overview.md#ownership-and-pruning).

### Collection and reconciliation fail differently

A cycle has two halves, and a failure in each has a different effect. Read the log line to tell them apart: the source reports `collection failed`, and the destination reports the resource it could not write.

| Failure | Effect |
|---|---|
| Collection fails: the registry is unreachable, a filter is rejected, or normalization finds a data error | No snapshot is produced, so no destination runs. Nothing is written and nothing is pruned, and the previous content stays in place. |
| Reconciliation fails: the Flight Control API rejects or does not answer a write | Writes that already succeeded stay committed. Reconciliation is not transactional, so the catalog can be left part-way between the old and the new desired state. |

Pruning is ordered to be safer than the writes, but it is not protected from everything. The destination begins deleting only after every desired write has succeeded and both complete lists of managed resources have been retrieved, so a failure in any of those steps deletes nothing at all. Once deletion starts, the deletions are issued one at a time: if one fails, the deletions already made stay applied and the remainder are abandoned.

Either failure advances the backoff and the source retries. A retry re-sends the complete desired state, so a partially applied reconciliation converges on the next successful cycle.

### Content that is in use cannot be removed

The Flight Control API refuses to delete a catalog item, and refuses an update that changes or removes one of its versions, while a device or a fleet still references that version. It answers with a conflict naming the affected versions, and it rejects the whole request, not only the part that touches the version in use.

Archiving such a model in the registry therefore does not make it disappear from Flight Control. The next cycle produces a snapshot without it, the destination asks for the deletion, the API refuses, and the cycle fails and is retried. The catalog item stays, carrying the content of the last successful cycle, until the references are removed.

> [!IMPORTANT]
> Check which devices and fleets reference a catalog item before archiving its model. Until those references are removed, the archived model fails the reconciliation of every cycle for that pipeline, which also abandons the pruning of anything else the same cycle wanted to remove.

## Configuring the source

The following configuration polls a model registry, renames the catalog, and writes to Flight Control:

```yaml
service:
  logLevel: info
  metrics:
    endpoint: 0.0.0.0:8888

extensions:
  healthcheck:
    endpoint: 0.0.0.0:13133
    livePath: /livez
    readyPath: /readyz

  bearertokenauth/model-registry:
    tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token

  oauth2client/flightctl:
    clientIdFile: /etc/flightctl/catalog-collector/oauth/client-id
    clientSecretFile: /etc/flightctl/catalog-collector/oauth/client-secret
    tokenUrl: https://sso.example.com/realms/flightctl/protocol/openid-connect/token
    scopes:
      - openid
    certificateAuthority: /etc/flightctl/catalog-collector/certs/oidc-ca.crt
    timeout: 30s
    expiryBuffer: 30s

sources:
  kubeflowmodelregistry/rhoai:
    endpoint: https://model-registry.example.com
    catalog: rhoai-model-registry
    pollInterval: 5m
    requestTimeout: 30s
    collectionTimeout: 5m
    pageSize: 100
    auth:
      authenticator: bearertokenauth/model-registry
    certificateAuthority: /etc/flightctl/catalog-collector/certs/model-registry-ca.crt
    selection:
      modelFilter: "state='LIVE'"
      versionFilter: "state='LIVE'"
    backoff:
      initialInterval: 1s
      maxInterval: 5m
      multiplier: 2
      randomizationFactor: 0.5

processors:
  catalogname/rename:
    mappings:
      rhoai-model-registry: ai-models

destinations:
  flightctl/service:
    server: https://flightctl.example.com
    auth:
      authenticator: oauth2client/flightctl
    certificateAuthority: /etc/flightctl/catalog-collector/certs/flightctl-ca.crt
    timeout: 30s

pipelines:
  rhoai-models:
    source: kubeflowmodelregistry/rhoai
    processors:
      - catalogname/rename
    destination: flightctl/service
```

The `endpoint` value is the registry base URL only. The collector appends the Model Registry API path itself, so the value must not contain query parameters, user information, or a `#` character.

Configuring `auth` requires an `https` endpoint. The source refuses to send credentials over plain HTTP.

Equivalent ready-to-edit files ship with both deployment forms:

| Deployment | File |
|---|---|
| Helm | `deploy/helm/flightctl-catalog-collector/examples/values-rhoai-to-flightctl.yaml` |
| RPM and Quadlet | `/usr/share/flightctl/flightctl-catalog-collector/examples/config-rhoai-to-flightctl.yaml` |

## Authorizing against Red Hat OpenShift AI

The Red Hat OpenShift AI model registry sits behind an authorizing proxy that validates the bearer token on each request against the Kubernetes API server and then applies RBAC. The collector therefore needs an identity the API server can review, and a role binding that grants it read access.

When the collector runs in the same cluster as the registry, the simplest credential is the pod ServiceAccount token:

1. Let the chart create a named ServiceAccount and turn on the token automount:

    ```yaml
    serviceAccount:
      create: true
      name: flightctl-catalog-collector
      automountServiceAccountToken: true
    ```

2. Bind that ServiceAccount to the reader role in the namespace where the registry runs. The registry ships a `registry-user-<registry_name>` role:

    ```yaml
    apiVersion: rbac.authorization.k8s.io/v1
    kind: RoleBinding
    metadata:
      name: flightctl-catalog-collector-registry-user
      namespace: <model_registry_namespace>
    subjects:
      - kind: ServiceAccount
        name: flightctl-catalog-collector
        namespace: <collector_namespace>
    roleRef:
      apiGroup: rbac.authorization.k8s.io
      kind: Role
      name: registry-user-<model_registry_name>
    ```

3. Point the bearer token extension at the automounted path:

    ```yaml
    extensions:
      bearertokenauth/model-registry:
        tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token
    ```

The kubelet renews the token in place, and the extension re-reads the file on every request, so rotation needs no restart. Leave the token audience at the default; a token projected with a custom audience is rejected.

A registry in a different cluster cannot review the local token. Create the ServiceAccount and the role binding in the registry cluster, issue a token there, deliver it to the collector as a Secret, and point `tokenFile` at the mounted path. A token issued that way is time-bound and nothing renews it, so refresh the Secret on a schedule shorter than the token lifetime.

The full procedure, including verification commands and explicit token projection, is in the [chart README](https://github.com/flightctl/flightctl/blob/main/deploy/helm/flightctl-catalog-collector/README.md).

## Verifying the import

1. Confirm that the collector became ready. This source implements a preflight check that runs before readiness, so a ready collector has already had its token and both of its filters accepted by the Model Registry.

    Readiness says nothing about the Flight Control destination. No preflight runs against a destination, so the collector contacts Flight Control for the first time when it reconciles its first snapshot. A destination credential that the service rejects shows up in the log and in the pipeline sync metrics, not in readiness.

2. Allow at least one poll interval to pass, then check the collection metrics:

    ```console
    curl -s http://127.0.0.1:8888/metrics | grep flightctl_catalogcollector_source
    ```

3. List what arrived:

    ```console
    flightctl get catalogs
    ```

    ```console
    flightctl get catalogitems --catalog ai-models
    ```

4. Reference an imported model from a device or fleet specification:

    ```yaml
    spec:
      applications:
        - name: inference
          appType: compose
          image: quay.io/example/inference-app:v1.0
          volumes:
            - name: model
              image:
                catalogItemRef:
                  catalog: ai-models
                  item: object-detector
                  version: "1.1.0"
                pullPolicy: IfNotPresent
    ```

An application volume source is the only place these items can be referenced. This source sets `spec.type` to `data` on every catalog item it creates, and a volume source is the one position that accepts a `data` item. The other two positions that take a `catalogItemRef`, the OS image and an application source, require an item whose type is `os` or matches the application type. See [Referencing catalog items in device specifications](../managing-catalogs.md#referencing-catalog-items-in-device-specifications) for all three positions and the type each one requires.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| The collector never becomes ready and the log reports a preflight failure | The registry rejected the credential or a selection filter. Check the role binding and the filter syntax. |
| The collector is ready, but no catalog appears | The first cycle failed, or every model was filtered out. Read the log and the collection metrics. |
| An error names two models that normalize to the same name | Two registered model names differ only in characters that normalization removes. Rename one of them. |
| An error reports no eligible model artifact | The version has no artifact pinned by an immutable `sha256` digest, or the only candidate is in an ineligible lifecycle state. |
| An error reports different OCI repositories | Versions of one model point at different repositories. All versions of a model must share one repository. |
| Catalog items disappear unexpectedly | A cycle succeeded with a smaller result set, for example because models were archived or a filter was narrowed. |

## Further reading

* [Catalog collector overview](overview.md)
* [Catalog collector configuration reference](../../references/catalog-collector.md)
* [Installing the catalog collector](../../installing/installing-catalog-collector.md)
* [Software catalog](../managing-catalogs.md)
