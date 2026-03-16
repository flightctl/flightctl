# Software catalog

Flight Control uses a software catalog to organize and distribute versioned software components for edge devices. The catalog provides a structured way to publish applications, system images, and other deployable content, track their versions, and define upgrade paths between them.

> [!NOTE]
> Software catalog is an alpha-stage feature available under the `v1alpha1` API version. The API may change in future releases.

The catalog consists of two types of resources:

* **Catalog**: A named grouping of catalog items. For example, you might have a catalog called `platform-apps` for core platform applications and another called `infrastructure` for infrastructure components.
* **CatalogItem**: A versioned entry within a catalog representing an application, system image, or other deployable component. Each item defines its type, artifacts, configuration, and one or more versions with upgrade paths.

## Catalogs

A Catalog resource defines a named grouping for catalog items.

### Catalog specification

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Catalog
metadata:
  name: platform-apps
  labels:
    team: platform
spec:
  displayName: Platform Apps
  shortDescription: Core platform applications
```

The `spec` supports the following optional fields:

| Field | Description |
| ------------------ | ---------------------------------------------------------------- |
| `displayName` | Human-readable name shown in catalog listings |
| `shortDescription` | A brief one-line description of the catalog |
| `icon` | URL or data URI of the catalog icon for display in the UI |
| `provider` | Provider or publisher of the catalog (company or team name) |
| `support` | Link to support resources |

### Creating a catalog

To create a catalog, save the catalog definition to a file and apply it:

```console
flightctl apply -f catalog.yaml
```

To verify the catalog was created:

```console
flightctl get catalogs
```

## Catalog items

A CatalogItem resource defines a versioned software component within a catalog. It specifies the item type, artifacts, versions, and optional metadata.

### Categories and types

Each catalog item has a category and a type. The category determines the broad class of the item, while the type specifies its format.

**Categories:**

| Category | Description |
| ------------- | ---------------------------------------------------- |
| `application` | Application workloads (default) |
| `system` | System-level components such as OS images or drivers |

**Types:**

| Type | Category | Description |
| -------------- | ------------- | ----------------------------------------- |
| `os` | `system` | Operating system image |
| `firmware` | `system` | Firmware update |
| `driver` | `system` | Device driver |
| `container` | `application` | Container image |
| `helm` | `application` | Helm chart |
| `quadlet` | `application` | Podman Quadlet unit |
| `compose` | `application` | Docker Compose or Podman Compose manifest |
| `data` | `application` | Data or configuration payload |

### Artifacts

Each catalog item defines one or more artifacts that represent the deliverable content. An artifact has a type and a URI, but does not include a version qualifier. The version-specific tag or digest is resolved at deployment time through the `references` field in each version entry.

Supported artifact types: `container`, `qcow2`, `ami`, `iso`, `anaconda-iso`, `vmdk`, `vhd`, `raw`, `gce`.

### Versions and channels

Each version entry in a catalog item includes:

* **`version`**: A semantic version identifier (for example, `1.8.0` or `2.0.0-rc1`).
* **`channels`**: A list of channels this version belongs to (for example, `stable`, `candidate`). Channels are labels on version nodes that describe the update paths a version participates in.
* **`references`**: A map of artifact type to image tag or digest. Keys must match a type defined in `spec.artifacts`.

A version can belong to multiple channels simultaneously. For example, a version might appear in both the `candidate` and `stable` channels, indicating it is part of the update path for both.

#### Cincinnati update protocol

Flight Control uses the [Cincinnati update protocol](https://github.com/openshift/cincinnati) to model version graphs for catalog items. In this model, each version is a node in an upgrade graph and channels are labels on those nodes. Upgrade edges between versions, combined with channel membership, describe the available update paths.

This approach gives you a declarative way to describe how versions relate to each other and which update paths exist.

#### Upgrade edges

Upgrade edges define how versions relate to each other in the upgrade graph. Flight Control supports three mechanisms:

* **`replaces`**: Defines the single version that this version directly replaces, forming a linear chain. A device must follow the chain one step at a time unless `skips` or `skipRange` provides a shortcut.
* **`skips`**: A list of specific older versions that can upgrade directly to this version, bypassing intermediate steps in the chain.
* **`skipRange`**: A semantic version range (for example, `>=1.0.0 <1.3.0`) of older versions that can upgrade directly to this version. Use `skipRange` instead of `skips` when you want to cover a broad set of prior versions without listing each one.

The following examples show each mechanism. For brevity, only the `versions` section is shown.

##### `replaces` example

```yaml
versions:
  - version: "1.0.0"
    channels:
      - stable
    references:
      container: "1.0.0"
  - version: "1.1.0"
    channels:
      - stable
    references:
      container: "1.1.0"
    replaces: "1.0.0"
  - version: "1.2.0"
    channels:
      - stable
    references:
      container: "1.2.0"
    replaces: "1.1.0"
