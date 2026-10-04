"""Unit tests for shared Schemathesis API hooks."""

import json
from copy import deepcopy
from types import SimpleNamespace

import pytest
import requests
import schemathesis
from schemathesis.core.transport import Response
from schemathesis.generation.stateful.state_machine import StepOutput
from schemathesis.hooks import GLOBAL_HOOK_DISPATCHER, HookContext, dispatch_before_call
from schemathesis.specs.openapi import expressions

from common import hooks
from core import hooks as core_hooks  # noqa: F401 -- registers CatalogItem hooks


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

    case.body = hooks.mutate_body(context, body)
    hooks.sync_put_name(context, case)

    assert body == original_body
    assert case.body == original_body


def test_status_subresource_syncs_resource_identity(monkeypatch):
    monkeypatch.setattr(hooks, "_PATH_TO_KIND", {"/enrollmentrequests": "EnrollmentRequest"})
    monkeypatch.setattr(hooks, "VERSION", "v1beta1")
    body = {"apiVersion": "", "kind": "", "metadata": {}, "spec": {}, "status": {"conditions": []}}
    context = SimpleNamespace(
        operation=SimpleNamespace(path="/enrollmentrequests/{name}/status", method="PUT")
    )
    case = SimpleNamespace(body=body, path_parameters={"name": "resource-name"})

    case.body = hooks.mutate_body(context, body)
    hooks.sync_put_name(context, case)

    assert case.body["apiVersion"] == "flightctl.io/v1beta1"
    assert case.body["kind"] == "EnrollmentRequest"
    assert case.body["metadata"]["name"] == "resource-name"


@pytest.fixture
def catalog_output():
    response_definition = {"responses": {"201": {"description": "Created"}}}
    schema = schemathesis.openapi.from_dict({
        "openapi": "3.0.3",
        "info": {"title": "Catalog links", "version": "1"},
        "paths": {
            "/catalogs": {"post": response_definition},
            "/catalogs/{catalog}/items": {"post": response_definition},
            "/catalogs/{catalog}/items/{name}": {"put": response_definition},
        },
    })
    body = {
        "apiVersion": "flightctl.io/v1alpha1",
        "kind": "Catalog",
        "metadata": {"name": "created-catalog", "resourceVersion": "1"},
        "spec": {"displayName": "Catalog"},
    }
    response = Response(
        status_code=201,
        headers={"content-type": ["application/json"]},
        content=json.dumps(body).encode(),
        request=requests.Request("POST", "http://localhost/catalogs").prepare(),
        elapsed=0,
        verify=True,
    )
    return StepOutput(response=response, case=schema["/catalogs"]["POST"].Case())


def linked_catalog_body(output):
    return expressions.evaluate(
        {field: f"$response.body#/{field}" for field in ("apiVersion", "kind", "metadata", "spec")},
        output,
        evaluate_nested=True,
    )


@pytest.mark.parametrize("version", ["v1alpha1", "v1beta1"])
def test_map_body_preserves_linked_response(monkeypatch, catalog_output, version):
    monkeypatch.setattr(hooks, "VERSION", version)
    operation = catalog_output.case.operation.schema["/catalogs/{catalog}/items"]["POST"]
    original = deepcopy(catalog_output.response.json())

    body = hooks.mutate_body(HookContext(operation=operation), linked_catalog_body(catalog_output))

    assert body["kind"] == "CatalogItem"
    assert body["apiVersion"] == f"flightctl.io/{version}"
    assert "resourceVersion" not in body["metadata"]
    assert body["spec"]["type"] == "container"
    assert catalog_output.response.json() == original


@pytest.mark.parametrize("version", ["v1alpha1", "v1beta1"])
@pytest.mark.parametrize("method", ["POST", "PUT"])
def test_before_call_preserves_catalog_status_link(monkeypatch, catalog_output, version, method):
    monkeypatch.setattr(hooks, "VERSION", version)
    path = "/catalogs/{catalog}/items" if method == "POST" else "/catalogs/{catalog}/items/{name}"
    operation = catalog_output.case.operation.schema[path][method]
    context = HookContext(operation=operation)
    original = deepcopy(catalog_output.response.json())
    path_parameters = {"catalog": "item-catalog"}
    if method == "PUT":
        path_parameters["name"] = "item-name"
    case = operation.Case(
        body=hooks.mutate_body(context, {"metadata": {}, "spec": {}}),
        path_parameters=path_parameters,
        media_type="application/json",
    )
    # Stateful links merge response objects after map_body has already run.
    case.body.update(linked_catalog_body(catalog_output))
    dispatch_before_call(GLOBAL_HOOK_DISPATCHER, context=context, case=case, kwargs={})

    assert case.body["metadata"]["catalog"] == "item-catalog"
    if method == "PUT":
        assert case.body["metadata"]["name"] == "item-name"
    else:
        assert case.body["metadata"]["name"] == "created-catalog"
        assert case.body["kind"] == "CatalogItem"
        assert case.body["spec"]["type"] == "container"
    assert expressions.evaluate("$response.body#/metadata/name", catalog_output) == "created-catalog"
    assert catalog_output.response.json() == original
