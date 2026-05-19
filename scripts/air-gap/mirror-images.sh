#!/usr/bin/env bash
# =============================================================================
# mirror-images.sh — RHEM Air-gapped Artifact Preparation Script
#
# PURPOSE
#   Enumerates all container images required for a given RHEM chart variant,
#   generates skopeo copy commands to mirror them to a local registry, and
#   writes a machine-readable artifact manifest (YAML) that lists every image
#   and RPM needed for a fully offline installation.
#
# USAGE
#   ./scripts/air-gap/mirror-images.sh --variant <variant> \
#       --dest-registry <registry> [--execute] [--help]
#
# STORIES
#   EDM-3957  CLI scaffold and argument parsing
#   EDM-3958  Parse helm-chart-opts.yaml and generate skopeo commands
#   EDM-3959  Parse observability images from packaging/images/*/images.yaml
#   EDM-3960  Generate artifact manifest YAML
#
# DEPENDENCIES
#   Required: yq v4+ (https://github.com/mikefarah/yq)
#   Optional: skopeo  (only needed when --execute is passed)
# =============================================================================

set -eo pipefail

# =============================================================================
# CONSTANTS — paths relative to the repository root
# =============================================================================

# Resolve the directory that contains this script, then walk up two levels to
# the repository root.  Using BASH_SOURCE[0] (not $0) so the script can be
# sourced or called via symlink without breaking the path calculation.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"

# Primary image source: per-variant image map.
# Structure: <variant>.images.<service>.{image,tag}
HELM_CHART_OPTS="${REPO_ROOT}/deploy/helm/helm-chart-opts.yaml"

# Helm chart metadata — used to read appVersion as the fallback tag for
# flightctl component images that have no explicit tag in helm-chart-opts.yaml.
CHART_YAML="${REPO_ROOT}/deploy/helm/flightctl/Chart.yaml"

# Secondary image source: observability images installed by RPMs.
# These are NOT listed in helm-chart-opts.yaml so they must be sourced here.
OBS_IMAGES_EL9="${REPO_ROOT}/packaging/images/el9/images.yaml"
OBS_IMAGES_EL10="${REPO_ROOT}/packaging/images/el10/images.yaml"

# RPM spec — parsed for runtime Requires to populate the manifest's rpms[] list.
RPM_SPEC="${REPO_ROOT}/packaging/rpm/flightctl.spec"

# Script version, embedded in the artifact manifest metadata section.
SCRIPT_VERSION="1.0.0"

# Allowed values for the --variant flag.  Any other value is rejected.
VALID_VARIANTS=("community-el9" "community-el10" "redhat-el9" "redhat-el10")

# =============================================================================
# LOGGING HELPERS
#
# All progress messages go to stderr so that only skopeo commands appear on
# stdout — making it easy to pipe output to a shell or redirect to a file.
# =============================================================================

log_info()  { echo "[INFO]  $*" >&2; }
log_warn()  { echo "[WARN]  $*" >&2; }
log_error() { echo "[ERROR] $*" >&2; }

# =============================================================================
# USAGE
# =============================================================================

usage() {
    cat >&2 <<EOF
Usage: $(basename "$0") --variant <variant> --dest-registry <registry> [OPTIONS]

Enumerate RHEM artifacts and generate skopeo mirror commands for air-gapped
installation. By default the commands are printed to stdout. Pass --execute to
run them immediately.

Required flags:
  --variant <variant>
      Chart variant to enumerate. One of:
        community-el9   upstream community build for RHEL 9
        community-el10  upstream community build for RHEL 10
        redhat-el9      Red Hat downstream build for RHEL 9
        redhat-el10     Red Hat downstream build for RHEL 10

  --dest-registry <host:port>
      Destination registry URL — no scheme, no trailing slash.
      Example: local-registry.example.com:5000

Options:
  --execute   Execute skopeo commands immediately in addition to printing them.
              Requires skopeo to be installed and the destination registry to
              be reachable. Without this flag the script is always a dry-run.
  --help, -h  Print this message and exit.

Output:
  stdout                         — one skopeo copy command per image
  stderr                         — progress logs ([INFO] / [WARN] / [ERROR])
  artifact-manifest-<variant>.yaml  — machine-readable artifact manifest

Examples:
  # Dry-run: print all commands for the community-el9 variant
  ./scripts/air-gap/mirror-images.sh \\
      --variant community-el9 \\
      --dest-registry local-registry.example.com:5000

  # Execute: mirror images to a running local registry
  ./scripts/air-gap/mirror-images.sh \\
      --variant redhat-el9 \\
      --dest-registry local-registry.example.com:5000 \\
      --execute
EOF
}

