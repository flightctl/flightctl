"""Version negotiation probe tests.

These run once per API operation to verify that the version negotiation
middleware behaves correctly:
- Invalid version header -> 406 Not Acceptable
- Missing version header -> response includes default version
"""
import pytest
from hypothesis import HealthCheck, settings

from conftest import schema

VERSION_HEADER = "Flightctl-API-Version"
SUPPORTED_HEADER = "Flightctl-API-Versions-Supported"
INVALID_VERSION = "v999"


@schema.parametrize()
@settings(max_examples=5, suppress_health_check=[HealthCheck.filter_too_much])
def test_invalid_version_rejected(case):
    """Invalid Flightctl-API-Version must return 406 Not Acceptable."""
    response = case.call(
        headers={VERSION_HEADER: INVALID_VERSION},
        verify=False,
    )
    if response.status_code in (404, 405):
        pytest.skip(f"Endpoint returned {response.status_code}")

    assert response.status_code == 406, (
        f"Expected 406 for version '{INVALID_VERSION}', got {response.status_code}"
    )
    supported = response.headers.get(SUPPORTED_HEADER.lower())
    assert supported, f"Missing {SUPPORTED_HEADER} header in 406 response"


@schema.parametrize()
@settings(max_examples=5, suppress_health_check=[HealthCheck.filter_too_much])
def test_no_version_header_fallback(case):
    """Request without version header must include version in response."""
    response = case.call(verify=False, timeout=30)

    if response.status_code in (404, 405):
        pytest.skip(f"Endpoint returned {response.status_code}")

    version_value = response.headers.get(VERSION_HEADER.lower())
    assert version_value, (
        f"No {VERSION_HEADER} in response when no version header sent"
    )
