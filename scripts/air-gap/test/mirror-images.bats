#!/usr/bin/env bats
# =============================================================================
# mirror-images.bats — Unit and integration tests for mirror-images.sh
#
# Stories covered:
#   EDM-3958  Parse helm-chart-opts.yaml and generate skopeo commands
#
# Related:
#   EDM-3957  CLI scaffold (parse_args / validate_args)
#   EDM-3959  Parse observability image files
#   EDM-3960  Generate artifact manifest YAML
#
# Test categories (tagged with @tag in description):
#   unit        — isolated function tests using fixture files, no live YAML
#   integration — tests against the real deploy/helm/helm-chart-opts.yaml
#
# Prerequisites:
#   yq v4+  (mikefarah) — must be on PATH before running
#   bats    ≥ 1.5.0
#
# Run all tests:
#   bats scripts/air-gap/test/mirror-images.bats
#
# Run only unit tests (requires bats-core ≥ 1.5 with --filter):
#   bats --filter unit scripts/air-gap/test/mirror-images.bats
#
# Run only integration tests:
#   bats --filter integration scripts/air-gap/test/mirror-images.bats
# =============================================================================

# ---------------------------------------------------------------------------
# Setup / teardown
# ---------------------------------------------------------------------------

# Absolute paths resolved once so tests are location-independent.
SCRIPT_DIR="$(cd "$(dirname "${BATS_TEST_FILENAME}")/.." && pwd)"
SCRIPT="${SCRIPT_DIR}/mirror-images.sh"
FIXTURES="${SCRIPT_DIR}/test/fixtures"

# Each test gets its own isolated temp dir so manifest files don't collide.
setup() {
    TEST_TMP="$(mktemp -d)"
    # Empty regular file for use as a stub RPM spec (parse_rpm_requires returns
    # empty; check_dependencies' -f test passes since it is a real file).
    EMPTY_SPEC="${TEST_TMP}/empty.spec"
    touch "${EMPTY_SPEC}"
    # Change into the temp dir so manifest output goes there, not the repo root.
    cd "${TEST_TMP}"
}

teardown() {
    rm -rf "${TEST_TMP}"
}

# Empty spec file created once per test run (regular file, not /dev/null, so
# check_dependencies' -f test passes; parse_rpm_requires returns an empty list).
EMPTY_SPEC=""

# ---------------------------------------------------------------------------
# Helper: source the script with fixture paths injected via environment
# variables that the script honours when set. This lets unit tests exercise
# individual functions without touching any real repo files.
# ---------------------------------------------------------------------------

# Source the script and override its path constants with fixture equivalents.
# Called at the start of every unit test that exercises internal functions.
source_with_fixtures() {
    # The script skips main() when sourced (BASH_SOURCE guard), so only
    # function definitions are loaded.
    # shellcheck source=../mirror-images.sh
    source "${SCRIPT}"

    # Override the constants the script sets at the top of the file.
    HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml"
    CHART_YAML="${FIXTURES}/Chart.yaml"
    OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml"
    OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml"
    # Point RPM_SPEC at an empty regular file so parse_rpm_requires returns
    # an empty list without error.  /dev/null is a char device, not a file,
    # so we use a temp file created by setup().
    RPM_SPEC="${EMPTY_SPEC}"
}

# ---------------------------------------------------------------------------
# [unit] CLI / argument parsing
# ---------------------------------------------------------------------------

@test "[unit] --help prints usage and exits 0" {
    run "${SCRIPT}" --help
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"Usage:"* ]]
    [[ "${output}" == *"--variant"* ]]
    [[ "${output}" == *"--dest-registry"* ]]
}

@test "[unit] missing --variant exits non-zero with error message" {
    run "${SCRIPT}" --dest-registry localhost:5000
    [ "${status}" -ne 0 ]
    [[ "${output}" == *"--variant"* ]]
}

@test "[unit] missing --dest-registry exits non-zero with error message" {
    run "${SCRIPT}" --variant community-el9
    [ "${status}" -ne 0 ]
    [[ "${output}" == *"--dest-registry"* ]]
}