# =============================================================================
# DEPENDENCY CHECK
#
# yq is required to parse YAML files.  skopeo is only needed when --execute is
# passed, but we warn early if it is missing so users know before they wait for
# all the parse work to complete.
# =============================================================================

check_dependencies() {
    # yq v4 is required; yq v3 has incompatible syntax (e.g. uses "." vs "e")
    if ! command -v yq &>/dev/null; then
        log_error "yq is required but not installed."
        log_error "Install it with: dnf install yq  (RHEL) or see https://github.com/mikefarah/yq"
        exit 1
    fi

    # Validate that we have yq v4+.  v4 prints "yq (https://github.com/mikefarah/yq)".
    # v3 prints "yq version 3.x.x". Check for the v4 signature.
    if ! yq --version 2>&1 | grep -q 'mikefarah'; then
        log_warn "yq does not appear to be mikefarah/yq v4. Parsing may fail."
        log_warn "Install v4 from: https://github.com/mikefarah/yq/releases"
    fi

    # skopeo is optional unless --execute was requested
    if [[ "${EXECUTE}" == "true" ]] && ! command -v skopeo &>/dev/null; then
        log_error "skopeo is required when --execute is set but was not found."
        log_error "Install it with: dnf install skopeo"
        exit 1
    elif ! command -v skopeo &>/dev/null; then
        log_warn "skopeo not found — commands will be printed but not executed."
    fi

    # Verify the required source files exist inside this repository checkout
    for f in "${HELM_CHART_OPTS}" "${CHART_YAML}" "${RPM_SPEC}"; do
        if [[ ! -f "${f}" ]]; then
            log_error "Required file not found: ${f}"
            log_error "Run this script from the flightctl repository root, or ensure the repo is up to date."
            exit 1
        fi
    done
}

# =============================================================================
# ARGUMENT PARSING  (EDM-3957)
#
# Uses a manual while-case loop rather than getopt for maximum portability
# across RHEL 8/9/10 without requiring util-linux extras.
# =============================================================================

parse_args() {
    # Initialise all flags to safe defaults before parsing
    VARIANT=""
    DEST_REGISTRY=""
    EXECUTE="false"

    # Require at least one argument before entering the loop
    if [[ $# -eq 0 ]]; then
        usage
        exit 1
    fi

    while [[ $# -gt 0 ]]; do
        case "$1" in
            --variant)
                # The value is the next positional argument
                [[ -z "${2:-}" ]] && { log_error "--variant requires a value"; exit 1; }
                VARIANT="$2"
                shift 2
                ;;
            --dest-registry)
                [[ -z "${2:-}" ]] && { log_error "--dest-registry requires a value"; exit 1; }
                DEST_REGISTRY="$2"
                shift 2
                ;;
            --execute)
                EXECUTE="true"
                shift
                ;;
            --dry-run)
                # Explicit dry-run is the default behaviour; flag is accepted
                # for clarity but changes nothing.
                EXECUTE="false"
                shift
                ;;
            --help|-h)
                usage
                exit 0
                ;;
            *)
                log_error "Unknown flag: $1"
                usage
                exit 1
                ;;
        esac
    done
}

# =============================================================================
# INPUT VALIDATION  (EDM-3957)
# =============================================================================

