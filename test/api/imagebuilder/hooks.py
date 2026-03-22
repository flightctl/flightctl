"""ImageBuilder API service hooks."""
import os
import re

import common  # noqa: F401
from common.hooks import register_body_mutator, register_path_kinds, TOKEN, CORE_URL

import requests as http_requests

register_path_kinds({
    "/api/v1/imagebuilds": "ImageBuild",
    "/api/v1/imageexports": "ImageExport",
})


# ---------------------------------------------------------------------------
# Fixture creation for parent resources
# ---------------------------------------------------------------------------
_FIXTURES = {}

_VALID_EXPORT_FORMATS = ("vmdk", "qcow2", "iso", "qcow2-disk-container")


def _create_resource(url, headers, body, name):
    try:
        resp = http_requests.post(
            url, headers=headers, json=body, verify=False, timeout=30,
        )
        if resp.status_code in (201, 409):
            return
        raise RuntimeError(
            f"Fixture '{name}' creation failed: HTTP {resp.status_code}"
        )
    except RuntimeError:
        raise
    except Exception as exc:
        raise RuntimeError(
            f"Failed to create fixture '{name}': {exc}"
        ) from exc


def _setup_fixtures():
    if not CORE_URL or not TOKEN:
        return
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {TOKEN}",
        "Flightctl-API-Version": "v1beta1",
    }
    repo_url = f"{CORE_URL}/api/v1/repositories"
    for name in ("source-repo", "dest-repo"):
        _create_resource(repo_url, headers, {
            "apiVersion": "flightctl.io/v1beta1",
            "kind": "Repository",
            "metadata": {"name": name},
            "spec": {"type": "oci", "registry": "quay.io", "accessMode": "ReadWrite"},
        }, name)
    _FIXTURES["source_repo"] = "source-repo"
    _FIXTURES["dest_repo"] = "dest-repo"

    # Create fixture ImageBuild for ImageExport tests
    ib_base_url = os.environ.get("BASE_URL", "")
    if ib_base_url:
        ib_headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {TOKEN}",
            "Flightctl-API-Version": "v1alpha1",
        }
        build_name = "st-fixture-build"
        _create_resource(f"{ib_base_url}/api/v1/imagebuilds", ib_headers, {
            "apiVersion": "flightctl.io/v1alpha1",
            "kind": "ImageBuild",
            "metadata": {"name": build_name},
            "spec": {
                "source": {
                    "repository": "source-repo",
                    "imageName": "test/base",
                    "imageTag": "latest",
                },
                "destination": {
                    "repository": "dest-repo",
                    "imageName": "test/output",
                    "imageTag": "v1.0.0",
                },
                "binding": {"type": "late"},
            },
        }, build_name)
        _FIXTURES["image_build"] = build_name


_setup_fixtures()


# ===========================================================================
# Body mutators — registered with the single map_body hook in common/hooks.py
# ===========================================================================

@register_body_mutator(path=r'/imagebuilds')
def _fix_imagebuild_refs(body, **_):
    spec = body.get("spec")
    if not isinstance(spec, dict):
        return
    source = spec.get("source")
    if isinstance(source, dict):
        if "repository" in source and _FIXTURES.get("source_repo"):
            source["repository"] = _FIXTURES["source_repo"]
        name = source.get("imageName")
        if not name or not re.match(r'^[a-z0-9][a-z0-9._/:-]*$', str(name)):
            source["imageName"] = "test/base"
    dest = spec.get("destination")
    if isinstance(dest, dict):
        if "repository" in dest and _FIXTURES.get("dest_repo"):
            dest["repository"] = _FIXTURES["dest_repo"]
        name = dest.get("imageName")
        if not name or not re.match(r'^[a-z0-9][a-z0-9._/:-]*$', str(name)):
            dest["imageName"] = "test/output"


@register_body_mutator(path=r'/imageexports')
def _fix_imageexport_refs(body, **_):
    spec = body.get("spec")
    if not isinstance(spec, dict):
        return
    source = spec.get("source")
    if isinstance(source, dict):
        source["type"] = "imageBuild"
        ref = source.get("imageBuildRef", "")
        if not ref.startswith("st-") and _FIXTURES.get("image_build"):
            source["imageBuildRef"] = _FIXTURES["image_build"]
    fmt = spec.get("format")
    if fmt not in _VALID_EXPORT_FORMATS:
        spec["format"] = "qcow2"
