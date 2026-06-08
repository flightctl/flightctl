#!/bin/sh
# Runs a single schemathesis test suite inside the container.
# Usage: run_suite.sh <spec_path> <config_toml_path> <results_dir>

SPEC="$1"
CONFIG="$2"
RESULTS="$3"

# Suppress urllib3 TLS warnings globally (affects both schemathesis and pytest).
# PYTHONWARNINGS env var doesn't work with Python 3.14t (can't resolve module at startup),
# so we use the simpler module-level filter that matches any urllib3 warning.
export PYTHONWARNINGS="ignore:::urllib3"

# Symlink config for schemathesis discovery (searches from CWD)
ln -sf "$CONFIG" /app/schemathesis.toml

# Build the schemathesis run command
ST_CMD="schemathesis run $SPEC --report junit --report-dir $RESULTS"
[ -n "${CI:-}" ] && ST_CMD="$ST_CMD --output-sanitize true"

# Step 1: Run schemathesis CLI tests (stateful, passive checks, coverage)
if [ -z "${CI:-}" ]; then
    # Interactive: use script(1) to provide a pseudo-TTY for progress output
    script -qefc "$ST_CMD" "$RESULTS/st_output.raw"
    ST_EXIT=$?
    python3 /app/config/render_log.py "$RESULTS/st_output.raw" "$RESULTS/st_output.log"
    rm -f "$RESULTS/st_output.raw"
else
    # CI: run directly, no TTY needed
    $ST_CMD >"$RESULTS/st_output.log" 2>&1
    ST_EXIT=$?
    cat "$RESULTS/st_output.log"
fi

# Step 2: Run pytest-based version probe tests
export SPEC_PATH="$SPEC"
export API_VERSION=$(basename $(dirname "$CONFIG"))
timeout 600 pytest /app/config/test_version_probes.py \
    -p no:cacheprovider \
    -W ignore::hypothesis.errors.HypothesisSideeffectWarning \
    -W ignore::urllib3.exceptions.InsecureRequestWarning \
    --tb=short \
    --junit-xml="$RESULTS/junit-probes.xml" \
    -v >"$RESULTS/pytest_output.log" 2>&1
PYTEST_EXIT=$?
cat "$RESULTS/pytest_output.log"
cat "$RESULTS/pytest_output.log" >> "$RESULTS/st_output.log"
rm -f "$RESULTS/pytest_output.log"

# Exit with failure if either step failed
if [ $ST_EXIT -ne 0 ]; then
    exit $ST_EXIT
fi
exit $PYTEST_EXIT
