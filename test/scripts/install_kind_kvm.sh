#!/usr/bin/env bash
set -euo pipefail

# Development/e2e bootstrap only. Production clusters manage their own plugin.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
enabled=${IMAGEBUILDER_WORKER_ENABLED:-true}
accelerator=${IMAGEBUILDER_VM_ACCELERATOR:-kvm}
context=${KIND_KUBE_CONTEXT:-kind-kind}

case "$enabled" in
    true|false) ;;
    *) echo 'IMAGEBUILDER_WORKER_ENABLED must be true or false.' >&2; exit 1 ;;
esac
case "$accelerator" in
    kvm|tcg) ;;
    *) echo 'IMAGEBUILDER_VM_ACCELERATOR must be kvm or tcg.' >&2; exit 1 ;;
esac
if [[ "$enabled" == false ]]; then
    echo 'Skipping test KVM plugin (image worker disabled).'
    exit 0
fi
if [[ $(uname -m) != x86_64 ]]; then
    echo 'The imagebuilder VM worker supports x86_64 hosts only. Set IMAGEBUILDER_WORKER_ENABLED=false on other architectures.' >&2
    exit 1
fi
if [[ "$accelerator" == tcg ]]; then
    echo 'Skipping test KVM plugin (TCG development ImageBuild only; ImageExport requires KVM).'
    exit 0
fi
if [[ ! -c /dev/kvm || ! -r /dev/kvm || ! -w /dev/kvm ]]; then
    cat >&2 <<'MESSAGE'
The enabled imagebuilder VM worker requires an x86_64 host with readable/writable /dev/kvm.
Enable virtualization and grant the current user KVM access, then recreate the kind cluster.
For a deployment without image building, set IMAGEBUILDER_WORKER_ENABLED=false.
For development ImageBuild only, set IMAGEBUILDER_VM_ACCELERATOR=tcg; ImageExport requires KVM.
MESSAGE
    exit 1
fi
if [[ ${1:-} == --check-host ]]; then
    exit 0
fi

kubectl --context "$context" apply -f "${SCRIPT_DIR}/kind-kvm-device-plugin.yaml"
kubectl --context "$context" -n kube-system rollout status daemonset/flightctl-test-kvm-device-plugin --timeout=120s
if ! kubectl --context "$context" wait nodes --all     --for='jsonpath={.status.allocatable.devices\.anza-labs\.dev/kvm}=10' --timeout=60s; then
    cat >&2 <<'MESSAGE'
The kind nodes did not advertise the test KVM resource devices.anza-labs.dev/kvm.
Recreate the cluster with test/scripts/create_cluster.sh so /dev/kvm is mounted into its node.
Check the test device-plugin logs in kube-system if the device is already mounted.
MESSAGE
    exit 1
fi
