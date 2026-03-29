#!/bin/bash
#
# Reproducer for helm 4 kstatus watcher "send on closed channel" panic.
#
# The race is internal to helm's rollback logic:
#   1. Chart deploys successfully, pods start
#   2. Failing pod crashes after 20s delay
#   3. --rollback-on-failure detects failure, starts rollback
#   4. During rollback, kstatus watcher context cancels
#   5. Status-check goroutine sends on closed channel -> panic
#
# Requires aggressive CPU throttling from the host to widen the race window.

set -euo pipefail

CHART_DIR="/tmp/failing-chart"
NAMESPACE="test"
KUBECONFIG="/var/lib/microshift/resources/kubeadmin/kubeconfig"
RUNS=${FLAKE_RUNS:-50}
LOGDIR="/tmp/flake-logs"
RELEASE="test-chart"

if ! helm version 2>/dev/null | grep -q 'v4\.'; then
    echo "ERROR: helm v4.x required (got: $(helm version 2>/dev/null))"
    exit 1
fi

if [ ! -f "$KUBECONFIG" ]; then
    echo "ERROR: kubeconfig not found at $KUBECONFIG"
    exit 1
fi

kubectl --kubeconfig "$KUBECONFIG" create namespace "$NAMESPACE" 2>/dev/null || true
crictl pull quay.io/flightctl-tests/alpine:v1 2>/dev/null || true

rm -rf "$LOGDIR"
mkdir -p "$LOGDIR"

TOTAL_PANICS=0
TOTAL_FAILURES=0
TOTAL_OK=0

ts() { date +%H:%M:%S.%3N; }

cleanup() {
    helm uninstall "$RELEASE" --namespace "$NAMESPACE" --kubeconfig "$KUBECONFIG" 2>/dev/null || true
}
trap cleanup EXIT

echo "Reproducing helm 4 kstatus watcher race condition"
echo "  helm $(helm version --short 2>/dev/null)"
echo "  chart: $CHART_DIR"
echo "  namespace: $NAMESPACE"
echo "  runs: $RUNS"
echo ""
echo "Strategy: install chart with failing pod (20s delay), let rollback trigger, repeat"
echo "          Host must throttle VM CPU to ~20% for race window to open"
echo "---"

for i in $(seq 1 "$RUNS"); do
    LOG="$LOGDIR/run-$i.log"

    # Uninstall previous release completely
    helm uninstall "$RELEASE" --namespace "$NAMESPACE" --kubeconfig "$KUBECONFIG" --wait 2>/dev/null || true

    # Wait for pods to be fully cleaned up
    for c in $(seq 1 15); do
        PODS=$(kubectl --kubeconfig "$KUBECONFIG" get pods -n "$NAMESPACE" --no-headers 2>/dev/null | wc -l)
        if [ "$PODS" -eq 0 ]; then break; fi
        sleep 1
    done

    # Fresh install with --rollback-on-failure
    # The failing pod will crash after 20s, triggering rollback where the race lives
    START=$(date +%s%3N)
    echo -n "[$(ts)] [$i/$RUNS] helm upgrade --install --rollback-on-failure ... "

    OUTPUT=$(helm upgrade "$RELEASE" "$CHART_DIR" \
        --install \
        --namespace "$NAMESPACE" \
        --kubeconfig "$KUBECONFIG" \
        --rollback-on-failure 2>&1) || true
    RC=$?
    END=$(date +%s%3N)
    ELAPSED=$((END - START))

    if echo "$OUTPUT" | grep -qi "panic"; then
        TOTAL_PANICS=$((TOTAL_PANICS + 1))
        TOTAL_FAILURES=$((TOTAL_FAILURES + 1))
        echo "PANIC (${ELAPSED}ms)"
        echo "[$(ts)] [$i/$RUNS] *** PANIC ***" >> "$LOG"
        echo "$OUTPUT" >> "$LOG"
        echo "$OUTPUT"
    elif [ $RC -ne 0 ]; then
        TOTAL_FAILURES=$((TOTAL_FAILURES + 1))
        ERRMSG=$(echo "$OUTPUT" | tail -1)
        echo "FAILED rc=$RC (${ELAPSED}ms): $ERRMSG"
        echo "[$(ts)] [$i/$RUNS] FAILED rc=$RC" >> "$LOG"
        echo "$OUTPUT" >> "$LOG"
    else
        TOTAL_OK=$((TOTAL_OK + 1))
        echo "ok (${ELAPSED}ms)"
    fi
done

echo ""
echo "=== Results ==="
echo "Runs:           $RUNS"
echo "OK:             $TOTAL_OK"
echo "Failures:       $TOTAL_FAILURES"
echo "Panics:         $TOTAL_PANICS"

if [ "$TOTAL_PANICS" -gt 0 ]; then
    echo ""
    echo "Race condition reproduced!"
    for f in "$LOGDIR"/run-*.log; do
        [ -f "$f" ] || continue
        if grep -q "PANIC" "$f"; then
            echo "=== $(basename "$f") ==="
            cat "$f"
        fi
    done
    exit 1
fi