validate_args() {
    local valid=false

    # Ensure --variant was provided and is one of the four allowed values
    if [[ -z "${VARIANT}" ]]; then
        log_error "--variant is required"
        usage
        exit 1
    fi

    for v in "${VALID_VARIANTS[@]}"; do
        [[ "${VARIANT}" == "${v}" ]] && valid=true && break
    done

    if [[ "${valid}" != "true" ]]; then
        log_error "Invalid variant '${VARIANT}'"
        log_error "Allowed values: ${VALID_VARIANTS[*]}"
        exit 1
    fi

    # Ensure --dest-registry was provided and looks like host:port or hostname
    if [[ -z "${DEST_REGISTRY}" ]]; then
        log_error "--dest-registry is required"
        usage
        exit 1
    fi

    # Reject values with a scheme prefix — skopeo expects host:port, not https://...
    if [[ "${DEST_REGISTRY}" =~ ^https?:// ]]; then
        log_error "--dest-registry must not include a URL scheme (https:// or http://)"
        log_error "Example: local-registry.example.com:5000"
        exit 1
    fi
}

# =============================================================================
# CHART APP VERSION
#
# Images for core flightctl services (api, worker, periodic, etc.) omit the
# tag field in helm-chart-opts.yaml.  At packaging time the tag is set to the
# release version.  When running from a development checkout we use the
# Chart.yaml appVersion as the best available fallback.
# =============================================================================

get_app_version() {
    # Read the appVersion scalar from Chart.yaml.
    # yq e returns the raw string value without surrounding quotes.
    local ver
    ver=$(yq e '.appVersion' "${CHART_YAML}")

    # Warn if the chart still carries "latest" — this means the repo was not
    # built with a release tag, so mirrored images will track :latest.
    if [[ "${ver}" == "latest" ]]; then
        log_warn "Chart.yaml appVersion is 'latest' — tagless images will be mirrored as :latest."
        log_warn "For reproducible air-gapped installs, use a release-tagged checkout."
    fi

    echo "${ver}"
}

# =============================================================================
# DESTINATION PATH CALCULATION
#
# Strip the source registry hostname from the image reference, then prepend
# the caller-supplied destination registry.  This preserves the full image
# path (organisation/name) on the destination side.
#
# Example:
#   source:  quay.io/flightctl/flightctl-api-el9
#   dest:    local-registry.example.com:5000/flightctl/flightctl-api-el9
# =============================================================================

image_to_dest() {
    local source_image="$1"   # Full source image reference, without tag
    local tag="$2"            # Tag to apply at both source and destination

    # Remove the leading registry hostname.
    # Parameter expansion "##*/" strips everything up to and including the
    # last "/" when the image path has multiple segments.
    # For "quay.io/flightctl/flightctl-api-el9" this produces
    #   "flightctl/flightctl-api-el9"  (strips "quay.io/")
    # We need to keep the full path (org + name), so strip only the first
    # component (the hostname).  Use "#*/" to strip up to the first "/" only.
    local path_without_host="${source_image#*/}"

    echo "${DEST_REGISTRY}/${path_without_host}:${tag}"
}

# =============================================================================
# PARSE HELM-CHART-OPTS.YAML  (EDM-3958)
#
# The file contains one top-level key per variant.  Under each variant, the
# "images" map lists every container image the Helm chart deploys.
#
# Format:
#   community-el9:
#     images:
#       api:
#         image: quay.io/flightctl/flightctl-api-el9   # no tag → use appVersion
#       db:
#         image: quay.io/sclorg/postgresql-16-c9s
#         tag: "20250214"                               # explicit tag
#
# This function echoes lines in the format:
#   <source_image>:<tag> <dest_image>:<tag>
# which is consumed by generate_skopeo_commands.
# =============================================================================

parse_helm_chart_opts() {
    local app_version="$1"   # Fallback tag for images with no tag field

    log_info "Parsing helm-chart-opts.yaml for variant '${VARIANT}'..."

    # Confirm the requested variant key exists in the file
    if ! yq e "has(\"${VARIANT}\")" "${HELM_CHART_OPTS}" | grep -q "true"; then
        log_error "Variant '${VARIANT}' not found in ${HELM_CHART_OPTS}"
        exit 1
    fi

    # Retrieve the list of service keys under <variant>.images.
    # yq outputs one key per line; we read them into an array.
    local keys
    mapfile -t keys < <(yq e ".\"${VARIANT}\".images | keys | .[]" "${HELM_CHART_OPTS}")

    if [[ ${#keys[@]} -eq 0 ]]; then
        log_warn "No images found for variant '${VARIANT}' in helm-chart-opts.yaml"
        return
    fi

    log_info "Found ${#keys[@]} image entries in helm-chart-opts.yaml"

    for key in "${keys[@]}"; do
        # Read the full image reference (registry + org + name, no tag)
        local image
        image=$(yq e ".\"${VARIANT}\".images.${key}.image" "${HELM_CHART_OPTS}")

        # Read the tag; yq returns "null" when the field is absent.
        # Replace "null" or empty string with the chart's appVersion.
        local tag
        tag=$(yq e ".\"${VARIANT}\".images.${key}.tag // \"\"" "${HELM_CHART_OPTS}")
        [[ -z "${tag}" || "${tag}" == "null" ]] && tag="${app_version}"

        # Compute the destination reference
        local dest
        dest=$(image_to_dest "${image}" "${tag}")

        # Emit a single space-separated pair: "SOURCE:TAG DEST:TAG"
        echo "${image}:${tag} ${dest}"
    done
}

# =============================================================================
# PARSE OBSERVABILITY IMAGES  (EDM-3959)
#
# Observability images (grafana, prometheus, alertmanager, etc.) are installed
# by the flightctl-observability RPM and are therefore NOT listed in
# helm-chart-opts.yaml.  They must be sourced from the separate per-OS-version
# images.yaml file.
#
# NOTE: Some observability images (e.g. downstream grafana, prometheus) are
# only available in the Red Hat internal (downstream) registry.  This function
# emits a warning when such images are detected so the operator knows they need
# access to that registry during the preparation phase.
#
# Format of packaging/images/el9/images.yaml:
#   api:
#     image: quay.io/flightctl/flightctl-api-el9
#     tag: latest
#   grafana:
#     image: docker.io/grafana/grafana
#     tag: latest
#
# This function echoes the same "SOURCE:TAG DEST:TAG" format as
# parse_helm_chart_opts, so both outputs can be piped to the same consumer.
# =============================================================================

parse_observability_images() {
    # Select the correct observability file based on the OS version embedded
    # in the variant name (el9 or el10).
    local obs_file
    if [[ "${VARIANT}" == *"el9"* ]]; then
        obs_file="${OBS_IMAGES_EL9}"
    else
        obs_file="${OBS_IMAGES_EL10}"
    fi

    # The observability file is optional — community variants may not have
    # one if the downstream observability images are not publicly available.
    if [[ ! -f "${obs_file}" ]]; then
        log_warn "Observability images file not found: ${obs_file}"
        log_warn "Skipping observability image enumeration."
        return 0
    fi

    log_info "Parsing observability images from $(basename "$(dirname "${obs_file}")")/$(basename "${obs_file}")..."

    # Read all top-level keys (service names) from the images.yaml file
    local keys
    mapfile -t keys < <(yq e '. | keys | .[]' "${obs_file}")

    log_info "Found ${#keys[@]} observability image entries"

    for key in "${keys[@]}"; do
        local image tag
        image=$(yq e ".${key}.image" "${obs_file}")
        tag=$(yq e ".${key}.tag" "${obs_file}")

        # Guard against malformed entries
        if [[ -z "${image}" || "${image}" == "null" ]]; then
            log_warn "Skipping observability entry '${key}': missing image field"
            continue
        fi
        [[ -z "${tag}" || "${tag}" == "null" ]] && tag="latest"

        # Warn the operator if this image lives in a registry that requires
        # downstream (Red Hat internal) access — they will need network access
        # to that registry during the preparation phase on the connected system.
        if [[ "${image}" == registry.redhat.io/* ]]; then
            log_warn "Observability image '${key}' requires downstream registry access:"
            log_warn "  ${image}:${tag}"
            log_warn "  Ensure the connected system has credentials for registry.redhat.io"
        fi

        local dest
        dest=$(image_to_dest "${image}" "${tag}")

        echo "${image}:${tag} ${dest}"
    done
}

# =============================================================================
# SKOPEO COMMAND GENERATION  (EDM-3957 / EDM-3958 / EDM-3959)
#
# Collects all source→destination image pairs from both YAML sources, dedupes
# them (the same image may appear in both files), then either prints or
# executes a skopeo copy command for each pair.
#
# Each generated command uses:
#   docker://  transport for both source and destination
#   --all      to copy all manifests (multi-arch support)
#
# When --execute is NOT set, commands are printed to stdout only — users can
# review them, redirect them to a script, or pipe them to bash.
# =============================================================================

generate_skopeo_commands() {
    local app_version="$1"

    log_info "Generating skopeo copy commands..."

    # Collect all image pairs from both sources into a temporary file so we
    # can deduplicate before processing.  Using a temp file avoids subshell
    # variable scope issues with process substitution on bash < 5.
    local tmp_pairs
    tmp_pairs=$(mktemp)
    # Ensure the temp file is removed when the script exits or errors
    trap 'rm -f "${tmp_pairs}"' EXIT

    # Source 1: Helm chart images
    parse_helm_chart_opts "${app_version}" >> "${tmp_pairs}"

    # Source 2: Observability RPM images
    parse_observability_images >> "${tmp_pairs}"

    # Deduplicate by source image reference (first column).
    # 'sort -u' on the whole line is sufficient since the dest is derived
    # deterministically from the source.
    local unique_pairs
    unique_pairs=$(sort -u "${tmp_pairs}")

    local total
    total=$(echo "${unique_pairs}" | grep -c . || true)
    log_info "Total unique images to mirror: ${total}"

    # Track images for manifest generation (written to a global array)
    MANIFEST_IMAGES=()

    # Process each deduplicated source→dest pair
    while IFS=' ' read -r source dest; do
        [[ -z "${source}" ]] && continue   # skip blank lines

        # Build the skopeo command.  We copy all manifests (--all) so that
        # multi-architecture images are preserved in the destination registry.
        local cmd="skopeo copy --all docker://${source} docker://${dest}"

        # Always print the command to stdout — this serves as the dry-run
        # output and also lets users capture or audit what was executed.
        echo "${cmd}"

        # Record the pair for the artifact manifest
        MANIFEST_IMAGES+=("${source} ${dest}")

        # Optionally execute the command immediately
        if [[ "${EXECUTE}" == "true" ]]; then
            log_info "Executing: ${cmd}"
            if ! ${cmd}; then
                # Skopeo errors are non-fatal: log and continue so one
                # unavailable image does not abort the entire mirror run.
                log_warn "skopeo copy failed for ${source} — continuing"
            fi
        fi
    done <<< "${unique_pairs}"

    rm -f "${tmp_pairs}"
    # Remove the EXIT trap now that we cleaned up manually
    trap - EXIT
}

# =============================================================================
# RPM DEPENDENCY PARSING  (EDM-3960)
#
# Reads the flightctl RPM spec file and extracts all runtime Requires lines.
# These are included in the artifact manifest so operators know exactly which
# RPMs must be present in the local repository before installing flightctl.
#
# The spec uses multiple sub-packages (%package cli, agent, selinux, etc.)
# each with their own Requires lines.  We collect all of them.
# =============================================================================

parse_rpm_requires() {
    # Grep all "Requires:" lines (but not BuildRequires:) then extract the
    # package name token (second field).  Macro references like
    # "flightctl-selinux = %{version}" are reduced to just "flightctl-selinux"
    # by stripping everything from " =" onwards.
    grep -E '^Requires:' "${RPM_SPEC}" \
        | awk '{print $2}' \
        | sed 's/=.*//' \
        | grep -v '^/' \
        | sort -u
}

# =============================================================================
# ARTIFACT MANIFEST GENERATION  (EDM-3960)
#
# Writes artifact-manifest-<variant>.yaml in the current working directory.
# The manifest is:
#   - Machine-readable: valid YAML, consistent structure
#   - Human-readable: comments, clear section headers
#   - Auditable: includes generation timestamp and script version
#
# Manifest sections:
#   metadata   — variant, timestamp, script version
#   images[]   — source + destination for every image to be mirrored
#   rpms[]     — runtime RPM packages needed for RHEL installation
#   catalogs[] — reserved for future catalog content import (EDM future scope)
# =============================================================================

generate_manifest() {
    local manifest_file="artifact-manifest-${VARIANT}.yaml"
    local generated_at
    generated_at=$(date -u +"%Y-%m-%dT%H:%M:%SZ")

    log_info "Writing artifact manifest to ${manifest_file}..."

    {
        # ---------- file header ----------
        echo "# RHEM Air-gapped Installation Artifact Manifest"
        echo "# Generated by scripts/air-gap/mirror-images.sh"
        echo "# Re-run the script to regenerate this file with updated image references."
        echo "#"
        echo "# To mirror all listed images, pipe this script's stdout to bash:"
        echo "#   ./scripts/air-gap/mirror-images.sh --variant ${VARIANT} \\"
        echo "#       --dest-registry <registry> | bash"
        echo ""

        # ---------- metadata section ----------
        echo "metadata:"
        echo "  # Chart variant this manifest was generated for"
        echo "  variant: ${VARIANT}"
        echo "  # UTC timestamp of manifest generation"
        echo "  generated_at: \"${generated_at}\""
        echo "  # Version of this script; increment when the script logic changes"
        echo "  script_version: \"${SCRIPT_VERSION}\""
        echo ""

        # ---------- images section ----------
        echo "# Container images that must be mirrored to the local registry."
        echo "# Use the generated skopeo commands (stdout) to perform the mirror."
        echo "images:"
        for pair in "${MANIFEST_IMAGES[@]}"; do
            # pair format: "source:tag dest:tag"
            local src dst
            src=$(echo "${pair}" | awk '{print $1}')
            dst=$(echo "${pair}" | awk '{print $2}')
            echo "  - source: ${src}"
            echo "    destination: ${dst}"
        done
        echo ""

        # ---------- rpms section ----------
        echo "# RPM packages required for RHEL-based flightctl installation."
        echo "# Mirror these to a local dnf repository using dnf reposync before"
        echo "# attempting offline RHEL installation (see Epic 2 documentation)."
        echo "rpms:"
        # Include the top-level flightctl packages explicitly — they are not
        # listed as Requires of themselves in the spec.
        for pkg in flightctl-cli flightctl-agent flightctl-selinux flightctl-services flightctl-observability; do
            echo "  - ${pkg}"
        done
        # Append transitive runtime dependencies parsed from the spec
        while IFS= read -r pkg; do
            # Skip empty lines and the flightctl-* packages already added above
            [[ -z "${pkg}" ]] && continue
            [[ "${pkg}" == flightctl-* ]] && continue
            echo "  - ${pkg}"
        done < <(parse_rpm_requires)
        echo ""

        # ---------- catalogs section ----------
        echo "# Reserved for future catalog content import support (EDM-future scope)."
        echo "# Populate when catalog import is implemented."
        echo "catalogs: []"

    } > "${manifest_file}"

    log_info "Manifest written: ${manifest_file}"
    log_info "  Images: ${#MANIFEST_IMAGES[@]}"
}

# =============================================================================
# MAIN
#
# Orchestrates the full workflow:
#   1. Parse and validate arguments
#   2. Check tool dependencies
#   3. Read chart appVersion (tag fallback)
#   4. Generate and (optionally) execute skopeo copy commands
#   5. Write the artifact manifest YAML
# =============================================================================

main() {
    parse_args "$@"
    validate_args
    check_dependencies

    log_info "Starting artifact enumeration"
    log_info "  Variant:          ${VARIANT}"
    log_info "  Dest registry:    ${DEST_REGISTRY}"
    log_info "  Execute commands: ${EXECUTE}"

    # Resolve the fallback tag for images without an explicit tag in the chart
    local app_version
    app_version=$(get_app_version)
    log_info "  Chart appVersion: ${app_version} (used as tag fallback)"

    # Generate skopeo commands (printed to stdout; optionally executed)
    generate_skopeo_commands "${app_version}"

    # Write the artifact manifest to disk
    generate_manifest

    log_info "Done."
    if [[ "${EXECUTE}" != "true" ]]; then
        log_info "Commands were printed but not executed."
        log_info "To execute, re-run with --execute, or pipe stdout to bash:"
        log_info "  $0 --variant ${VARIANT} --dest-registry ${DEST_REGISTRY} | bash"
    fi
}

# Run main only when the script is executed directly (not sourced in tests)
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
    main "$@"
fi
