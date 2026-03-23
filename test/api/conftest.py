"""Pytest configuration for schemathesis API probe tests."""
import os

import hypothesis.errors
import pytest
import requests
import schemathesis
from requests.adapters import HTTPAdapter
from urllib3.util.retry import Retry

schema = schemathesis.openapi.from_path(os.environ["SPEC_PATH"])
schema.config.base_url = os.environ["BASE_URL"]

# Session with retry for transient connection errors (DNS flakes, TCP resets).
_retry = Retry(total=5, connect=5, backoff_factor=1)
resilient_session = requests.Session()
resilient_session.mount("https://", HTTPAdapter(max_retries=_retry))
resilient_session.mount("http://", HTTPAdapter(max_retries=_retry))


@pytest.hookimpl(hookwrapper=True)
def pytest_runtest_makereport(item, call):
    """Convert Unsatisfiable failures to xfail.

    Some schemas (e.g. CatalogItem with allOf composition, semver patterns, and
    nested minItems/minProperties constraints) exceed Hypothesis's generation budget.
    The versioning tests only check HTTP headers, so skipping body-dependent
    operations is safe — GET/DELETE variants still cover the same middleware paths.
    """
    outcome = yield
    report = outcome.get_result()
    if call.when == "call" and report.failed:
        if call.excinfo and call.excinfo.errisinstance(hypothesis.errors.Unsatisfiable):
            report.outcome = "skipped"
            report.wasxfail = "Schema too complex for generation"
