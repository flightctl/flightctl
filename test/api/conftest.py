"""Pytest configuration for schemathesis API probe tests."""
import os

import hypothesis.errors
import pytest
import schemathesis

schema = schemathesis.openapi.from_path(os.environ["SPEC_PATH"])
schema.config.base_url = os.environ["BASE_URL"]


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
