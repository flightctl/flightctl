{{/*
Chart name, overridable with nameOverride.
*/}}
{{- define "flightctl-catalog-collector.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{/*
Fully qualified release name.

The chart name is already prefixed with "flightctl-", so a release named
"flightctl-catalog-collector" is not doubled up.
*/}}
{{- define "flightctl-catalog-collector.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := include "flightctl-catalog-collector.name" . -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Selector labels: the stable subset, safe to put in a Deployment selector.
*/}}
{{- define "flightctl-catalog-collector.selectorLabels" -}}
app.kubernetes.io/name: {{ include "flightctl-catalog-collector.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Standard labels.
*/}}
{{- define "flightctl-catalog-collector.labels" -}}
{{ include "flightctl-catalog-collector.selectorLabels" . }}
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/component: catalog-collector
app.kubernetes.io/part-of: flightctl
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/*
Service account name.
*/}}
{{- define "flightctl-catalog-collector.serviceAccountName" -}}
{{- if .Values.serviceAccount.create -}}
{{- default (include "flightctl-catalog-collector.fullname" .) .Values.serviceAccount.name -}}
{{- else -}}
{{- default "default" .Values.serviceAccount.name -}}
{{- end -}}
{{- end -}}

{{/*
Validate the whole value set and return nothing.

Every template includes this, so an invalid value set fails the render
regardless of which manifest Helm happens to process first.
*/}}
{{- define "flightctl-catalog-collector.validate" -}}
{{- include "flightctl-catalog-collector.validateConfig" . -}}
{{- include "flightctl-catalog-collector.validateReplicas" . -}}
{{- include "flightctl-catalog-collector.validatePodLabels" . -}}
{{- include "flightctl-catalog-collector.validatePodAnnotations" . -}}
{{- include "flightctl-catalog-collector.validatePorts" . -}}
{{- end -}}

{{/*
Validate the config block and return nothing.

config.content and config.existingName are mutually exclusive, and exactly one
of them must be set. There is no default content: with neither set, the
container would start without a configuration file and crash-loop on
"--config is required", so the render fails first.
*/}}
{{- define "flightctl-catalog-collector.validateConfig" -}}
{{- $content := trim (default "" .Values.config.content) -}}
{{- $existing := trim (default "" .Values.config.existingName) -}}
{{- if and $content $existing -}}
{{- fail "flightctl-catalog-collector: config.content and config.existingName are mutually exclusive; set exactly one of them" -}}
{{- end -}}
{{- if and (not $content) (not $existing) -}}
{{- fail "flightctl-catalog-collector: one of config.content or config.existingName must be set. The collector has no usable default pipeline; start from examples/values-vanilla.yaml, or point config.existingName at a ConfigMap managed outside the chart" -}}
{{- end -}}
{{- if not (trim (default "" .Values.config.key)) -}}
{{- fail "flightctl-catalog-collector: config.key must not be empty" -}}
{{- end -}}
{{- if not (hasPrefix "/" (default "" .Values.config.mountPath)) -}}
{{- fail "flightctl-catalog-collector: config.mountPath must be an absolute path" -}}
{{- end -}}
{{- end -}}

{{/*
Validate replicaCount and return nothing.

The collector owns the catalogs it writes. Two instances would poll the same
upstream registry and reconcile the same catalog concurrently, racing each
other into conflicting updates, so anything above 1 is rejected rather than
silently clamped. 0 is allowed: it is the supported way to pause the collector
without uninstalling the release.

Fractional values are rejected too. The Deployment renders replicaCount
verbatim, so a value this validator truncated to an in-range integer would
still reach the API server as a fraction and be rejected there, with an error
that points at the manifest rather than at the value that produced it.
*/}}
{{- define "flightctl-catalog-collector.validateReplicas" -}}
{{- $replicas := .Values.replicaCount -}}
{{- if not (or (kindIs "int" $replicas) (kindIs "int64" $replicas) (kindIs "float64" $replicas)) -}}
{{- fail (printf "flightctl-catalog-collector: replicaCount must be a number, got %v" $replicas) -}}
{{- end -}}
{{- if and (kindIs "float64" $replicas) (ne $replicas (float64 (int $replicas))) -}}
{{- fail (printf "flightctl-catalog-collector: replicaCount must be a whole number, got %v" $replicas) -}}
{{- end -}}
{{- $n := int $replicas -}}
{{- if lt $n 0 -}}
{{- fail (printf "flightctl-catalog-collector: replicaCount must be 0 or 1, got %d" $n) -}}
{{- end -}}
{{- if gt $n 1 -}}
{{- fail (printf "flightctl-catalog-collector: replicaCount must be 0 or 1, got %d. The collector reconciles catalogs it owns, so concurrent replicas would write the same catalog and conflict. Use 0 to pause it" $n) -}}
{{- end -}}
{{- end -}}

