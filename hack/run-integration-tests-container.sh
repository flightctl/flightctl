#!/bin/bash

set -e

# Build the integration test container
echo "Building integration test container..."
podman build -f Dockerfile.integration-test -t flightctl-integration-test .

# Set default test pattern if not provided
TEST_PATTERN="${1:-./test/integration/...}"
# Support for test filtering (like -run TestName)
TEST_RUN="${2:-}"

echo "Running integration tests with pattern: $TEST_PATTERN"
if [ -n "$TEST_RUN" ]; then
    echo "Filtering tests with: -run $TEST_RUN"
fi

# Create reports directory on host to avoid permission issues
mkdir -p reports

# Run the container with everything inside
podman run --rm --privileged \
    -v "$(pwd):/workspace" \
    -e FLIGHTCTL_KV_PASSWORD=adminpass \
    -e FLIGHTCTL_POSTGRESQL_MASTER_PASSWORD=adminpass \
    -e FLIGHTCTL_POSTGRESQL_USER_PASSWORD=adminpass \
    -e DB_APP_PASSWORD=adminpass \
    -e DB_MIGRATION_PASSWORD=adminpass \
    -e CGO_ENABLED=1 \
    -e TRACE_TESTS=false \
    -e GORM_TRACE_ENFORCE_FATAL=true \
    -e GORM_TRACE_INCLUDE_QUERY_VARIABLES=true \
    flightctl-integration-test \
    bash -c "
        # Change to workspace directory
        cd /workspace
        
        # Services are already started by the entrypoint
        echo 'PostgreSQL and Redis are ready!'
        
        # Reports directory already exists on host
        echo 'Reports directory is ready'
        
        # Run the integration tests
        go run -modfile=tools/go.mod gotest.tools/gotestsum \
            --format=pkgname \
            --junitfile reports/junit_integration_test.xml \
            -- \
            -count=1 -race \
            -coverprofile=reports/integration-coverage.out \
            -timeout 30m \
            ${TEST_RUN:+-run $TEST_RUN} \
            $TEST_PATTERN
    "

echo "Integration tests completed!"
echo "Reports available in ./reports/"