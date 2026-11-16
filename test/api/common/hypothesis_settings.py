"""Preserve suite health-check configuration when CI profiles are active."""

import functools

from hypothesis import settings


def install():
    # Compatibility with the pinned Schemathesis 4.28.0 builder: it replaces
    # configured suppressions with the active profile's entire collection.
    from schemathesis.generation.hypothesis import builder

    if getattr(builder.create_test, "_flightctl_health_checks", False):
        return
    original = builder.create_test

    @functools.wraps(original)
    def create(*args, **kwargs):
        test = original(*args, **kwargs)
        current = getattr(test, builder.SETTINGS_ATTRIBUTE_NAME)
        configured = kwargs["config"].project.suppress_health_check
        checks = list(current.suppress_health_check)
        for check in configured:
            for value in check.as_hypothesis():
                if value not in checks:
                    checks.append(value)
        # Use the same settings attribute as the pinned builder. Preserve every
        # other setting, including explicit decorators and the CI profile.
        setattr(test, builder.SETTINGS_ATTRIBUTE_NAME, settings(current, suppress_health_check=checks))
        return test

    create._flightctl_health_checks = True
    builder.create_test = create