@test "[unit] invalid variant name exits 1 with clear error" {
    run "${SCRIPT}" --variant not-a-variant --dest-registry localhost:5000
    [ "${status}" -eq 1 ]
    [[ "${output}" == *"Invalid variant"* ]]
}

@test "[unit] all four valid variant names are accepted (no error on validate_args)" {
    # Source only so we can call validate_args directly without touching files.
    source_with_fixtures

    # validate_args reads $VARIANT and $DEST_REGISTRY.
    for v in community-el9 community-el10 redhat-el9 redhat-el10; do
        VARIANT="${v}"
        DEST_REGISTRY="localhost:5000"
        # validate_args should not exit non-zero for any accepted variant.
        run validate_args
        [ "${status}" -eq 0 ] || fail "variant '${v}' was rejected by validate_args"
    done
}

# ---------------------------------------------------------------------------
# [unit] get_app_version — reads appVersion from Chart.yaml
# ---------------------------------------------------------------------------

@test "[unit] get_app_version returns value from Chart.yaml fixture" {
    source_with_fixtures
    run get_app_version
    [ "${status}" -eq 0 ]
    [ "${output}" = "v0.99.0-test" ]
}

@test "[unit] get_app_version fails when Chart.yaml is missing" {
    source_with_fixtures
    CHART_YAML="/nonexistent/Chart.yaml"
    run get_app_version
    [ "${status}" -ne 0 ]
}

# ---------------------------------------------------------------------------
# [unit] parse_helm_chart_opts — image extraction and tag fallback
# ---------------------------------------------------------------------------

