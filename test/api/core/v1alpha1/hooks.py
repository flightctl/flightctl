"""Schemathesis hooks entry point for core/v1alpha1."""
import core.hooks  # noqa: F401
from common.hooks import register_version

register_version("v1alpha1")