{{/*
Validate podLabels and return nothing.

The Deployment selector is built from selectorLabels and is immutable after
the first install, so a podLabels entry that reuses one of those keys with a
different value renders a pod template the selector can no longer match. The
API server rejects that Deployment with "`selector` does not match template
`labels`", which says nothing about the value that caused it.

Only a conflicting value is rejected. Unrelated keys are the entire point of
podLabels, and repeating a selector key with the value the chart already uses
changes nothing.
*/}}
{{- define "flightctl-catalog-collector.validatePodLabels" -}}
{{- $selector := fromYaml (include "flightctl-catalog-collector.selectorLabels" .) -}}
{{- range $key, $value := (default (dict) .Values.podLabels) -}}
{{- if hasKey $selector $key -}}
{{- $reserved := index $selector $key -}}
{{- if ne (toString $value) (toString $reserved) -}}
{{- fail (printf "flightctl-catalog-collector: podLabels sets %s=%s, which conflicts with the immutable Deployment selector label %s=%s. The pod template would no longer match the selector and the API server would reject the Deployment. Drop the override, or set it to %q" $key (toString $value) $key $reserved $reserved) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Pod template labels: the chart labels with podLabels merged on top.

Merged rather than appended so a podLabels entry that repeats a chart label
does not emit the same YAML mapping key twice. podLabels still wins, which is
what appending already did via last-one-wins; the two selector labels are the
exception, and validatePodLabels has already rejected any value for them other
than the one the chart uses.
*/}}
{{- define "flightctl-catalog-collector.podTemplateLabels" -}}
{{- $chart := fromYaml (include "flightctl-catalog-collector.labels" .) -}}
{{- toYaml (merge (dict) (default (dict) .Values.podLabels) $chart) -}}
{{- end -}}