@test "[unit] parse_helm_chart_opts emits one line per image for community-el9" {
    source_with_fixtures
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"
    APP_VERSION="$(get_app_version)"

    # parse_helm_chart_opts prints 'source_image dest_image' pairs to stdout;
    # [INFO] progress lines go to stderr.  Filter log lines before counting.
    run parse_helm_chart_opts "${APP_VERSION}"
    [ "${status}" -eq 0 ]

    # Fixture has 4 images under community-el9.
    image_lines="$(echo "${output}" | grep -v "^\[" | grep -v "^$")"
    [ "$(echo "${image_lines}" | wc -l)" -eq 4 ]
}

@test "[unit] parse_helm_chart_opts uses explicit tag when present" {
    source_with_fixtures
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"
    APP_VERSION="$(get_app_version)"

    # Pass app_version as positional $1 — the function uses it as the tag fallback.
    run parse_helm_chart_opts "${APP_VERSION}"
    # The db image in the fixture has tag "20250214" — verify it appears in output.
    [[ "${output}" == *"quay.io/sclorg/postgresql-16-c9s:20250214"* ]]
}

@test "[unit] parse_helm_chart_opts falls back to appVersion for untagged images" {
    source_with_fixtures
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"

    # Pass the desired fallback tag directly as the positional argument.
    run parse_helm_chart_opts "v0.99.0-test"
    # 'api' and 'worker' images in the fixture have no 'tag:' field.
    [[ "${output}" == *"quay.io/flightctl/flightctl-api-el9:v0.99.0-test"* ]]
    [[ "${output}" == *"quay.io/flightctl/flightctl-worker-el9:v0.99.0-test"* ]]
}

@test "[unit] parse_helm_chart_opts correctly maps images for redhat-el9 variant" {
    source_with_fixtures
    VARIANT="redhat-el9"
    DEST_REGISTRY="localhost:5000"
    APP_VERSION="$(get_app_version)"

    run parse_helm_chart_opts "${APP_VERSION}"
    # Fixture has registry.redhat.io sources for the redhat-el9 variant.
    [[ "${output}" == *"registry.redhat.io/rhel9/postgresql-16:9.7-1766414426"* ]]
    [[ "${output}" == *"registry.redhat.io/rhem/flightctl-api-rhel9"* ]]
}

@test "[unit] parse_helm_chart_opts fails gracefully when helm-chart-opts.yaml is missing" {
    source_with_fixtures
    HELM_CHART_OPTS="/nonexistent/helm-chart-opts.yaml"
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"

    run parse_helm_chart_opts "v0.0.1"
    [ "${status}" -ne 0 ]
    [[ "${output}" == *"not found"* ]] || [[ "${output}" == *"does not exist"* ]] || \
        [[ "${output}" == *"helm-chart-opts"* ]]
}

# ---------------------------------------------------------------------------
# [unit] image_to_dest — destination path construction
# ---------------------------------------------------------------------------

@test "[unit] image_to_dest strips source registry host and prepends dest-registry" {
    source_with_fixtures
    DEST_REGISTRY="myregistry.example.com:5000"

    # image_to_dest takes (image_without_tag, tag) as two separate args.
    run image_to_dest "quay.io/flightctl/flightctl-api-el9" "v1.2.3"
    [ "${status}" -eq 0 ]
    [ "${output}" = "myregistry.example.com:5000/flightctl/flightctl-api-el9:v1.2.3" ]
}

@test "[unit] image_to_dest handles docker.io single-component paths" {
    source_with_fixtures
    DEST_REGISTRY="localhost:5000"

    # docker.io/redis has only one path component after the registry host.
    run image_to_dest "docker.io/redis" "7.4.1"
    [ "${status}" -eq 0 ]
    [ "${output}" = "localhost:5000/redis:7.4.1" ]
}

@test "[unit] image_to_dest preserves multi-component paths" {
    source_with_fixtures
    DEST_REGISTRY="localhost:5000"

    run image_to_dest "registry.redhat.io/rhem/flightctl-api-rhel9" "latest"
    [ "${status}" -eq 0 ]
    [ "${output}" = "localhost:5000/rhem/flightctl-api-rhel9:latest" ]
}

# ---------------------------------------------------------------------------
# [unit] parse_observability_images — el9 / el10 path selection
# ---------------------------------------------------------------------------

@test "[unit] parse_observability_images selects el9 file for community-el9" {
    source_with_fixtures
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"

    run parse_observability_images
    [ "${status}" -eq 0 ]
    # grafana is in el9 fixture but not el10 fixture.
    [[ "${output}" == *"docker.io/grafana/grafana"* ]]
}

@test "[unit] parse_observability_images selects el10 file for community-el10" {
    source_with_fixtures
    VARIANT="community-el10"
    DEST_REGISTRY="localhost:5000"

    run parse_observability_images
    [ "${status}" -eq 0 ]
    # el10 fixture has flightctl-api-el10, not the el9 path.
    [[ "${output}" == *"quay.io/flightctl/flightctl-api-el10"* ]]
}

@test "[unit] parse_observability_images emits correct number of entries for el9" {
    source_with_fixtures
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"

    run parse_observability_images
    # Fixture images-el9.yaml has 4 entries.  Filter [INFO]/[WARN] log lines
    # that go to stderr but may be merged into $output by bats' run.
    image_lines="$(echo "${output}" | grep -v "^\[" | grep -v "^$")"
    [ "$(echo "${image_lines}" | wc -l)" -eq 4 ]
}

@test "[unit] parse_observability_images warns but exits 0 when observability file is missing" {
    # The obs file is treated as optional — a missing file produces a [WARN] and
    # returns successfully so the script can still mirror helm-chart-opts images.
    source_with_fixtures
    VARIANT="community-el9"
    DEST_REGISTRY="localhost:5000"
    OBS_IMAGES_EL9="/nonexistent/images.yaml"

    run parse_observability_images
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"not found"* ]] || [[ "${output}" == *"Skipping"* ]]
}

# ---------------------------------------------------------------------------
# [unit] Deduplication — overlapping images between helm-chart-opts and obs
# ---------------------------------------------------------------------------

@test "[unit] full dry-run deduplicates images shared between helm-chart-opts and observability" {
    # The community-el9 fixture intentionally has 'api' and 'worker' in both
    # helm-chart-opts (no tag → appVersion fallback = v0.99.0-test) and images-el9.yaml
    # (tag: latest).  These resolve to DIFFERENT source:tag pairs so they should NOT
    # be deduplicated — both lines must appear.
    # This test verifies the dedup key is (source image + tag), not just image name.
    run env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000

    [ "${status}" -eq 0 ]

    # Count unique skopeo lines in stdout (stderr is suppressed by run mixing).
    skopeo_lines="$(echo "${output}" | grep -c "^skopeo copy" || true)"
    # 4 from helm-chart-opts + 4 from observability; api:v0.99.0-test and api:latest
    # are distinct, so total is 8 — no collapsing expected for this fixture.
    [ "${skopeo_lines}" -ge 6 ]
}