```

Upgrade path: `1.0.0` → `1.1.0` → `1.2.0`. A device on `1.0.0` must first upgrade to `1.1.0` before it can reach `1.2.0`.

##### `skips` example

```yaml
versions:
  - version: "1.0.0"
    channels:
      - stable
    references:
      container: "1.0.0"
  - version: "1.1.0"
    channels:
      - stable
    references:
      container: "1.1.0"
    replaces: "1.0.0"
  - version: "1.2.0"
    channels:
      - stable
    references:
      container: "1.2.0"
    replaces: "1.1.0"
  - version: "1.3.0"
    channels:
      - stable
    references:
      container: "1.3.0"
    replaces: "1.2.0"
    skips:
      - "1.0.0"
      - "1.1.0"
```

Version `1.3.0` replaces `1.2.0` (the linear chain) and also lets devices on `1.0.0` or `1.1.0` jump directly to `1.3.0`.

##### `skipRange` example

```yaml
versions:
  - version: "1.0.0"
    channels:
      - stable
    references:
      container: "1.0.0"
  - version: "1.0.1"
    channels:
      - stable
    references:
      container: "1.0.1"
    replaces: "1.0.0"
  - version: "1.1.0"
    channels:
      - stable
    references:
      container: "1.1.0"
    replaces: "1.0.1"
  - version: "1.2.0"
    channels:
      - stable
    references:
      container: "1.2.0"
    replaces: "1.1.0"
    skipRange: ">=1.0.0 <1.2.0"
```

Version `1.2.0` replaces `1.1.0` and also lets any version matching `>=1.0.0 <1.2.0` (that is, `1.0.0`, `1.0.1`, and `1.1.0`) upgrade directly to `1.2.0`.

### Catalog item specification

The following example defines a Keycloak catalog item with two versions:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: CatalogItem
metadata:
  name: keycloak
  catalog: infrastructure
  labels:
    category: security
spec:
  type: container
  displayName: Keycloak
  shortDescription: Open source identity and access management
  artifacts:
    - type: container
      uri: quay.io/keycloak/keycloak
  versions:
    - version: "24.0.4"
      references:
        container: "24.0.4"
      channels:
        - stable
    - version: "25.0.1"
      references:
        container: "25.0.1"
      channels:
        - stable
        - candidate
      replaces: "24.0.4"
```

### Required fields

