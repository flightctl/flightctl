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
        ("/devices", "/devices/{name}/status", "Device"),
        ("/fleets", "/fleets/{name}/status", "Fleet"),
        ("/enrollmentrequests", "/enrollmentrequests/{name}/status", "EnrollmentRequest"),
        ("/catalogs/{catalog}/items", "/catalogs/{catalog}/items/{name}", "CatalogItem"),
        ("/catalogs", "/catalogs/{name}", "Catalog"),
    ],
)
def test_kind_for_path_matches_resource_object_routes(monkeypatch, prefix, path, expected_kind):
    monkeypatch.setattr(hooks, "_PATH_TO_KIND", {prefix: expected_kind or "Resource"})

    assert hooks._kind_for_path(path) == expected_kind


def test_action_subresource_body_is_unchanged(monkeypatch):
    monkeypatch.setattr(hooks, "_PATH_TO_KIND", {"/devices": "Device", "/fleets": "Fleet"})
    body = {"target": "Unenroll"}
    original_body = body.copy()
    context = SimpleNamespace(operation=SimpleNamespace(path="/devices/{name}/decommission", method="PUT"))
    case = SimpleNamespace(body=body, path_parameters={"name": "resource-name"})

    hooks.mutate_body(context, body)
    hooks.sync_put_name(context, case)

    assert body == original_body


def test_status_subresource_syncs_resource_identity(monkeypatch):
    monkeypatch.setattr(hooks, "_PATH_TO_KIND", {"/enrollmentrequests": "EnrollmentRequest"})
    monkeypatch.setattr(hooks, "VERSION", "v1beta1")
    body = {"apiVersion": "", "kind": "", "metadata": {}, "spec": {}, "status": {"conditions": []}}
    context = SimpleNamespace(
        operation=SimpleNamespace(path="/enrollmentrequests/{name}/status", method="PUT")
    )
    case = SimpleNamespace(body=body, path_parameters={"name": "resource-name"})

    hooks.mutate_body(context, body)
    hooks.sync_put_name(context, case)

    assert body["apiVersion"] == "flightctl.io/v1beta1"
    assert body["kind"] == "EnrollmentRequest"
    assert body["metadata"]["name"] == "resource-name"
