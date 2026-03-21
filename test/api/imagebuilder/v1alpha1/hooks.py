"""Schemathesis hooks entry point for imagebuilder/v1alpha1."""
import imagebuilder.hooks  # noqa: F401
from common.hooks import register_version

register_version("v1alpha1")