* **`spec.type`**: The type of catalog item (see [Categories and types](#categories-and-types)).
* **`spec.artifacts`**: At least one artifact definition. Each artifact must have a unique `type` and a `uri` without a version qualifier.
* **`spec.versions`**: At least one version entry. Each version must include `version`, `channels`, and `references`.
* **`metadata.catalog`**: The name of the catalog this item belongs to. This field is immutable after creation.

### Optional fields

| Field | Description |
| ------------------ | --------------------------------------------------------------------- |
| `displayName` | Human-readable name shown in catalog listings |
| `shortDescription` | A brief description of the item |
| `icon` | URL or data URI of the item icon |
| `provider` | Provider or publisher name |
| `support` | Link to support resources |
| `homepage` | Link to project's homepage |
| `documentationUrl` | Link to external documentation |
| `category` | `application` (default) or `system` |
| `defaults` | Default configuration values that can be overridden per version |
| `deprecation` | Deprecation information including a message and optional replacement |

### Configurable defaults

Catalog items support configuration fields at the item level that serve as defaults across all versions. Versions can override these defaults; version-level values fully replace item-level values.

The configurable fields include:

* **`config`**: Configuration values such as environment variables, ports, volumes, and resource limits.
* **`configSchema`**: A JSON Schema defining configurable parameters and their validation rules.
* **`readme`**: Detailed documentation, preferably in Markdown format.

### Creating a catalog item

To create a catalog item, save the item definition to a file and apply it:

```console
flightctl apply -f catalog-item.yaml
```

To list all items in a specific catalog:

```console
flightctl get catalogitems -c <catalog_name>
```

## Importing catalogs using ResourceSync

Instead of creating catalogs and catalog items individually, you can store their definitions in a Git repository and import them automatically using a ResourceSync resource. Flight Control periodically polls the repository and synchronizes any changes, making Git the single source of truth for your catalog definitions.

### How catalog synchronization works

1. You store Catalog and CatalogItem YAML definitions in a Git repository.
2. You create a ResourceSync resource with `spec.type` set to `catalog`, pointing to the repository and path containing the definitions.
3. Flight Control periodically clones the repository, reads the YAML files from the specified path, and creates, updates, or deletes catalogs and catalog items to match the repository contents.
4. The Catalogs and CatalogItems that are synchronized are marked as being managed by the ResourceSync. Such elements cannot be modified directly using the Flight Control API, CLI or UI. All modifications must be done exclusively on the git repository's resource definitions.

> [!IMPORTANT]
> A ResourceSync with `type: catalog` only accepts Catalog and CatalogItem resources. If the specified path contains other resource types, the synchronization reports an error.

### Prerequisites

* A Git repository accessible from the Flight Control service. See [Managing Repositories](managing-devices.md#getting-configuration-from-a-git-repository) for how to configure repository access.
* A Repository resource configured in Flight Control that points to your Git repository.

### Setting up the repository structure

Organize your catalog definitions in a directory within the Git repository. Place the Catalog resource and its CatalogItem resources together. For example:

```text
catalogs/
  infrastructure/
    catalog.yaml
    app-1.yaml
    app-2.yaml
    os.yaml
```

The `catalog.yaml` file defines the Catalog resource:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: Catalog
metadata:
  name: infrastructure
  labels:
    team: platform
spec:
  displayName: Infrastructure
  shortDescription: Core infrastructure applications for edge devices
```

Each additional YAML file defines a CatalogItem belonging to that catalog:

```yaml
apiVersion: flightctl.io/v1alpha1
kind: CatalogItem
metadata:
  name: prometheus
  catalog: infrastructure
  labels:
    category: monitoring
spec:
  type: container
  displayName: Prometheus Node Exporter
  shortDescription: Hardware and OS metrics exporter
  artifacts:
    - type: container
      uri: quay.io/prometheus/node-exporter
  versions:
    - version: "1.7.0"
      references:
        container: "v1.7.0"
      channels:
        - stable
    - version: "1.8.0"
      references:
        container: "v1.8.0"
      channels:
        - stable
        - candidate
      replaces: "1.7.0"
```

### Creating a ResourceSync for catalogs

To import catalogs from a Git repository, create a ResourceSync resource with `spec.type` set to `catalog`:

```yaml
apiVersion: flightctl.io/v1beta1
kind: ResourceSync
metadata:
  name: infrastructure-catalog-sync
spec:
  repository: my-config-repo
  path: /catalogs/infrastructure
  targetRevision: main
  type: catalog
```

| Field | Description |
| ---------------- | -------------------------------------------------------------------------------------------------------------- |
| `repository` | The name of the Repository resource configured in Flight Control |
| `path` | The path to a file or directory in the repository containing Catalog and CatalogItem YAML definitions |
| `targetRevision` | The Git branch, tag, or commit to track |
| `type` | Must be `catalog` for catalog synchronization. If omitted, defaults to `fleet` |

Apply the ResourceSync:

```console
flightctl apply -f resourcesync.yaml
```

### Monitoring synchronization status

The ResourceSync resource reports its status through conditions. To check the synchronization status:

```console
flightctl get resourcesyncs
```

For detailed status information:

```console
flightctl get resourcesync infrastructure-catalog-sync -o yaml
```

The status conditions include:

| Condition | Description |
| ---------------- | ------------------------------------------------------------------- |
| `Accessible` | Whether the repository is reachable |
| `ResourceParsed` | Whether the YAML files were parsed without errors |
| `Synced` | Whether the synchronization completed and resources are up to date |

### Updating catalogs through Git

Because Git is the source of truth, you update catalogs by committing changes to the repository:

* **Adding a new catalog item**: Add a new YAML file to the repository path and commit. On the next sync cycle, Flight Control creates the catalog item.
* **Updating a version**: Edit the catalog item YAML to add a new version entry or modify channels, then commit. Flight Control updates the item on the next sync.
* **Removing a catalog item**: Delete the YAML file from the repository and commit. Flight Control deletes the catalog item that is no longer present in the repository.
* **Adding a version to an update path**: Add a channel to a version that was not previously part of that update path, then commit. The updated version graph takes effect on the next sync.

> [!NOTE]
> Flight Control checks for updates periodically. Changes in the Git repository are not reflected immediately but on the next synchronization cycle.