@test "[unit] no duplicate skopeo commands for identical source:tag pairs" {
    # Build a fixture where the SAME image:tag appears in both sources.
    local dup_helm="${TEST_TMP}/dup-chart-opts.yaml"
    local dup_obs="${TEST_TMP}/dup-images.yaml"

    cat > "${dup_helm}" <<'YAML'
community-el9:
  images:
    grafana:
      image: docker.io/grafana/grafana
      tag: "11.0.0"
YAML

    cat > "${dup_obs}" <<'YAML'
grafana:
  image: docker.io/grafana/grafana
  tag: "11.0.0"
YAML

    run env \
        HELM_CHART_OPTS="${dup_helm}" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${dup_obs}" \
        OBS_IMAGES_EL10="${dup_obs}" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000

    [ "${status}" -eq 0 ]

    # Only one skopeo command should appear because source:tag is identical.
    skopeo_lines="$(echo "${output}" | grep -c "^skopeo copy" || true)"
    [ "${skopeo_lines}" -eq 1 ]
}

# ---------------------------------------------------------------------------
# [unit] Skopeo command format
# ---------------------------------------------------------------------------

@test "[unit] skopeo commands use --all flag for multi-arch support" {
    run env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000

    [ "${status}" -eq 0 ]
    # Every skopeo line must include --all.
    while IFS= read -r line; do
        [[ "${line}" == "skopeo copy --all"* ]] || fail "Missing --all in: ${line}"
    done < <(echo "${output}" | grep "^skopeo copy")
}

@test "[unit] skopeo commands use docker:// transport prefix for both src and dest" {
    run env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000

    [ "${status}" -eq 0 ]
    while IFS= read -r line; do
        # Line must have two docker:// tokens — one for src, one for dest.
        count="$(echo "${line}" | grep -o "docker://" | wc -l)"
        [ "${count}" -eq 2 ] || fail "Expected 2 docker:// tokens in: ${line}"
    done < <(echo "${output}" | grep "^skopeo copy")
}

@test "[unit] destination registry appears in every skopeo destination field" {
    local dest="myprivate.registry.corp:8443"

    run env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry "${dest}"

    [ "${status}" -eq 0 ]
    while IFS= read -r line; do
        [[ "${line}" == *"docker://${dest}/"* ]] || \
            fail "Dest registry not in destination field: ${line}"
    done < <(echo "${output}" | grep "^skopeo copy")
}

# ---------------------------------------------------------------------------
# [unit] Artifact manifest generation (EDM-3960)
# ---------------------------------------------------------------------------

@test "[unit] manifest file is created after dry-run" {
    run env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000

    [ "${status}" -eq 0 ]
    [ -f "${TEST_TMP}/artifact-manifest-community-el9.yaml" ]
}

@test "[unit] manifest contains expected top-level keys" {
    env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000 >/dev/null 2>&1

    local manifest="${TEST_TMP}/artifact-manifest-community-el9.yaml"
    [ -f "${manifest}" ]

    # Required top-level keys must exist.
    /tmp/yq e '.metadata' "${manifest}" | grep -q "variant"
    /tmp/yq e '.images' "${manifest}" | grep -q "\-"
    /tmp/yq e '.catalogs' "${manifest}"  # may be empty list — must exist
}

@test "[unit] manifest variant field matches --variant argument" {
    env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant redhat-el9 --dest-registry localhost:5000 >/dev/null 2>&1

    local manifest="${TEST_TMP}/artifact-manifest-redhat-el9.yaml"
    [ -f "${manifest}" ]

    local variant
    variant="$(/tmp/yq e '.metadata.variant' "${manifest}")"
    [ "${variant}" = "redhat-el9" ]
}

