#!/usr/bin/env bash
#
# Focused render tests for the flightctl-catalog-collector chart.
#
# "helm lint" only proves the chart renders for the value sets it is given.
# These cases assert the behaviour the chart promises in its README: which
# value combinations must fail, which must succeed, and what the rendered
# objects must contain.
#
# Run directly, or through "make test-helm-catalog-collector".
#
# Requires: helm. Nothing else; no cluster is contacted (--dry-run is not
# used, "helm template" renders locally).

set -euo pipefail

CHART_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HELM="${HELM:-helm}"
# Pin the Capabilities.KubeVersion so results do not drift with the local
# helm build's default.
KUBE_VERSION="${KUBE_VERSION:-1.32.0}"

PASS=0
FAIL=0

# render RELEASE_ARGS... -> prints rendered manifests on stdout.
render() {
    "${HELM}" template test-release "${CHART_DIR}" \
        --namespace flightctl-test \
        --kube-version "${KUBE_VERSION}" \
        "$@"
}

ok() {
    PASS=$((PASS + 1))
    printf 'ok   %s\n' "$1"
}

bad() {
    FAIL=$((FAIL + 1))
    printf 'FAIL %s\n' "$1" >&2
    if [[ $# -gt 1 ]]; then
        printf '     %s\n' "$2" >&2
    fi
}

# assert_render_fails <name> <expected-substring> <helm args...>
assert_render_fails() {
    local name="$1" expect="$2"
    shift 2
    local output
    if output="$(render "$@" 2>&1)"; then
        bad "${name}" "render succeeded but was expected to fail"
        return
    fi
    if [[ "${output}" != *"${expect}"* ]]; then
        bad "${name}" "error did not mention '${expect}': ${output}"
        return
    fi
    ok "${name}"
}

# assert_render_succeeds <name> <helm args...>
assert_render_succeeds() {
    local name="$1"
    shift
    local output
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    ok "${name}"
}

# assert_contains <name> <substring> <helm args...>
assert_contains() {
    local name="$1" expect="$2"
    shift 2
    local output
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    if [[ "${output}" != *"${expect}"* ]]; then
        bad "${name}" "rendered output did not contain '${expect}'"
        return
    fi
    ok "${name}"
}

# assert_matches <name> <extended-regex> <helm args...>
#
# Line-anchored matching, for needles that are a prefix of another kind
# ("kind: Service" versus "kind: ServiceAccount").
assert_matches() {
    local name="$1" expect="$2"
    shift 2
    local output
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    if ! grep -qE "${expect}" <<<"${output}"; then
        bad "${name}" "rendered output did not match /${expect}/"
        return
    fi
    ok "${name}"
}

# assert_not_matches <name> <extended-regex> <helm args...>
assert_not_matches() {
    local name="$1" unexpect="$2"
    shift 2
    local output
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    if grep -qE "${unexpect}" <<<"${output}"; then
        bad "${name}" "rendered output unexpectedly matched /${unexpect}/"
        return
    fi
    ok "${name}"
}

# assert_not_contains <name> <substring> <helm args...>
assert_not_contains() {
    local name="$1" unexpect="$2"
    shift 2
    local output
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    if [[ "${output}" == *"${unexpect}"* ]]; then
        bad "${name}" "rendered output unexpectedly contained '${unexpect}'"
        return
    fi
    ok "${name}"
}

# assert_label_appears_once <name> <label-key> <helm args...>
#
# Asserts that the pod template labels name <label-key> exactly once. A
# repeated YAML mapping key is ambiguous: parsers differ on whether it is an
# error or last-one-wins, so the chart must never emit one even when both
# values agree.
assert_label_appears_once() {
    local name="$1" key="$2"
    shift 2
    local output count
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    # Pod template labels are indented by eight spaces; the Deployment's own
    # metadata labels and the selector sit at four and six.
    count="$(grep -cE "^        ${key//./\\.}:" <<<"${output}" || true)"
    if [[ "${count}" != "1" ]]; then
        bad "${name}" "pod template names '${key}' ${count} time(s), expected 1"
        return
    fi
    ok "${name}"
}

# assert_secret_default_mode <name> <secret-name> <expected-mode> <helm args...>
#
# Asserts the defaultMode of the Secret volume backed by <secret-name>. Helm
# renders the octal literal as its decimal value (0440 -> 288), so the
# expected mode is given in decimal.
assert_secret_default_mode() {
    local name="$1" secret="$2" expect="$3"
    shift 3
    local output mode
    if ! output="$(render "$@" 2>&1)"; then
        bad "${name}" "render failed: ${output}"
        return
    fi
    # Track the defaultMode of the volume entry currently being read; a new
    # list item resets it, so a volume without one cannot inherit a mode from
    # the entry above it.
    mode="$(awk -v secret="${secret}" '
        /^[[:space:]]*- / { mode = "" }
        /^[[:space:]]*defaultMode:[[:space:]]/ { mode = $2 }
        $0 ~ ("^[[:space:]]*secretName:[[:space:]]+" secret "$") { print mode; exit }
    ' <<<"${output}")"
    if [[ -z "${mode}" ]]; then
        bad "${name}" "no defaultMode found for Secret '${secret}'"
        return
    fi
    if [[ "${mode}" != "${expect}" ]]; then
        bad "${name}" "Secret '${secret}' has defaultMode ${mode}, expected ${expect}"
        return
    fi
    ok "${name}"
}

MINIMAL_CONFIG='sources:
  http/s:
    listenAddress: 0.0.0.0:8080
destinations:
  debug/d: {}
pipelines:
  p:
    source: http/s
    destination: debug/d
'

# --------------------------------------------------------------------------
# config.content / config.existingName
# --------------------------------------------------------------------------

# When neither config source is set it should fail with an actionable message.
assert_render_fails \
    "bare install fails: no configuration given" \
    "one of config.content or config.existingName must be set"

# When both config sources are set it should refuse rather than pick one.
assert_render_fails \
    "config.content and config.existingName together are rejected" \
    "mutually exclusive" \
    --set-string "config.content=${MINIMAL_CONFIG}" \
    --set-string config.existingName=external-config

# When only config.existingName is set it should render without a ConfigMap.
assert_render_succeeds \
    "config.existingName alone renders" \
    --set-string config.existingName=external-config

assert_not_contains \
    "config.existingName alone creates no ConfigMap" \
    "kind: ConfigMap" \
    --set-string config.existingName=external-config

assert_contains \
    "config.existingName alone mounts the external ConfigMap" \
    "name: external-config" \
    --set-string config.existingName=external-config

# The checksum annotation only makes sense for chart-owned content; with an
# external ConfigMap the chart cannot see the content to hash.
assert_not_contains \
    "config.existingName adds no checksum/config annotation" \
    "checksum/config" \
    --set-string config.existingName=external-config

assert_contains \
    "config.content creates a chart-owned ConfigMap" \
    "kind: ConfigMap" \
    --set-string "config.content=${MINIMAL_CONFIG}"

assert_contains \
    "config.content adds a checksum/config annotation" \
    "checksum/config" \
    --set-string "config.content=${MINIMAL_CONFIG}"

assert_render_fails \
    "relative config.mountPath is rejected" \
    "config.mountPath must be an absolute path" \
    --set-string config.existingName=external-config \
    --set-string config.mountPath=etc/flightctl

assert_render_fails \
    "empty config.key is rejected" \
    "config.key must not be empty" \
    --set-string config.existingName=external-config \
    --set-string config.key=

assert_contains \
    "--config argument is mountPath/key" \
    "/etc/flightctl/catalog-collector/config.yaml" \
    --set-string config.existingName=external-config

# --------------------------------------------------------------------------
# replicaCount
# --------------------------------------------------------------------------

assert_render_fails \
    "replicaCount=2 is rejected" \
    "replicaCount must be 0 or 1" \
    --set-string config.existingName=external-config \
    --set replicaCount=2

assert_render_fails \
    "replicaCount=-1 is rejected" \
    "replicaCount must be 0 or 1" \
    --set-string config.existingName=external-config \
    --set replicaCount=-1

# --set-json, not --set: "--set replicaCount=0.5" reaches the template as the
# string "0.5" and is caught by the numeric-type check above it. A values file
# yields a real float64, which is the case that used to be truncated to an
# in-range integer by the validator while the Deployment rendered the fraction.
assert_render_fails \
    "fractional replicaCount is rejected" \
    "replicaCount must be a whole number" \
    --set-string config.existingName=external-config \
    --set-json replicaCount=0.5

assert_contains \
    "a whole-number float replicaCount still renders" \
    "replicas: 1" \
    --set-string config.existingName=external-config \
    --set-json replicaCount=1.0

assert_contains \
    "replicaCount=0 renders a paused Deployment" \
    "replicas: 0" \
    --set-string config.existingName=external-config \
    --set replicaCount=0

assert_contains \
    "replicaCount=1 renders a single replica" \
    "replicas: 1" \
    --set-string config.existingName=external-config \
    --set replicaCount=1

# --------------------------------------------------------------------------
# Service
# --------------------------------------------------------------------------

assert_not_matches \
    "Service is omitted when it would have no ports" \
    "^kind: Service$" \
    --set-string config.existingName=external-config \
    --set metrics.enabled=false \
    --set health.enabled=false

assert_matches \
    "Service is rendered when only extraPorts remain" \
    "^kind: Service$" \
    --set-string config.existingName=external-config \
    --set metrics.enabled=false \
    --set health.enabled=false \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080"

assert_not_matches \
    "Service is omitted when service.enabled=false" \
    "^kind: Service$" \
    --set-string config.existingName=external-config \
    --set service.enabled=false

assert_matches \
    "ServiceAccount is still created when the Service is omitted" \
    "^kind: ServiceAccount$" \
    --set-string config.existingName=external-config \
    --set service.enabled=false

assert_render_fails \
    "ServiceMonitor without a Service is rejected" \
    "requires a Service" \
    --set-string config.existingName=external-config \
    --set metrics.serviceMonitor.enabled=true \
    --set service.enabled=false

assert_contains \
    "ServiceMonitor is rendered with a Service" \
    "kind: ServiceMonitor" \
    --set-string config.existingName=external-config \
    --set metrics.serviceMonitor.enabled=true

# --------------------------------------------------------------------------
# Metrics path
# --------------------------------------------------------------------------

# The collector serves its registry at a fixed /metrics; service.metrics.
# endpoint in its configuration is host:port and carries no path. The chart
# therefore hardcodes the scrape path instead of exposing a value that could
# only have pointed Prometheus at a 404.

assert_contains \
    "ServiceMonitor scrapes the fixed /metrics path" \
    "path: /metrics" \
    --set-string config.existingName=external-config \
    --set metrics.serviceMonitor.enabled=true

assert_contains \
    "the Prometheus pod annotation uses the fixed /metrics path" \
    'prometheus.io/path: "/metrics"' \
    --set-string config.existingName=external-config

# metrics.path is not a chart value any more. Helm accepts an unknown --set
# key silently, so assert on the effect: setting it must not move either
# scrape target off /metrics.
assert_not_contains \
    "a stray metrics.path does not move the ServiceMonitor scrape path" \
    "path: /moved" \
    --set-string config.existingName=external-config \
    --set metrics.serviceMonitor.enabled=true \
    --set-string metrics.path=/moved

assert_not_contains \
    "a stray metrics.path does not move the Prometheus pod annotation" \
    'prometheus.io/path: "/moved"' \
    --set-string config.existingName=external-config \
    --set-string metrics.path=/moved

# The shipped value files must not carry the removed key either. Helm ignores
# an unknown values key without a word, so a leftover "metrics.path" reads as a
# supported knob that silently does nothing. Only the top-level metrics block
# is inspected: "path" is a legitimate key inside config.content, which is the
# collector's own configuration and not chart values.
for values in "${CHART_DIR}"/examples/*.yaml "${CHART_DIR}/lint-values.yaml"; do
    name="$(basename "${values}") does not set the removed metrics.path"
    hit="$(awk '
        /^[^[:space:]#]/ { in_metrics = ($0 ~ /^metrics:/) }
        in_metrics && /^  path:[[:space:]]/ { print FILENAME ":" FNR ": " $0 }
    ' "${values}")"
    if [[ -n "${hit}" ]]; then
        bad "${name}" "${hit}"
    else
        ok "${name}"
    fi
done

# --------------------------------------------------------------------------
# podLabels versus the Deployment selector
# --------------------------------------------------------------------------

# The selector is immutable after the first install and is built from the two
# selector labels. A podLabels entry that changes either of them produces a
# pod template the selector cannot match, which the API server rejects long
# after "helm template" said the chart was fine. Catch it at render time.

assert_render_fails \
    "podLabels overriding the selector name label is rejected" \
    "conflicts with the immutable Deployment selector label" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.app\.kubernetes\.io/name=hijacked"

assert_render_fails \
    "podLabels overriding the selector instance label is rejected" \
    "conflicts with the immutable Deployment selector label" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.app\.kubernetes\.io/instance=hijacked"

# Only a conflicting value is rejected. Everything else podLabels is for still
# works, including repeating a selector key with the value already in use.

assert_matches \
    "podLabels keeps unrelated custom labels on the pod template" \
    "^        team: edge$" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.team=edge"

assert_render_succeeds \
    "podLabels repeating a selector label with the same value is accepted" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.app\.kubernetes\.io/instance=test-release"

assert_matches \
    "a non-selector chart label may still be overridden by podLabels" \
    "^        app\.kubernetes\.io/component: custom$" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.app\.kubernetes\.io/component=custom"

# Whatever podLabels contains, the pod template must keep the labels the
# selector matches on, and must name each label exactly once: a repeated YAML
# mapping key is ambiguous before the API server ever sees it.

assert_matches \
    "pod template keeps the selector name label" \
    "^        app\.kubernetes\.io/name: flightctl-catalog-collector$" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.app\.kubernetes\.io/instance=test-release" \
    --set-string "podLabels.team=edge"

assert_label_appears_once \
    "a podLabels entry repeating a chart label emits the key once" \
    "app.kubernetes.io/instance" \
    --set-string config.existingName=external-config \
    --set-string "podLabels.app\.kubernetes\.io/instance=test-release"

# --------------------------------------------------------------------------
# podAnnotations versus the chart-owned config checksum
# --------------------------------------------------------------------------

# checksum/config is what makes "helm upgrade --set config.content=..." roll
# the pods. Letting podAnnotations pin it would emit the key twice and leave
# the collector running the old configuration.

assert_render_fails \
    "podAnnotations overriding checksum/config is rejected" \
    "podAnnotations must not set" \
    --set-string "config.content=${MINIMAL_CONFIG}" \
    --set-string "podAnnotations.checksum/config=pinned"

# With an external ConfigMap the chart emits no checksum, so the key is the
# operator's to use.
assert_render_succeeds \
    "podAnnotations may set checksum/config when the ConfigMap is external" \
    --set-string config.existingName=external-config \
    --set-string "podAnnotations.checksum/config=pinned"

assert_matches \
    "podAnnotations keeps unrelated annotations on the pod template" \
    "^        example\.com/owner: edge$" \
    --set-string config.existingName=external-config \
    --set-string "podAnnotations.example\.com/owner=edge"

# --------------------------------------------------------------------------
# Port name collisions
# --------------------------------------------------------------------------

# The chart adds the metrics and health ports itself. Reusing either name
# renders a container with two ports sharing a name, which the API server
# rejects with an error that points at a list index.

assert_render_fails \
    "an extraPort reusing the metrics port name is rejected" \
    'port name "metrics" is used by both' \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=metrics" \
    --set "service.extraPorts[0].port=9999"

assert_render_succeeds \
    "a distinct extraPort is accepted" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080"

# Port numbers are not the chart's business. Kubernetes allows several Service
# ports to target the same container port, and a TCP and a UDP entry may share
# a number, so neither shape may be rejected at render time.

assert_render_succeeds \
    "distinct Service ports may target the same container port" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080" \
    --set "service.extraPorts[0].targetPort=9000" \
    --set "service.extraPorts[1].name=snapshots-alt" \
    --set "service.extraPorts[1].port=8081" \
    --set "service.extraPorts[1].targetPort=9000"

assert_render_succeeds \
    "a TCP and a UDP port may share a number under distinct names" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=syslog-tcp" \
    --set "service.extraPorts[0].port=9000" \
    --set "service.extraPorts[0].protocol=TCP" \
    --set "service.extraPorts[1].name=syslog-udp" \
    --set "service.extraPorts[1].port=9000" \
    --set "service.extraPorts[1].protocol=UDP"

# --------------------------------------------------------------------------
# Extra port shape: name and targetPort
# --------------------------------------------------------------------------

# Kubernetes lets a Service port's targetPort name a container port, but the
# chart renders the same targetPort as the collector's containerPort, which
# must be an integer. A named targetPort would therefore render
# "containerPort: snap" and the API server would reject the Deployment.

assert_render_fails \
    "a named extraPort targetPort is rejected" \
    'sets targetPort "snap", which is not a port number' \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080" \
    --set-string "service.extraPorts[0].targetPort=snap"

# A numeric targetPort is the supported shape, quoted or not: the templates
# render it unquoted either way, so the manifest carries an integer.

assert_matches \
    "a numeric extraPort targetPort becomes the containerPort" \
    "^              containerPort: 9000$" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080" \
    --set "service.extraPorts[0].targetPort=9000"

assert_render_succeeds \
    "a quoted numeric extraPort targetPort is accepted" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080" \
    --set-string "service.extraPorts[0].targetPort=9000"

# Every extra port needs a name: without one the chart renders "name: null" on
# both the containerPort and the Service port, and the API server requires a
# name on every port of a multi-port Service.

assert_render_fails \
    "an extraPort with no name is rejected" \
    "service.extraPorts[0] has no name" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].port=8080"

assert_render_fails \
    "an extraPort with an empty name is rejected" \
    "service.extraPorts[0] has no name" \
    --set-string config.existingName=external-config \
    --set-string "service.extraPorts[0].name=" \
    --set "service.extraPorts[0].port=8080"

# The index in the message points at the offending entry, not at the first one.
assert_render_fails \
    "a nameless extraPort is reported by its own index" \
    "service.extraPorts[1] has no name" \
    --set-string config.existingName=external-config \
    --set "service.extraPorts[0].name=snapshots" \
    --set "service.extraPorts[0].port=8080" \
    --set "service.extraPorts[1].port=8081"

# --------------------------------------------------------------------------
# Security context and probes
# --------------------------------------------------------------------------

assert_contains \
    "pod runs as non-root" \
    "runAsNonRoot: true" \
    --set-string config.existingName=external-config

assert_contains \
    "root filesystem is read-only" \
    "readOnlyRootFilesystem: true" \
    --set-string config.existingName=external-config

assert_contains \
    "probes target the health port" \
    "livenessProbe:" \
    --set-string config.existingName=external-config

assert_not_contains \
    "probes are omitted when health.enabled=false" \
    "livenessProbe:" \
    --set-string config.existingName=external-config \
    --set health.enabled=false

# --------------------------------------------------------------------------
# Rendered output hygiene
# --------------------------------------------------------------------------

# Template commentary belongs in {{/* */}}. A YAML comment in a template is
# copied verbatim into the rendered manifest and into "helm get manifest".

assert_not_contains \
    "checksum rationale is not rendered into the manifest" \
    "Roll the pods when the rendered configuration changes" \
    --set-string "config.content=${MINIMAL_CONFIG}"

assert_not_contains \
    "Recreate rationale is not rendered into the manifest" \
    "two instances reconciling the" \
    --set-string config.existingName=external-config

assert_not_contains \
    "Service rationale is not rendered into the manifest" \
    "Nothing to publish" \
    --set-string config.existingName=external-config \
    --set metrics.enabled=false \
    --set health.enabled=false

# The chart's own templates must not leak YAML comments at all. Only the
# user-supplied config.content may, because the chart copies it verbatim.
assert_not_matches \
    "chart templates render no YAML comments outside the Source headers" \
    "^[[:space:]]+#" \
    --set-string config.existingName=external-config

# --------------------------------------------------------------------------
# Credential volume permissions
# --------------------------------------------------------------------------

# A regular Secret volume stays owned by root:root whatever uid the pod runs
# as, so the owner bits are useless to the container (uid 1001) and 0400
# renders a credential it cannot open. The read has to come from the group
# bit, because the image's group is gid 0. 0440 is therefore load-bearing,
# not cosmetic: assert it for the credential volume the example ships.

RHOAI_EXAMPLE="${CHART_DIR}/examples/values-rhoai-to-flightctl.yaml"
MODE_0440=288

assert_secret_default_mode \
    "OAuth2 Secret volume is group-readable (0440)" \
    flightctl-catalog-collector-oauth "${MODE_0440}" \
    --values "${RHOAI_EXAMPLE}"

# --------------------------------------------------------------------------
# ServiceAccount token mounting
# --------------------------------------------------------------------------

# The chart default stays closed: a collector talking only to endpoints that
# authenticate with mounted files has no use for the pod's own token, and
# mounting it anyway hands every such deployment an API-server credential it
# never asked for.

assert_matches \
    "chart default does not automount the ServiceAccount token on the pod" \
    "^      automountServiceAccountToken: false$" \
    --set-string config.existingName=external-config

assert_matches \
    "chart default does not automount the ServiceAccount token on the ServiceAccount" \
    "^automountServiceAccountToken: false$" \
    --set-string config.existingName=external-config

assert_not_matches \
    "chart default never renders an automount opt-in" \
    "automountServiceAccountToken: true" \
    --set-string config.existingName=external-config

# The RHOAI example opts in, because the model registry's kube-rbac-proxy
# validates the bearer token against the API server's own audience. A
# projected token with a custom audience is rejected there, so the pod's
# automounted token is the credential that works.

assert_matches \
    "RHOAI example automounts the ServiceAccount token on the pod" \
    "^      automountServiceAccountToken: true$" \
    --values "${RHOAI_EXAMPLE}"

assert_matches \
    "RHOAI example automounts the ServiceAccount token on the ServiceAccount" \
    "^automountServiceAccountToken: true$" \
    --values "${RHOAI_EXAMPLE}"

# Having opted in, the example must not also carry the hand-issued token it
# replaces: leaving the Secret behind would keep a long-lived credential in
# the namespace that nothing reads and nobody rotates.

assert_not_contains \
    "RHOAI example mounts no static model registry token Secret" \
    "flightctl-catalog-collector-tokens" \
    --values "${RHOAI_EXAMPLE}"

assert_not_contains \
    "RHOAI example has no tokens mount path" \
    "/etc/flightctl/catalog-collector/tokens" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example reads the model registry token from the automounted path" \
    "tokenFile: /var/run/secrets/kubernetes.io/serviceaccount/token" \
    --values "${RHOAI_EXAMPLE}"

# Only the model registry credential moved. Flight Control still authenticates
# as an OAuth2 client, and both legs still verify TLS against a mounted CA.

assert_contains \
    "RHOAI example keeps the Flight Control OAuth2 client Secret" \
    "secretName: flightctl-catalog-collector-oauth" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example keeps the OAuth2 client id file" \
    "clientIdFile: /etc/flightctl/catalog-collector/oauth/client-id" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example keeps the OAuth2 client secret file" \
    "clientSecretFile: /etc/flightctl/catalog-collector/oauth/client-secret" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example keeps the CA bundle ConfigMap mounted" \
    "name: flightctl-catalog-collector-ca" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example still verifies the model registry certificate" \
    "certificateAuthority: /etc/flightctl/catalog-collector/certs/model-registry-ca.crt" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example still verifies the Flight Control certificate" \
    "certificateAuthority: /etc/flightctl/catalog-collector/certs/flightctl-ca.crt" \
    --values "${RHOAI_EXAMPLE}"

assert_contains \
    "RHOAI example still verifies the OIDC issuer certificate" \
    "certificateAuthority: /etc/flightctl/catalog-collector/certs/oidc-ca.crt" \
    --values "${RHOAI_EXAMPLE}"

# --------------------------------------------------------------------------
# Shipped example value files
# --------------------------------------------------------------------------

for example in "${CHART_DIR}"/examples/*.yaml; do
    assert_render_succeeds \
        "example $(basename "${example}") renders" \
        --values "${example}"
done

assert_render_succeeds \
    "lint-values.yaml renders" \
    --values "${CHART_DIR}/lint-values.yaml"

# --------------------------------------------------------------------------

printf '\n%d passed, %d failed\n' "${PASS}" "${FAIL}"
[[ ${FAIL} -eq 0 ]]
