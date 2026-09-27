# Workload image digest collection plan

**Status:** Implementation and focused verification are complete. Synchronize the parent branch and rebase its descendants before rollout.
**Target:** The current application-image-digests parent branch in the stack.
**Purpose:** Track the fix for CodeRabbit comment 1 and make its implementation choices explicit.

## Scope and current behavior

The existing `ApplicationImageDigest` API entry already has the required `image` and `digest` fields. This fix should enrich the data sent by the agent; it should not require OpenAPI edits or regenerated API types.

The current status path has three gaps:

- `application.collectImageDigests` reports the parent application image with its digest. It can report a workload image with an empty digest when `Workload.Image` is populated, but both monitors currently leave that field empty.
- The Kubernetes monitor records pod phase and restart counts. Its event model omits the pod's container image references and image IDs.
- Podman events carry an image reference, but `updateApplicationStatus` does not copy it to the tracked `Workload`.

The delta worker skips entries without a digest. Even after workload image references are reported, the current repository-to-digest index stores only one digest per repository. A pod with multiple containers can therefore expose another data-loss case if two current images come from the same repository.

The data flow after the fix should be:

```text
Kubernetes Pod spec/status ─┐
                            ├─ Workload.Images ─ collectImageDigests ─ DeviceApplicationStatus.ImageDigests
Podman event/image inspect ─┘                                      └─ delta worker candidates
```

## Proposed implementation

### 1. Represent multiple images per workload

Keep one status `Workload` per existing unit: one Podman container or one Kubernetes pod. Add an image list because one pod may run multiple containers.

```go
type WorkloadImage struct {
    Image  string
    Digest string
}

type Workload struct {
    ID       string
    Name     string
    Status   StatusType
    Restarts int

    // Images contains the image references used by this workload.
    // Kubernetes pods may contribute more than one entry.
    Images []WorkloadImage

    RequiresHealth bool
}
```

Replace the current scalar `Workload.Image` with `Workload.Images`. The scalar is only consumed by `collectImageDigests`, so this should remain a localized model change.

Keep `Workload.Images` as a slice rather than a map. It is a snapshot of the references observed in a pod or container. A map keyed only by image reference would overwrite two different digests for the same mutable tag. Do not add deduplication logic to each monitor; perform it once when building application status so parent and workload images follow the same rule.

`Workloads()` currently copies the workload slice by value. Once a nested image slice is added, clone that slice too:

```go
func (a *application) Workloads() []Workload {
    result := make([]Workload, len(a.workloads))
    for i := range a.workloads {
        result[i] = a.workloads[i]
        result[i].Images = slices.Clone(a.workloads[i].Images)
    }
    return result
}
```

This preserves the existing `CopyWorkloadsFrom` behavior without sharing the new slice backing array between old and replacement application objects. Review `AddWorkload` for the same ownership rule if callers begin reusing or mutating the input image slice.

### 2. Capture Kubernetes image references and digests

Extend the small Pod event model in `kubernetes_monitor.go` with pod spec image references and the status fields needed to resolve them:

```go
type kubernetesPod struct {
    // Existing metadata...
    Spec   kubernetesPodSpec   `json:"spec"`
    Status kubernetesPodStatus `json:"status"`
}

type kubernetesPodSpec struct {
    Containers     []kubernetesContainerSpec `json:"containers,omitempty"`
    InitContainers []kubernetesContainerSpec `json:"initContainers,omitempty"`
}

type kubernetesContainerSpec struct {
    Name  string `json:"name"`
    Image string `json:"image"`
}

type kubernetesPodStatus struct {
    Phase                  string                      `json:"phase"`
    ContainerStatuses      []kubernetesContainerStatus `json:"containerStatuses,omitempty"`
    InitContainerStatuses  []kubernetesContainerStatus `json:"initContainerStatuses,omitempty"`
}

type kubernetesContainerStatus struct {
    Name         string `json:"name"`
    ImageID      string `json:"imageID,omitempty"`
    RestartCount int    `json:"restartCount"`
    // Existing readiness and state fields...
}
```

Use the Pod spec as the `Image` value because the API contract describes the rendered image reference. Join status entries to spec entries by container name and use `ImageID` only to resolve the content digest. Include regular and init containers; the existing OCI target collection already considers init-container images.

The collector should return a complete snapshot for each pod event:

```go
func collectPodImages(pod *kubernetesPod) []WorkloadImage {
    statuses := make(map[string]kubernetesContainerStatus)
    for _, status := range pod.Status.ContainerStatuses {
        statuses[status.Name] = status
    }
    for _, status := range pod.Status.InitContainerStatuses {
        statuses[status.Name] = status
    }

    containers := append(
        slices.Clone(pod.Spec.InitContainers),
        pod.Spec.Containers...,
    )
    images := make([]WorkloadImage, 0, len(containers))
    for _, container := range containers {
        status := statuses[container.Name]
        images = append(images, WorkloadImage{
            Image:  container.Image,
            Digest: digestFromKubernetesImageID(container.Image, status.ImageID),
        })
    }
    return images
}
```