@test "[unit] manifest image count matches skopeo command count" {
    run env \
        HELM_CHART_OPTS="${FIXTURES}/helm-chart-opts.yaml" \
        CHART_YAML="${FIXTURES}/Chart.yaml" \
        OBS_IMAGES_EL9="${FIXTURES}/images-el9.yaml" \
        OBS_IMAGES_EL10="${FIXTURES}/images-el10.yaml" \
        RPM_SPEC="${TEST_TMP}/empty.spec" \
        "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000

    [ "${status}" -eq 0 ]

    local skopeo_count manifest_count manifest
    skopeo_count="$(echo "${output}" | grep -c "^skopeo copy" || true)"
    manifest="${TEST_TMP}/artifact-manifest-community-el9.yaml"
    manifest_count="$(/tmp/yq e '.images | length' "${manifest}")"

    [ "${skopeo_count}" -eq "${manifest_count}" ] || \
        fail "skopeo count (${skopeo_count}) != manifest image count (${manifest_count})"
}

# ---------------------------------------------------------------------------
# [integration] Tests against real repo files
# ---------------------------------------------------------------------------

# Resolve the repo root relative to this test file: test/ → scripts/air-gap/ → scripts/ → repo root
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

@test "[integration] community-el9 produces skopeo commands from real helm-chart-opts.yaml" {
    # Skip if the real file is absent (e.g., shallow clone).
    [ -f "${REPO_ROOT}/deploy/helm/helm-chart-opts.yaml" ] || \
        skip "deploy/helm/helm-chart-opts.yaml not found"

    run "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000
    [ "${status}" -eq 0 ]

    skopeo_count="$(echo "${output}" | grep -c "^skopeo copy" || true)"
    # The real file has 16 helm images + 18 observability images; expect at least 10 unique.
    [ "${skopeo_count}" -ge 10 ] || \
        fail "Expected ≥10 skopeo commands, got ${skopeo_count}"
}

@test "[integration] redhat-el9 references registry.redhat.io for at least one image" {
    [ -f "${REPO_ROOT}/deploy/helm/helm-chart-opts.yaml" ] || \
        skip "deploy/helm/helm-chart-opts.yaml not found"

    run "${SCRIPT}" --variant redhat-el9 --dest-registry localhost:5000
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"docker://registry.redhat.io"* ]]
}

@test "[integration] community-el10 produces at least one quay.io source image" {
    [ -f "${REPO_ROOT}/deploy/helm/helm-chart-opts.yaml" ] || \
        skip "deploy/helm/helm-chart-opts.yaml not found"

    run "${SCRIPT}" --variant community-el10 --dest-registry localhost:5000
    [ "${status}" -eq 0 ]
    [[ "${output}" == *"docker://quay.io"* ]]
}

@test "[integration] manifest image count is consistent with stdout command count (community-el9)" {
    [ -f "${REPO_ROOT}/deploy/helm/helm-chart-opts.yaml" ] || \
        skip "deploy/helm/helm-chart-opts.yaml not found"

    run "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000
    [ "${status}" -eq 0 ]

    skopeo_count="$(echo "${output}" | grep -c "^skopeo copy" || true)"
    manifest="${TEST_TMP}/artifact-manifest-community-el9.yaml"
    manifest_count="$(/tmp/yq e '.images | length' "${manifest}")"

    [ "${skopeo_count}" -eq "${manifest_count}" ] || \
        fail "stdout skopeo count (${skopeo_count}) != manifest images[] length (${manifest_count})"
}

@test "[integration] stdout is clean — no [INFO]/[WARN]/[ERROR] lines mixed in" {
    [ -f "${REPO_ROOT}/deploy/helm/helm-chart-opts.yaml" ] || \
        skip "deploy/helm/helm-chart-opts.yaml not found"

    # run captures merged stdout+stderr; we need only stdout.
    # Use process substitution to capture stdout and stderr separately.
    stdout_file="${TEST_TMP}/stdout.txt"
    "${SCRIPT}" --variant community-el9 --dest-registry localhost:5000 \
        > "${stdout_file}" 2>/dev/null

    # stdout must contain only skopeo lines (no log noise).
    bad_lines="$(grep -v "^skopeo copy" "${stdout_file}" | grep -v "^$" || true)"
    [ -z "${bad_lines}" ] || fail "Non-skopeo lines in stdout: ${bad_lines}"
}