{{/*
Validate podAnnotations and return nothing.

checksum/config is owned by the chart: it carries the hash of the rendered
ConfigMap so a "helm upgrade" that only changes config.content still rolls the
pods. A podAnnotations entry with the same key emits the mapping key twice and
the later one wins, which pins the annotation to a constant and silently turns
config-change rollouts off.

Only checked when the chart actually emits the annotation. With
config.existingName set the ConfigMap is managed elsewhere, the chart emits no
checksum, and the key is the operator's to use.
*/}}
{{- define "flightctl-catalog-collector.validatePodAnnotations" -}}
{{- if not (trim (default "" .Values.config.existingName)) -}}
{{- if hasKey (default (dict) .Values.podAnnotations) "checksum/config" -}}
{{- fail "flightctl-catalog-collector: podAnnotations must not set \"checksum/config\". The chart owns that annotation and uses it to roll the pods when config.content changes; overriding it would pin the value and stop those rollouts" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Validate the collector's port set and return nothing.

metrics, health, and every service.extraPorts entry become a containerPort on
the collector (and a port on the Service). Kubernetes requires those names to
be unique, within the container and within the Service. The chart adds the
metrics and health ports itself, so a values file that repeats either name
renders a Deployment the API server refuses, pointing at a list index rather
than at the value responsible.

Each extra port must also carry a non-empty name, and a targetPort that is a
port number rather than a port name. Both are rejected here because the
failure they cause otherwise surfaces at apply time, against a manifest the
operator never wrote:

  - A nameless entry renders "name: null" on both the containerPort and the
    Service port. The API server requires a name on every port of a
    multi-port Service, so the Service is refused.

  - Kubernetes lets a Service port's targetPort name a container port, but
    the chart has no separate container-port value: the Deployment renders
    the same targetPort as the collector's containerPort, which must be an
    integer. "targetPort: snap" therefore renders "containerPort: snap" and
    the Deployment is refused.

Port numbers themselves are not checked, and may legitimately repeat: several
Service ports can target the same container port, and TCP and UDP entries can
share a number.
*/}}
{{- define "flightctl-catalog-collector.validatePorts" -}}
{{- $entries := list -}}
{{- if .Values.metrics.enabled -}}
{{- $entries = append $entries (dict "src" "metrics.port" "name" "metrics") -}}
{{- end -}}
{{- if .Values.health.enabled -}}
{{- $entries = append $entries (dict "src" "health.port" "name" "health") -}}
{{- end -}}
{{- range $i, $p := (default (list) .Values.service.extraPorts) -}}
{{- $src := printf "service.extraPorts[%d]" $i -}}
{{- $name := trim (toString (default "" $p.name)) -}}
{{- if not $name -}}
{{- fail (printf "flightctl-catalog-collector: %s has no name. Every entry in service.extraPorts must set a non-empty name: it becomes the name of both a containerPort on the collector and a port on the Service, and the API server requires a name on every port of a multi-port Service" $src) -}}
{{- end -}}
{{/*
Treat an absent, null, or empty targetPort as unset, exactly as the
Deployment and the Service do with "default .port .targetPort".
*/}}
{{- $target := trim (toString (default "" $p.targetPort)) -}}
{{- if and $target (not (regexMatch "^[0-9]+$" $target)) -}}
{{- fail (printf "flightctl-catalog-collector: %s sets targetPort %q, which is not a port number. The chart renders targetPort as the collector's containerPort as well, and a containerPort must be an integer, so a named targetPort would produce a Deployment the API server rejects. Give targetPort the number the collector listens on; the name comes from %s.name" $src $target $src) -}}
{{- end -}}
{{- $entries = append $entries (dict "src" $src "name" $name) -}}
{{- end -}}
{{- $byName := dict -}}
{{- range $e := $entries -}}
{{- if hasKey $byName $e.name -}}
{{- fail (printf "flightctl-catalog-collector: port name %q is used by both %s and %s. Port names must be unique within the collector container and within the Service; the chart adds the metrics and health ports itself, so do not repeat them in service.extraPorts" $e.name (index $byName $e.name) $e.src) -}}
{{- end -}}
{{- $_ := set $byName $e.name $e.src -}}
{{- end -}}
{{- end -}}

{{/*
Emit "true" when the Service would carry at least one port.

A Service with an empty ports list is rejected by the API server, so it is
skipped entirely in that case even when service.enabled is true.
*/}}
{{- define "flightctl-catalog-collector.serviceHasPorts" -}}
{{- if or .Values.metrics.enabled .Values.health.enabled (gt (len (default (list) .Values.service.extraPorts)) 0) -}}
true
{{- end -}}
{{- end -}}

{{/*
Emit "true" when a Service should be rendered at all.
*/}}
{{- define "flightctl-catalog-collector.serviceEnabled" -}}
{{- if and .Values.service.enabled (include "flightctl-catalog-collector.serviceHasPorts" .) -}}
true
{{- end -}}
{{- end -}}

{{/*
Name of the ConfigMap holding the collector configuration: either the one this
chart creates, or the pre-existing one the operator pointed us at.
*/}}
{{- define "flightctl-catalog-collector.configMapName" -}}
{{- include "flightctl-catalog-collector.validateConfig" . -}}
{{- if trim (default "" .Values.config.existingName) -}}
{{- .Values.config.existingName -}}
{{- else -}}
{{- printf "%s-config" (include "flightctl-catalog-collector.fullname" .) -}}
{{- end -}}
{{- end -}}

{{/*
Absolute path of the configuration file inside the container.
*/}}
{{- define "flightctl-catalog-collector.configFilePath" -}}
{{- printf "%s/%s" (trimSuffix "/" .Values.config.mountPath) .Values.config.key -}}
{{- end -}}

{{/*
Container image reference.
*/}}
{{- define "flightctl-catalog-collector.image" -}}
{{- printf "%s:%s" .Values.image.image (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/*
HTTP path the collector serves its Prometheus registry on.

Fixed, and deliberately not a chart value. The collector's metrics server
registers the handler at /metrics and nothing in its configuration moves it:
service.metrics.endpoint is a host:port pair. A chart value would therefore
have changed only the scrape target in the Service annotations and in the
ServiceMonitor, pointing Prometheus at a path the collector answers with 404
— a silent loss of metrics rather than a visible error.
*/}}
{{- define "flightctl-catalog-collector.metricsPath" -}}
/metrics
{{- end -}}
