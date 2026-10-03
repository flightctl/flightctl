#!/usr/bin/env bash
set -euo pipefail

real_podman="${FLIGHTCTL_KIND_REAL_PODMAN:-}"
if [[ -z "${real_podman}" || ! -x "${real_podman}" ]]; then
    echo "FLIGHTCTL_KIND_REAL_PODMAN must name the installed Podman binary" >&2
    exit 1
fi

args=("$@")
if [[ "${args[0]:-}" == "run" ]]; then
    node_name=""
    for ((i = 1; i < ${#args[@]}; i++)); do
        case "${args[i]}" in
            --name)
                if ((i + 1 < ${#args[@]})); then
                    node_name="${args[i + 1]}"
                fi
                ;;
            --name=*)
                node_name="${args[i]#--name=}"
                ;;
        esac
    done

    # Kind v0.26's Podman provider does not set keep-groups on its node
    # containers. Preserve the invoking user's KVM group for the /dev/kvm
    # device mounted into a rootless Kind node.
    if [[ "${node_name}" == kind-* ]]; then
        exec "${real_podman}" run --group-add=keep-groups "${args[@]:1}"
    fi
fi

exec "${real_podman}" "${args[@]}"
