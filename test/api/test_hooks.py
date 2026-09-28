"""Unit tests for shared Schemathesis API hooks."""

from types import SimpleNamespace

import pytest

from common import hooks


@pytest.mark.parametrize(
    ("prefix", "path", "expected_kind"),
    [
        ("/devices", "/devices", "Device"),
        ("/devices", "/devices/{name}", "Device"),
        ("/devices", "/devices/{name}/decommission", None),
        ("/devices", "/devices/{name}/status", None),
        ("/fleets", "/fleets/{name}/status", None),
        ("/catalogs/{catalog}/items", "/catalogs/{catalog}/items/{name}", "CatalogItem"),
        ("/catalogs", "/catalogs/{name}", "Catalog"),
    ],
)
def test_kind_for_path_matches_resource_object_routes(monkeypatch, prefix, path, expected_kind):
    monkeypatch.setattr(hooks, "_PATH_TO_KIND", {prefix: expected_kind or "Resource"})

    assert hooks._kind_for_path(path) == expected_kind


@pytest.mark.parametrize(
    ("path", "body"),
    [
        ("/devices/{name}/decommission", {"target": "Unenroll"}),
        ("/fleets/{name}/status", {"conditions": []}),
    ],
)
def test_resource_hooks_leave_subresource_bodies_unchanged(monkeypatch, path, body):
    monkeypatch.setattr(hooks, "_PATH_TO_KIND", {"/devices": "Device", "/fleets": "Fleet"})
    original_body = body.copy()
    context = SimpleNamespace(operation=SimpleNamespace(path=path, method="PUT"))
    case = SimpleNamespace(body=body, path_parameters={"name": "resource-name"})

    hooks.mutate_body(context, body)
    hooks.sync_put_name(context, case)

    assert body == original_body
