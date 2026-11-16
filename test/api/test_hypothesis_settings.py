"""Exercise suite health checks through actual Schemathesis test construction."""

import pytest
import schemathesis
from hypothesis import HealthCheck, settings
from schemathesis.config import HealthCheck as ConfigHealthCheck
from schemathesis.generation.hypothesis import builder

from common.hypothesis_settings import install


@pytest.mark.parametrize("configured", [[], [ConfigHealthCheck.filter_too_much], [ConfigHealthCheck.all]])
def test_ci_profile_preserves_configured_health_checks(configured):
    previous = settings.get_current_profile_name()
    settings.load_profile("ci")
    try:
        schema = schemathesis.openapi.from_dict({
            "openapi": "3.0.3", "info": {"title": "test", "version": "1"},
            "paths": {"/fleets": {"post": {"responses": {"200": {"description": "ok"}}}}},
        })
        schema.config.suppress_health_check = configured
        schema.config.generation.max_examples = 7
        install()
        test = builder.create_test(
            operation=schema["/fleets"]["POST"],
            test_func=lambda case: None,
            config=builder.HypothesisTestConfig(
                project=schema.config,
                modes=[builder.HypothesisTestMode.FUZZING],
                settings=schema.config.get_hypothesis_settings(),
            ),
        )
        effective = getattr(test, builder.SETTINGS_ATTRIBUTE_NAME)
        expected = {HealthCheck.too_slow}
        for check in configured:
            expected.update(check.as_hypothesis())
        assert set(effective.suppress_health_check) == expected
        assert len(effective.suppress_health_check) == len(expected)
        assert effective.max_examples == 7
        assert effective.derandomize is True
        assert effective.deadline is None
    finally:
        settings.load_profile(previous)