Assign the result for both new and existing workloads in `updatePodStatus`. Replacing the snapshot prevents images from a previous pod spec from lingering. If a digest is not available in the current event, keep the image reference with an empty digest; this preserves the existing full-pull fallback.

The digest parser must accept only a digest that is useful as a registry image digest:

```go
func digestFromKubernetesImageID(imageRef, imageID string) string {
    if digest, ok := digestFromReference(imageRef); ok {
        return digest // An immutable rendered reference is already authoritative.
    }

    pullableRef, ok := parsePullableImageID(imageID)
    if !ok || !sameNormalizedRepository(imageRef, pullableRef.Image) {
        return "" // Opaque runtime IDs are not safe delta source digests.
    }
    return pullableRef.Digest
}
```

Implement `parsePullableImageID` with the repository's existing OCI reference parser. It should recognize a pullable reference such as `docker-pullable://registry.example/app@sha256:...`. Treat runtime-only forms such as `containerd://sha256:...` as unknown unless verification shows that the value is the registry manifest digest used by the delta worker. Do not make a remote registry call from the Kubernetes watch handler.

### 3. Capture Podman event images and resolve digests

Podman events already provide `event.Image`. Store it on the container workload. For tag references, use the existing `Podman.ImageDigest` helper to read the content digest from local Podman storage.

```go
func resolvePodmanWorkloadImage(
    ctx context.Context,
    podman *client.Podman,
    imageRef string,
) WorkloadImage {
    image := WorkloadImage{Image: imageRef}
    if imageRef == "" {
        return image
    }
    if digest, ok := digestFromReference(imageRef); ok {
        image.Digest = digest
        return image
    }

    digest, err := podman.ImageDigest(ctx, imageRef)
    if err != nil {
        // Log the reference and continue reporting workload status.
        return image
    }
    image.Digest = digest
    return image
}
```

Resolve lazily when a workload is first observed, its container ID changes, or its image reference changes. Reuse the Podman client that the Compose status path already creates; create one for the Quadlet path only when resolution is needed. Do not hold the monitor mutex while invoking Podman. A lookup failure must not fail or delay the workload status update beyond the bounded client call; an empty digest keeps delta selection fail-open.

When updating an existing workload, replace the image entry when the event supplies a non-empty image reference. If a status event omits the image, retain the previous entry. The existing remove event should continue deleting the whole workload and its image data.

### 4. Aggregate workload pairs into application status

Update `collectImageDigests` to combine the provider-captured parent image and every workload image. The per-workload slices may include repeated pairs when several containers use the same image. Deduplicate once here: prefer a known digest over an empty digest for the same image reference, preserve distinct known digests for one reference, and sort the final list by image and digest so status output is deterministic.

The wire status intentionally flattens workload images into one `ImageDigests` list per application. The current `DeviceApplicationStatus` API has no workload entries, and the delta worker needs candidate image/digest pairs rather than container-to-image attribution. This preserves the application boundary but loses which workload supplied each pair. If per-workload attribution is required, it needs a separate API design with a nested workload status field; adding that is outside this fix.

A map shaped as `image -> set[digest]` implements those rules: initialize an entry for empty digests, add only non-empty digests to the set, and emit an empty digest only when the set for that image is empty.

```go
byImage := make(map[string]map[string]struct{})

add := func(image, digest string) {
    if image == "" {
        return
    }
    if _, ok := byImage[image]; !ok {
        byImage[image] = make(map[string]struct{})
    }
    if digest != "" {
        byImage[image][digest] = struct{}{}
    }
}

add(a.imageRef, a.imageDigest)
for _, workload := range a.workloads {
    for _, image := range workload.Images {
        add(image.Image, image.Digest)
    }
}
```

This retains multiple observed contents for the same mutable tag during a rolling update, while avoiding a redundant unknown entry when another workload has a known digest for that reference.

### 5. Preserve multiple source digests per repository

Change `buildDigestIndex` in `internal/delta_worker/app_expand.go` from one digest per repository to a unique, deterministic list:

```go
type imageDigestIndex map[string][]string

// For each valid status entry:
repo, err := applicationImageRepository(entry.Image)
if err == nil && entry.Digest != "" {
    index[repo] = appendUnique(index[repo], entry.Digest)
}
```

The current `map[string]string` assignment lets the last status entry silently overwrite earlier containers from the same repository. Make candidate pairing inspect the new image once, then pair that digest with each distinct current digest for the repository:

```go
for _, currentDigest := range digestIndex[repo] {
    if currentDigest == newDigest {
        continue
    }
    candidates = append(candidates, preparetask.DeltaCandidate{
        ImageRepository: repo,
        CurrentDigest:   currentDigest,
        NewDigest:       newDigest,
    })
}
```

Sort the digest lists before pairing. This avoids map iteration affecting candidate order. Keep skipping invalid references and entries with empty digests. The prepare task already represents candidates as a list and deduplicates identical repository/current/target triples.

## Decisions and recommendations

| Decision | Recommendation | Reason |
|---|---|---|
| One image field or a list on `Workload` | Use `[]WorkloadImage` | A pod may have multiple regular or init containers; creating one status workload per container would change workload counts and status semantics. |
| Wire-report grouping | Flatten pairs into `DeviceApplicationStatus.ImageDigests` per application | This matches the existing API and delta-worker input; it does not preserve container-to-image attribution. |
| Kubernetes image reference source | Use `spec.containers[].image` and `spec.initContainers[].image` | These are the rendered references expected by `ApplicationImageDigest`. |
| Kubernetes digest source | Parse `ImageID` only when it contains a pullable repository digest that matches the normalized spec repository | Runtime-specific IDs can be config/image IDs and may not be usable OCI manifest digests. |
| Unknown Kubernetes digest | Report the image with an empty digest | This is safe and leaves the existing full-pull behavior available. |
| Podman digest lookup | Use `event.Image` plus `Podman.ImageDigest` on first observation or when ID/reference changes | The event already supplies the image reference; a local lookup resolves the digest actually present in storage. |
| Lookup failure | Keep the reference, leave digest empty, and continue status reporting | Delta preparation should fail open to a full pull. |
| Duplicate image references | Drop duplicate exact pairs; suppress an empty digest if a known digest exists for the same reference; retain multiple distinct known digests | A rolling update can briefly have different contents under the same mutable tag. |
| Deduplication location | Keep raw image observations in workload slices; deduplicate during application-status aggregation | One shared aggregation point handles duplicates across pods, containers, and the parent image. |
| Multiple current digests in one repository | Generate candidates for all distinct source digests | A single-value repository index loses valid sources nondeterministically. |
| Image-backed volumes | Leave volume digest collection out of this workload fix | The delta worker extracts image-volume targets, but workload monitors do not observe a unified volume digest lifecycle. Volume digest collection needs a separate source/resolution design. |

## Focused verification plan

Add or extend tests in the affected packages; do not add API schema tests because the wire type is unchanged.

- `internal/agent/device/applications/applications_test.go`: parent plus several workload images, repeated pairs, same reference with known and unknown digests, multiple known digests, and clearing stale `ImageDigests`.
- `internal/agent/device/applications/applications_test.go`: `Workloads()` returns independent image slices.
- `internal/agent/device/applications/kubernetes_monitor_test.go`: a pod with regular and init containers, name-based status matching, a pullable image ID, an opaque runtime-only ID, and an update that removes an old image.
- `internal/agent/device/applications/podman_monitor_test.go`: event image is recorded, digest lookup happens for a new or changed container, known digest is reused on later status events, and lookup failure does not block status.
- `internal/delta_worker/app_expand_test.go`: two current digests from one normalized repository both produce candidates for a changed tag; matching digests, invalid refs, and empty digests remain skipped.

## Implementation status

- Added `Workload.Images []WorkloadImage` and cloned image slices at workload ownership boundaries.
- Kubernetes snapshots regular and init container image references and accepts only matching `docker-pullable://` image digests; opaque runtime IDs remain unknown.
- Podman snapshots the event image, resolves tag digests with the existing client helper, reuses known digests for the same container ID/reference, and refreshes after an ID or reference change. If an ID changes on an event with no image, the old digest is cleared until a later event can refresh it.
- Application status now deduplicates image/digest pairs deterministically, retaining multiple known digests for a mutable reference and clearing stale image status when the collected set is empty.
- The delta worker indexes all unique current digests per normalized repository and inspects each new reference once.
- Focused verification passed: `go test ./internal/agent/device/applications ./internal/delta_worker`.
- `git diff --check` passed. No API schema or generated type changes were needed.

## Stack placement and rollout

Implement this in the current application-image-digests parent branch so the render-hints and apply-deltas descendants inherit the enriched status. Keep the existing fixes for CodeRabbit comments 2–4 intact. No OpenAPI or generated API changes are expected.

`gh stack view` confirms that the render-hints and apply-deltas descendants are above this parent. After committing the implementation, synchronize the parent with its upstream branch, then rebase the descendants so they inherit this fix. Use targeted `gh stack` rebase and push steps; `gh stack sync` also pushes all active branches.
