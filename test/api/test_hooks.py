"""Unit tests for shared Schemathesis API hooks."""

import json
import os
import re
from copy import deepcopy
from types import SimpleNamespace
from urllib.parse import unquote, urlsplit

import pytest
import requests
import schemathesis
from hypothesis import given, settings, strategies as st
from schemathesis.core.transport import Response
from schemathesis.generation import GenerationMode
from schemathesis.generation.stateful.state_machine import StepOutput
from schemathesis.hooks import GLOBAL_HOOK_DISPATCHER, HookContext, dispatch_before_call
from schemathesis.specs.openapi import expressions
from schemathesis.specs.openapi.stateful import collect_transitions, into_step_input
from urllib3._collections import HTTPHeaderDict

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
    resource_body = {"content": {"application/json": {"schema": {
        "type": "object",
        "properties": {
            "apiVersion": {"type": "string"},
            "kind": {"type": "string"},
            "metadata": {"type": "object"},
            "spec": {"type": "object"},
        },
    }}}}
    def name_parameter(name):
        return {"name": name, "in": "path", "required": True, "schema": {"type": "string"}}
    schema = schemathesis.openapi.from_dict({
        "openapi": "3.0.3",
        "info": {"title": "Catalog links", "version": "1"},
        "paths": {
            "/catalogs": {"post": {"responses": {"201": {
                "description": "Created",
                "links": {
                    "ReplaceItem": {
                        "operationId": "replaceCatalogItem",
                        "parameters": {"catalog": "$response.body#/metadata/name", "name": "linked-item"},
                        "requestBody": {field: f"$response.body#/{field}"
                                        for field in ("apiVersion", "kind", "metadata", "spec")},
                    },
                    "GetStatus": {
                        "operationId": "getCatalogStatus",
                        "parameters": {"name": "$response.body#/metadata/name"},
                    },
                },
            }}}},
            "/catalogs/{catalog}/items": {"post": response_definition},
            "/catalogs/{catalog}/items/{name}": {"put": {
                "operationId": "replaceCatalogItem",
                "parameters": [name_parameter("catalog"), name_parameter("name")],
                "requestBody": resource_body,
                "responses": {"200": {"description": "Replaced"}},
            }},
            "/catalogs/{name}/status": {"get": {
                "operationId": "getCatalogStatus",
                "parameters": [name_parameter("name")],
                "responses": {"200": {"description": "Status"}},
            }},
        },
    })
    body = {
        "apiVersion": "flightctl.io/v1alpha1",
        "kind": "Catalog",
        "metadata": {"name": "created-catalog", "resourceVersion": "1"},
        "spec": {
            "displayName": "Catalog",
            "type": "container",
            "artifacts": [{"type": "container", "uri": "invalid"}],
            "versions": [{
                "version": "1.0.0",
                "tag": "legacy-tag",
                "digest": "legacy-digest",
                "config": {"envVars": {"MODE": "test"}, "namespace": "helm-only"},
                "deprecation": {},
            }],
        },
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
    assert body["spec"]["artifacts"][0]["uri"] == "quay.io/schemathesis/test"
    version_body = body["spec"]["versions"][0]
    assert version_body["references"] == {"container": "v1.0.0"}
    assert version_body["channels"] == ["stable"]
    assert "tag" not in version_body
    assert "digest" not in version_body
    assert version_body["config"] == {"envVars": {"MODE": "test"}}
    assert version_body["deprecation"]["message"] == "Deprecated by schemathesis test"
    assert catalog_output.response.json() == original


@pytest.mark.parametrize("version", ["v1alpha1", "v1beta1"])
def test_stateful_catalog_item_and_status_links_preserve_response(monkeypatch, catalog_output, version):
    monkeypatch.setattr(hooks, "VERSION", version)
    schema = catalog_output.case.operation.schema
    schema.config.base_url = "http://localhost"
    operations = [
        catalog_output.case.operation,
        schema["/catalogs/{catalog}/items/{name}"]["PUT"],
        schema["/catalogs/{name}/status"]["GET"],
    ]
    links = {link.name: link for link in collect_transitions(operations).operations["POST /catalogs"].outgoing}
    original = deepcopy(catalog_output.response.json())
    requested_paths = []

    def send(_session, request, **_kwargs):
        path = urlsplit(request.url).path
        requested_paths.append(path)
        if request.method == "PUT":
            body = json.loads(request.body)
            assert body["metadata"]["name"] == "linked-item"
            assert body["metadata"]["catalog"] == "created-catalog"
        else:
            assert request.method == "GET"
            assert path == "/catalogs/created-catalog/status"
        response = requests.Response()
        response.status_code = 200
        response.request = request
        response._content = b"{}"
        response.headers["Content-Type"] = "application/json"
        response.raw = SimpleNamespace(headers=HTTPHeaderDict(response.headers), version=11)
        return response

    monkeypatch.setattr(requests.Session, "send", send)
    # Always select link values so the regression deterministically exercises both transitions.
    calibration = SimpleNamespace(use_probability=lambda _: 1.0, is_calibrated=lambda _: True)

    @settings(max_examples=1, deadline=None, derandomize=True)
    @given(st.data())
    def follow_links(data):
        for name in ("ReplaceItem", "GetStatus"):
            link = links[name]
            transition = data.draw(into_step_input(
                target=link.target, link=link, modes=[GenerationMode.POSITIVE],
                link_calibration=calibration,
            )(catalog_output))
            assert transition.case.call().status_code == 200
        assert catalog_output.response.json() == original

    follow_links()
    assert requested_paths == [
        "/catalogs/created-catalog/items/linked-item",
        "/catalogs/created-catalog/status",
    ]


@pytest.mark.parametrize("version", ["v1alpha1", "v1beta1"])
@pytest.mark.parametrize("method", ["POST", "PUT"])
def test_before_call_preserves_linked_catalog_response_identity(monkeypatch, catalog_output, version, method):
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


# ===========================================================================
# Regression tests for before_load_schema (public hook, no x-bundled dependency)
#
# Tests use direct raw-hook calls (hooks.before_load_schema(None, spec))
# so that assertions target the raw schema mutations, not Schemathesis's
# private bundling layout.  A few end-to-end tests load with from_dict to
# verify public loader behaviour.
# ===========================================================================

def _make_raw_spec(schemas=None, *, paths=None, extra_components=None):
    """Build a minimal raw OpenAPI document for direct hook calls."""
    spec = {
        "openapi": "3.0.3",
        "info": {"title": "Hook test", "version": "1"},
        "paths": paths or {
            "/resources": {
                "post": {
                    "requestBody": {
                        "content": {"application/json": {
                            "schema": {"$ref": "#/components/schemas/Resource"}
                        }},
                    },
                    "responses": {"201": {"description": "Created"}},
                },
            },
        },
    }
    if schemas is not None:
        spec.setdefault("components", {})["schemas"] = schemas
    if extra_components:
        spec.setdefault("components", {}).update(extra_components)
    return spec


class TestBeforeLoadSchemaIntegerLimits:
    """Integer limits applied via raw before_load_schema hook."""

    def test_int64_limits_on_component_schema(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"count": {"type": "integer"}},
        }})
        hooks.before_load_schema(None, raw)
        count = raw["components"]["schemas"]["Resource"]["properties"]["count"]
        assert count["minimum"] == hooks.INT64_MIN
        assert count["maximum"] == hooks.INT64_MAX

    def test_int32_limits(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"port": {"type": "integer", "format": "int32"}},
        }})
        hooks.before_load_schema(None, raw)
        port = raw["components"]["schemas"]["Resource"]["properties"]["port"]
        assert port["minimum"] == hooks.INT32_MIN
        assert port["maximum"] == hooks.INT32_MAX

    def test_existing_limits_not_overwritten(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"age": {"type": "integer", "minimum": 0, "maximum": 200}},
        }})
        hooks.before_load_schema(None, raw)
        age = raw["components"]["schemas"]["Resource"]["properties"]["age"]
        assert age["minimum"] == 0
        assert age["maximum"] == 200

    def test_limits_on_referenced_component(self):
        """Integer limits on a transitively referenced component."""
        raw = _make_raw_spec({
            "Resource": {"type": "object", "properties": {
                "nested": {"$ref": "#/components/schemas/Nested"},
            }},
            "Nested": {"type": "object", "properties": {
                "value": {"type": "integer"},
            }},
        })
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["Nested"]["properties"]["value"]["minimum"] == hooks.INT64_MIN


class TestBeforeLoadSchemaLabelConstraints:
    """Label key/value constraints via raw hook."""

    def test_label_pattern_added(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"labels": {
                "type": "object", "additionalProperties": {"type": "string"},
            }},
        }})
        hooks.before_load_schema(None, raw)
        labels = raw["components"]["schemas"]["Resource"]["properties"]["labels"]
        assert labels["propertyNames"]["pattern"] == hooks._LABEL_KEY_PATTERN
        assert labels["additionalProperties"]["pattern"] == hooks._LABEL_VALUE_PATTERN

    def test_label_on_referenced_component(self):
        raw = _make_raw_spec({
            "Resource": {"type": "object", "properties": {
                "metadata": {"$ref": "#/components/schemas/ObjectMeta"},
            }},
            "ObjectMeta": {"type": "object", "properties": {
                "labels": {"type": "object", "additionalProperties": {"type": "string"}},
            }},
        })
        hooks.before_load_schema(None, raw)
        labels = raw["components"]["schemas"]["ObjectMeta"]["properties"]["labels"]
        assert "propertyNames" in labels


class TestBeforeLoadSchemaNamePattern:
    """RFC 1123 name pattern via raw hook."""

    def test_name_pattern_on_maxlength_253(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"name": {"type": "string", "maxLength": 253}},
        }})
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["Resource"]["properties"]["name"]["pattern"] == hooks._RFC1123_RE.pattern

    def test_name_pattern_not_added_without_maxlength(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"name": {"type": "string"}},
        }})
        hooks.before_load_schema(None, raw)
        assert "pattern" not in raw["components"]["schemas"]["Resource"]["properties"]["name"]


class TestBeforeLoadSchemaDisallowExtraProperties:
    """additionalProperties: false and composition exemptions via raw hook."""

    def test_adds_additional_properties_false(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object", "properties": {"field": {"type": "string"}},
        }})
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["Resource"].get("additionalProperties") is False

    def test_skips_schema_with_existing_additional_properties(self):
        raw = _make_raw_spec({"Resource": {
            "type": "object", "properties": {"field": {"type": "string"}},
            "additionalProperties": True,
        }})
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["Resource"]["additionalProperties"] is True

    def test_composition_target_exempt(self):
        """Schemas referenced via allOf $ref are exempt."""
        raw = _make_raw_spec({
            "Resource": {"allOf": [
                {"$ref": "#/components/schemas/Base"},
                {"type": "object", "properties": {"extra": {"type": "string"}}},
            ]},
            "Base": {"type": "object", "properties": {"core": {"type": "string"}}},
        })
        hooks.before_load_schema(None, raw)
        assert "additionalProperties" not in raw["components"]["schemas"]["Base"]

    def test_nested_property_composition_ref_detected(self):
        """A $ref inside a nested property's allOf is detected as composition target."""
        raw = _make_raw_spec({
            "Resource": {"type": "object", "properties": {
                "spec": {"type": "object", "properties": {
                    "config": {"allOf": [
                        {"$ref": "#/components/schemas/ConfigBase"},
                        {"type": "object", "properties": {"extra": {"type": "string"}}},
                    ]},
                }},
            }},
            "ConfigBase": {"type": "object", "properties": {"setting": {"type": "string"}}},
        })
        hooks.before_load_schema(None, raw)
        assert "additionalProperties" not in raw["components"]["schemas"]["ConfigBase"]

    def test_non_composition_schema_constrained(self):
        raw = _make_raw_spec({
            "Resource": {"type": "object", "properties": {
                "meta": {"$ref": "#/components/schemas/Meta"},
            }},
            "Meta": {"type": "object", "properties": {"name": {"type": "string"}}},
        })
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["Meta"].get("additionalProperties") is False


class TestBeforeLoadSchemaNoXBundledDependency:
    """Production hooks do not reference x-bundled internals."""

    def test_walk_schema_ignores_x_bundled(self):
        visited = []
        schema = {
            "type": "object", "properties": {"a": {"type": "string"}},
            "x-bundled": {"orphan": {"type": "object", "properties": {"b": {"type": "integer"}}}},
        }
        hooks._walk_schema(schema, lambda s: visited.append(s.get("type")))
        types = [t for t in visited if t]
        assert "integer" not in types, "_walk_schema must not descend into x-bundled"
        assert "object" in types
        assert "string" in types

    def test_disallow_extra_ignores_x_bundled(self):
        schema = {
            "type": "object", "properties": {"a": {"type": "string"}},
            "x-bundled": {"inner": {"type": "object", "properties": {"b": {"type": "string"}}}},
        }
        hooks.disallow_extra_properties(schema)
        assert schema.get("additionalProperties") is False
        assert "additionalProperties" not in schema["x-bundled"]["inner"]


class TestInlineRequestBodyConstraints:
    """(A) Inline requestBody schemas receive all four constraints."""

    def test_inline_body_integer_limits(self):
        """Inline requestBody schema gets integer limits even without components."""
        raw = _make_raw_spec(paths={
            "/items": {"post": {
                "requestBody": {"content": {"application/json": {"schema": {
                    "type": "object",
                    "properties": {"count": {"type": "integer"}},
                }}}},
                "responses": {"201": {"description": "Created"}},
            }},
        })
        hooks.before_load_schema(None, raw)
        body_schema = raw["paths"]["/items"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        assert body_schema["properties"]["count"]["minimum"] == hooks.INT64_MIN

    def test_inline_body_label_constraints(self):
        raw = _make_raw_spec(paths={
            "/items": {"post": {
                "requestBody": {"content": {"application/json": {"schema": {
                    "type": "object",
                    "properties": {"labels": {
                        "type": "object", "additionalProperties": {"type": "string"},
                    }},
                }}}},
                "responses": {"201": {"description": "Created"}},
            }},
        })
        hooks.before_load_schema(None, raw)
        labels = raw["paths"]["/items"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["labels"]
        assert "propertyNames" in labels

    def test_inline_body_name_pattern(self):
        raw = _make_raw_spec(paths={
            "/items": {"post": {
                "requestBody": {"content": {"application/json": {"schema": {
                    "type": "object",
                    "properties": {"name": {"type": "string", "maxLength": 253}},
                }}}},
                "responses": {"201": {"description": "Created"}},
            }},
        })
        hooks.before_load_schema(None, raw)
        name = raw["paths"]["/items"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["name"]
        assert name["pattern"] == hooks._RFC1123_RE.pattern

    def test_inline_body_additional_properties(self):
        raw = _make_raw_spec(paths={
            "/items": {"post": {
                "requestBody": {"content": {"application/json": {"schema": {
                    "type": "object",
                    "properties": {"field": {"type": "string"}},
                }}}},
                "responses": {"201": {"description": "Created"}},
            }},
        })
        hooks.before_load_schema(None, raw)
        body = raw["paths"]["/items"]["post"]["requestBody"]["content"]["application/json"]["schema"]
        assert body.get("additionalProperties") is False

    def test_no_components_at_all(self):
        """Spec with no components section — inline body still gets constraints."""
        raw = {
            "openapi": "3.0.3", "info": {"title": "T", "version": "1"},
            "paths": {"/x": {"post": {
                "requestBody": {"content": {"application/json": {"schema": {
                    "type": "object",
                    "properties": {"n": {"type": "integer"}},
                }}}},
                "responses": {"201": {"description": "OK"}},
            }}},
        }
        hooks.before_load_schema(None, raw)
        assert raw["paths"]["/x"]["post"]["requestBody"]["content"]["application/json"]["schema"]["properties"]["n"]["minimum"] == hooks.INT64_MIN

    def test_inline_body_with_allof_composition_exempts_ref(self):
        """(C) Inline body allOf referencing a component exempts that component."""
        raw = _make_raw_spec(
            schemas={
                "Base": {"type": "object", "properties": {"core": {"type": "string"}}},
            },
            paths={"/items": {"post": {
                "requestBody": {"content": {"application/json": {"schema": {
                    "allOf": [
                        {"$ref": "#/components/schemas/Base"},
                        {"type": "object", "properties": {"extra": {"type": "string"}}},
                    ],
                }}}},
                "responses": {"201": {"description": "Created"}},
            }}},
        )
        hooks.before_load_schema(None, raw)
        assert "additionalProperties" not in raw["components"]["schemas"]["Base"], \
            "Component referenced from inline allOf must be exempt"


class TestCollectSchemaRefsRobustness:
    """(B) _collect_schema_refs and _request_body_schemas edge cases."""

    def test_example_data_not_traversed(self):
        """$ref-like strings inside example/default are not collected."""
        raw = _make_raw_spec({
            "Resource": {
                "type": "object",
                "properties": {"field": {"type": "string"}},
                "example": {"$ref": "#/components/schemas/Bogus"},
                "default": {"nested": {"$ref": "#/components/schemas/AlsoBogus"}},
            },
        })
        _, names = hooks._request_body_schemas(raw)
        assert "Bogus" not in names
        assert "AlsoBogus" not in names
        assert "Resource" in names

    def test_extension_data_not_traversed(self):
        """Vendor extensions (x-*) are not entered by _walk_schema."""
        raw = _make_raw_spec({
            "Resource": {
                "type": "object",
                "properties": {"field": {"type": "string"}},
                "x-custom": {"$ref": "#/components/schemas/Ghost"},
            },
        })
        refs = hooks._collect_schema_refs(raw["components"]["schemas"]["Resource"])
        assert "Ghost" not in refs

    def test_absent_ref_target_does_not_hang(self):
        """Reference to a non-existent component must not cause an infinite loop."""
        raw = _make_raw_spec({
            "Resource": {
                "type": "object",
                "properties": {"child": {"$ref": "#/components/schemas/Missing"}},
            },
        })
        # Must complete without hanging.
        _, names = hooks._request_body_schemas(raw)
        assert "Resource" in names
        assert "Missing" in names  # discovered but absent — still terminates

    def test_cyclic_refs_terminate(self):
        """Mutually referencing schemas must terminate."""
        raw = _make_raw_spec({
            "Resource": {"type": "object", "properties": {
                "a": {"$ref": "#/components/schemas/A"},
            }},
            "A": {"type": "object", "properties": {
                "b": {"$ref": "#/components/schemas/B"},
            }},
            "B": {"type": "object", "properties": {
                "a": {"$ref": "#/components/schemas/A"},
            }},
        })
        _, names = hooks._request_body_schemas(raw)
        assert {"Resource", "A", "B"} <= names


class TestParameterLevelConstraints:
    """Parameter-level patching in before_init_operation (no x-bundled)."""

    def test_inline_parameter_integer_limits(self):
        spec = {
            "openapi": "3.0.3", "info": {"title": "T", "version": "1"},
            "paths": {"/items": {"get": {
                "parameters": [{"name": "limit", "in": "query", "schema": {"type": "integer"}}],
                "responses": {"200": {"description": "OK"}},
            }}},
        }
        schema = schemathesis.openapi.from_dict(spec)
        op = schema["/items"]["GET"]
        for param in op.iter_parameters():
            if param.definition.get("name") == "limit":
                assert param.definition["schema"]["minimum"] == hooks.INT64_MIN
                return
        pytest.fail("limit parameter not found")


class TestResponseScopePreservation:
    """disallow_extra_properties scoped to request schemas only."""

    def test_response_only_schema_not_constrained(self):
        raw = _make_raw_spec(
            schemas={
                "CreateReq": {"type": "object", "properties": {"name": {"type": "string"}}},
                "ListResp": {"type": "object", "properties": {"items": {"type": "array"}}},
            },
            paths={"/items": {"post": {
                "requestBody": {"content": {"application/json": {
                    "schema": {"$ref": "#/components/schemas/CreateReq"},
                }}},
                "responses": {"200": {"description": "OK", "content": {"application/json": {
                    "schema": {"$ref": "#/components/schemas/ListResp"},
                }}}},
            }}},
        )
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["CreateReq"].get("additionalProperties") is False
        assert "additionalProperties" not in raw["components"]["schemas"]["ListResp"]

    def test_response_only_still_gets_integer_limits(self):
        raw = _make_raw_spec(
            schemas={"RespOnly": {"type": "object", "properties": {"count": {"type": "integer"}}}},
            paths={"/items": {"get": {
                "responses": {"200": {"description": "OK", "content": {"application/json": {
                    "schema": {"$ref": "#/components/schemas/RespOnly"},
                }}}},
            }}},
        )
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["RespOnly"]["properties"]["count"]["minimum"] == hooks.INT64_MIN


class TestReusableRequestBodyScoping:
    """Reusable requestBodies only affect schemas when used by an operation."""

    def _spec_with_reusable_body(self, *, operation_uses_body):
        """Build a spec with a reusable requestBody referencing RespOnly."""
        paths = {"/items": {"get": {
            "responses": {"200": {"description": "OK", "content": {"application/json": {
                "schema": {"$ref": "#/components/schemas/RespOnly"},
            }}}},
        }}}
        if operation_uses_body:
            paths["/items"]["post"] = {
                "requestBody": {"$ref": "#/components/requestBodies/CreateBody"},
                "responses": {"201": {"description": "Created"}},
            }
        return _make_raw_spec(
            schemas={
                "RespOnly": {"type": "object", "properties": {"field": {"type": "string"}}},
            },
            paths=paths,
            extra_components={
                "requestBodies": {"CreateBody": {"content": {"application/json": {
                    "schema": {"$ref": "#/components/schemas/RespOnly"},
                }}}},
            },
        )

    def test_unused_reusable_body_does_not_constrain_schema(self):
        """Schema referenced only by an unused reusable requestBody must not
        get additionalProperties: false."""
        raw = self._spec_with_reusable_body(operation_uses_body=False)
        hooks.before_load_schema(None, raw)
        assert "additionalProperties" not in raw["components"]["schemas"]["RespOnly"], \
            "Unused reusable requestBody must not taint response-only schema"

    def test_used_reusable_body_constrains_schema(self):
        """Schema referenced by a reusable requestBody that IS used by an
        operation must get additionalProperties: false."""
        raw = self._spec_with_reusable_body(operation_uses_body=True)
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["RespOnly"].get("additionalProperties") is False

    def test_chained_reusable_body_alias(self):
        """Operation $ref -> requestBody $ref -> requestBody with content."""
        raw = _make_raw_spec(
            schemas={
                "Target": {"type": "object", "properties": {"x": {"type": "string"}}},
            },
            paths={"/items": {"post": {
                "requestBody": {"$ref": "#/components/requestBodies/Alias"},
                "responses": {"201": {"description": "Created"}},
            }}},
            extra_components={
                "requestBodies": {
                    "Alias": {"$ref": "#/components/requestBodies/Real"},
                    "Real": {"content": {"application/json": {
                        "schema": {"$ref": "#/components/schemas/Target"},
                    }}},
                },
            },
        )
        hooks.before_load_schema(None, raw)
        assert raw["components"]["schemas"]["Target"].get("additionalProperties") is False

    def test_missing_reusable_body_alias_terminates(self):
        """$ref to a non-existent components/requestBodies entry must not hang."""
        raw = _make_raw_spec(
            schemas={},
            paths={"/items": {"post": {
                "requestBody": {"$ref": "#/components/requestBodies/Missing"},
                "responses": {"201": {"description": "Created"}},
            }}},
        )
        hooks.before_load_schema(None, raw)  # must not hang

    def test_cyclic_reusable_body_aliases_terminate(self):
        """Mutually-referencing requestBody aliases must not hang."""
        raw = _make_raw_spec(
            schemas={},
            paths={"/x": {"post": {
                "requestBody": {"$ref": "#/components/requestBodies/A"},
                "responses": {"201": {"description": "OK"}},
            }}},
            extra_components={
                "requestBodies": {
                    "A": {"$ref": "#/components/requestBodies/B"},
                    "B": {"$ref": "#/components/requestBodies/A"},
                },
            },
        )
        hooks.before_load_schema(None, raw)  # must not hang

    def test_unused_reusable_body_inline_content_untouched(self):
        """Inline content in an unused reusable requestBody must not be patched."""
        raw = _make_raw_spec(
            schemas={},
            paths={"/items": {"get": {
                "responses": {"200": {"description": "OK"}},
            }}},
            extra_components={
                "requestBodies": {"Orphan": {"content": {"application/json": {
                    "schema": {
                        "type": "object",
                        "properties": {"count": {"type": "integer"}},
                    },
                }}}},
            },
        )
        hooks.before_load_schema(None, raw)
        orphan_schema = raw["components"]["requestBodies"]["Orphan"]["content"]["application/json"]["schema"]
        assert "additionalProperties" not in orphan_schema, \
            "Unused reusable inline body must not get additionalProperties"
        assert "minimum" not in orphan_schema["properties"]["count"], \
            "Unused reusable inline body must not get integer limits"


    def test_reusable_body_inline_nodes_not_duplicated(self):
        """Each inline schema from a reusable body must appear exactly once."""
        raw = _make_raw_spec(
            schemas={},
            paths={"/x": {"post": {
                "requestBody": {"$ref": "#/components/requestBodies/Body"},
                "responses": {"201": {"description": "OK"}},
            }}},
            extra_components={
                "requestBodies": {"Body": {"content": {"application/json": {
                    "schema": {"type": "object", "properties": {"f": {"type": "string"}}},
                }}}},
            },
        )
        inline, _ = hooks._request_body_schemas(raw)
        assert len(inline) == 1, f"Expected 1 inline node, got {len(inline)}"

    def test_used_reusable_body_constraints_via_loader(self):
        """Reusable requestBody referenced by an operation: constraints applied
        through the public from_dict loader (positive end-to-end proof)."""
        spec = _make_raw_spec(
            schemas={
                "Item": {"type": "object", "properties": {
                    "name": {"type": "string", "maxLength": 253},
                    "count": {"type": "integer"},
                }},
            },
            paths={"/items": {"post": {
                "requestBody": {"$ref": "#/components/requestBodies/CreateItem"},
                "responses": {"201": {"description": "Created"}},
            }}},
            extra_components={
                "requestBodies": {"CreateItem": {"content": {"application/json": {
                    "schema": {"$ref": "#/components/schemas/Item"},
                }}}},
            },
        )
        loaded = schemathesis.openapi.from_dict(spec)
        item = spec["components"]["schemas"]["Item"]
        assert item.get("additionalProperties") is False, \
            "Used reusable body schema must get additionalProperties"
        assert item["properties"]["count"]["minimum"] == hooks.INT64_MIN, \
            "Integer limits must be applied"
        assert item["properties"]["name"]["pattern"] == hooks._RFC1123_RE.pattern, \
            "Name pattern must be applied"
        op = loaded["/items"]["POST"]
        assert op.body, "POST /items must have a body definition"


class TestEndToEndLoaderBehaviour:
    """Verify constraints survive through the public Schemathesis loader."""

    def test_component_integer_limits_reach_generated_case(self):
        spec = _make_raw_spec({"Resource": {
            "type": "object",
            "properties": {"count": {"type": "integer"}},
        }})
        loaded = schemathesis.openapi.from_dict(spec)
        op = loaded["/resources"]["POST"]
        for body in op.body:
            s = body.definition.get("schema", {})
            assert s["properties"]["count"]["minimum"] == hooks.INT64_MIN

    def test_composition_target_exempt_through_loader(self):
        """Composition target stays unconstrained through the public from_dict loader.

        With additionalProperties: false incorrectly on Base, the allOf
        becomes unsatisfiable (extra is rejected by Base).  This test
        generates a body via the public as_strategy() API and asserts
        both allOf branches contribute required properties — proving
        the composition exemption survived loading, not just the
        raw-hook callback.
        """
        from hypothesis import given, settings as h_settings, HealthCheck
        from hypothesis import strategies as st

        spec = _make_raw_spec(
            schemas={
                "Resource": {"allOf": [
                    {"$ref": "#/components/schemas/Base"},
                    {"type": "object", "required": ["extra"],
                     "properties": {"extra": {"type": "string"}}},
                ]},
                "Base": {"type": "object", "required": ["core"], "properties": {
                    "core": {"type": "string"},
                    "count": {"type": "integer"},
                }},
            },
            paths={"/resources": {"post": {
                "requestBody": {"required": True, "content": {"application/json": {
                    "schema": {"$ref": "#/components/schemas/Resource"},
                }}},
                "responses": {"201": {"description": "Created"}},
            }}},
        )
        loaded = schemathesis.openapi.from_dict(spec)
        base = spec["components"]["schemas"]["Base"]
        # Positive proof that before_load_schema ran: integer limits present.
        assert base["properties"]["count"]["minimum"] == hooks.INT64_MIN
        # Composition target must NOT have additionalProperties: false.
        assert "additionalProperties" not in base

        # Generate a body through the public loaded-schema strategy.
        # If Base were wrongly constrained, Hypothesis raises Unsatisfiable.
        op = loaded["/resources"]["POST"]

        @h_settings(max_examples=1, derandomize=True,
                     suppress_health_check=list(HealthCheck))
        @given(data=st.data())
        def _check(data):
            case = data.draw(op.as_strategy())
            assert isinstance(case.body, dict), "body must be a dict"
            assert "core" in case.body, "Base's required 'core' must be present"
            assert "extra" in case.body, "Sibling branch's required 'extra' must be present"

        _check()


def _find_spec_path():
    """Locate a real OpenAPI spec in both repo checkout and CI container layouts.

    Tries ``SPEC_PATH`` env var first (set by ``run_suite.sh`` in CI),
    then falls back to the repo-relative path from this file.
    Always returns an absolute path when a spec is found.
    """
    env_path = os.environ.get("SPEC_PATH")
    if env_path:
        abs_env = os.path.abspath(env_path)
        if os.path.isfile(abs_env):
            return abs_env
    repo_path = os.path.abspath(
        os.path.join(os.path.dirname(__file__), "..", "..", "api", "core", "v1beta1", "openapi.yaml"),
    )
    if os.path.isfile(repo_path):
        return repo_path
    return None


class TestFindSpecPath:
    """Verify _find_spec_path resolves specs in repo and container layouts."""

    def test_returns_spec_path_env_when_valid(self, tmp_path, monkeypatch):
        spec = tmp_path / "openapi.yaml"
        spec.write_text("openapi: '3.0.3'")
        monkeypatch.setenv("SPEC_PATH", str(spec))
        result = _find_spec_path()
        assert result == str(spec)

    def test_normalises_relative_env_to_absolute(self, tmp_path, monkeypatch):
        """A relative SPEC_PATH is resolved to an absolute path."""
        spec = tmp_path / "openapi.yaml"
        spec.write_text("openapi: '3.0.3'")
        monkeypatch.chdir(tmp_path)
        monkeypatch.setenv("SPEC_PATH", "openapi.yaml")
        result = _find_spec_path()
        assert os.path.isabs(result)
        assert result == str(spec)

    def test_ignores_missing_spec_path_env(self, tmp_path, monkeypatch):
        monkeypatch.setenv("SPEC_PATH", "/nonexistent/openapi.yaml")
        # Patch __file__ to a dir without repo layout so fallback returns None
        import test_hooks
        monkeypatch.setattr(test_hooks, "__file__", str(tmp_path / "test_hooks.py"))
        assert _find_spec_path() is None

    def test_repo_fallback_finds_spec(self, tmp_path, monkeypatch):
        """With no SPEC_PATH, falls back to repo-relative from __file__."""
        monkeypatch.delenv("SPEC_PATH", raising=False)
        # Build a minimal repo tree: __file__ at test/api/test_hooks.py
        repo = tmp_path / "repo"
        api_dir = repo / "api" / "core" / "v1beta1"
        api_dir.mkdir(parents=True)
        spec = api_dir / "openapi.yaml"
        spec.write_text("openapi: '3.0.3'")
        test_dir = repo / "test" / "api"
        test_dir.mkdir(parents=True)
        import test_hooks
        monkeypatch.setattr(test_hooks, "__file__", str(test_dir / "test_hooks.py"))
        result = _find_spec_path()
        assert result == str(spec)

    def test_container_layout_env_over_missing_repo(self, tmp_path, monkeypatch):
        """Simulated container: specs at app/specs, tests at app/config."""
        specs_dir = tmp_path / "app" / "specs"
        specs_dir.mkdir(parents=True)
        spec = specs_dir / "openapi.yaml"
        spec.write_text("openapi: '3.0.3'")
        config_dir = tmp_path / "app" / "config"
        config_dir.mkdir(parents=True)
        import test_hooks
        monkeypatch.setattr(test_hooks, "__file__", str(config_dir / "test_hooks.py"))
        monkeypatch.setenv("SPEC_PATH", str(spec))
        result = _find_spec_path()
        assert result == str(spec)


class TestBeforeLoadSchemaOnRealSpec:
    """Smoke tests against the actual flightctl OpenAPI spec using raw hook."""

    @pytest.fixture
    def real_raw(self):
        import yaml
        spec_path = _find_spec_path()
        if spec_path is None:
            pytest.skip("openapi.yaml not found")
        with open(spec_path) as f:
            return yaml.safe_load(f)

    def test_component_schemas_have_integer_limits(self, real_raw):
        hooks.before_load_schema(None, real_raw)
        schemas = real_raw.get("components", {}).get("schemas", {})
        # Pick a schema that has integer properties (spec-agnostic).
        for name, schema_def in schemas.items():
            if not isinstance(schema_def, dict):
                continue
            int_missing = []
            hooks._walk_schema(schema_def, lambda s: (
                int_missing.append(s) if s.get("type") == "integer" and "minimum" not in s else None
            ))
            assert not int_missing, f"{name}: {len(int_missing)} integer fields missing limits"

    def test_label_properties_have_constraints(self, real_raw):
        hooks.before_load_schema(None, real_raw)
        schemas = real_raw.get("components", {}).get("schemas", {})
        found = False
        for schema_def in schemas.values():
            if not isinstance(schema_def, dict):
                continue
            props = schema_def.get("properties", {})
            labels = props.get("labels")
            if isinstance(labels, dict) and labels.get("type") == "object":
                assert "propertyNames" in labels, "labels property should have propertyNames"
                found = True
        if not found:
            # Not every spec defines ``labels`` locally — core/v1alpha1
            # imports ObjectMeta from another document.  Skipping here would
            # make this case silently vacuous on exactly the specs the
            # imported-definition handling exists for, so the check is
            # instead made against the resolved request bodies.
            spec_path = _find_spec_path()
            assert spec_path is not None
            _assert_resolved_labels_constrained(spec_path)


class TestConftestHookRegistration:
    """Verify the actual conftest.py registers hooks before schema load.

    Subprocesses run in a private temporary CWD with a neutral empty
    ``schemathesis.toml`` so that ambient project TOML files (which may
    reference hooks that trigger network fixtures at import time) are
    never discovered by ``SchemathesisConfig.discover()``.  A minimal
    spec with a ``labels`` property is used so the test is self-contained
    and independent of any real spec's schema inventory.
    """

    _MINI_SPEC = json.dumps({
        "openapi": "3.0.3",
        "info": {"title": "conftest-test", "version": "1"},
        "paths": {"/x": {"get": {"responses": {"200": {"description": "OK"}}}}},
        "components": {"schemas": {"Obj": {
            "type": "object",
            "properties": {"labels": {
                "type": "object",
                "additionalProperties": {"type": "string"},
            }},
        }}},
    })

    @pytest.fixture
    def _env(self, tmp_path):
        test_api_dir = os.path.abspath(os.path.dirname(__file__))
        # Write a self-contained spec so labels are always available.
        spec_file = tmp_path / "mini_spec.json"
        spec_file.write_text(self._MINI_SPEC)
        return test_api_dir, str(spec_file)

    def _run_conftest_check(self, test_api_dir, spec_path, tmp_path,
                            *, conftest_dir=None):
        """Import conftest.py in a clean subprocess and return (ok, output).

        The child runs in *tmp_path* which contains a neutral empty
        ``schemathesis.toml`` — preventing ambient TOML discovery and
        side-effect-laden hook imports.
        """
        import subprocess
        import sys

        if conftest_dir is None:
            conftest_dir = test_api_dir
        # Neutral TOML prevents ambient hook discovery.
        neutral_toml = tmp_path / "schemathesis.toml"
        if not neutral_toml.exists():
            neutral_toml.write_text("")
        script = (
            "import os, sys\n"
            f"sys.path.insert(0, {test_api_dir!r})\n"
            f"sys.path.insert(0, {conftest_dir!r})\n"
            f"os.environ['SPEC_PATH'] = {spec_path!r}\n"
            "os.environ['BASE_URL'] = 'http://localhost:3443'\n"
            "os.environ['SCHEMATHESIS_HOOKS'] = ''\n"
            "import conftest\n"
            "raw = conftest.schema.raw_schema\n"
            "labels = raw['components']['schemas']['Obj']"
            "['properties']['labels']\n"
            "has_pn = 'propertyNames' in labels\n"
            "print(f'propertyNames={has_pn}')\n"
            "sys.exit(0 if has_pn else 1)\n"
        )
        result = subprocess.run(
            [sys.executable, "-c", script],
            capture_output=True, text=True, timeout=30,
            cwd=str(tmp_path),
        )
        return result.returncode == 0, result.stdout + result.stderr

    def test_conftest_applies_constraints_via_from_path(self, _env, tmp_path):
        """Real conftest.py (with 'import common') applies before_load_schema."""
        ok, output = self._run_conftest_check(*_env, tmp_path)
        assert ok, f"Conftest schema missing constraints:\n{output}"

    def test_conftest_without_import_common_lacks_constraints(self, _env, tmp_path):
        """Without 'import common', conftest loads the schema without hooks.

        This is the regression baseline: removing the import line must
        cause constraints to be absent, proving the import is required.
        Asserts the specific expected output (propertyNames=False) so
        that an unrelated subprocess failure cannot satisfy the baseline.
        """
        test_api_dir, spec_path = _env
        # Write a conftest.py without the 'import common' line
        real_conftest = os.path.join(test_api_dir, "conftest.py")
        with open(real_conftest) as f:
            lines = f.read().splitlines(keepends=True)
        child_dir = tmp_path / "neg"
        child_dir.mkdir()
        with open(child_dir / "conftest.py", "w") as f:
            for line in lines:
                if line.startswith("import common"):
                    continue
                f.write(line)
        ok, output = self._run_conftest_check(
            test_api_dir, spec_path, tmp_path, conftest_dir=str(child_dir),
        )
        assert not ok, (
            "Without 'import common', conftest schema should lack constraints. "
            f"Got: {output}"
        )
        assert "propertyNames=False" in output, (
            "Expected 'propertyNames=False' in output to confirm constraints "
            f"are absent (not an unrelated error). Got: {output}"
        )

    @pytest.mark.parametrize("with_import_common", [True, False],
                             ids=["positive", "negative"])
    def test_ambient_toml_hooks_not_loaded_in_child(
        self, _env, tmp_path, monkeypatch, with_import_common,
    ):
        """The helper's neutral-CWD isolation must prevent ambient TOML
        hooks from being imported in the child subprocess.

        Sets the PARENT process CWD to an ambient directory containing a
        ``schemathesis.toml`` with ``hooks = "sentinel_pkg"`` (a module
        that writes a marker file when imported).  Then invokes the real
        ``_run_conftest_check`` helper.  Asserts:
        - positive: propertyNames=True (real conftest applies hooks)
        - negative: propertyNames=False (stripped conftest lacks hooks)
        - both: sentinel marker absent (ambient hooks never loaded)
        """
        test_api_dir, spec_path = _env

        # Sentinel module that writes a marker file on import.
        marker = tmp_path / "SENTINEL_IMPORTED"
        sentinel_dir = tmp_path / "sentinel_pkg"
        sentinel_dir.mkdir()
        (sentinel_dir / "__init__.py").write_text(
            f"import pathlib; pathlib.Path({str(marker)!r}).write_text('yes')\n"
        )
        # Ambient TOML in a directory we'll chdir to.
        ambient_dir = tmp_path / "ambient"
        ambient_dir.mkdir()
        (ambient_dir / "schemathesis.toml").write_text('hooks = "sentinel_pkg"\n')
        # Put sentinel on PYTHONPATH so it would be importable from CWD.
        monkeypatch.setenv("PYTHONPATH", str(tmp_path))
        # Set parent CWD to ambient dir (simulates /app with symlinked TOML).
        monkeypatch.chdir(ambient_dir)

        # Build conftest_dir for positive vs negative.
        conftest_dir = None  # positive: use real conftest
        if not with_import_common:
            neg_dir = tmp_path / "neg"
            neg_dir.mkdir(exist_ok=True)
            real_conftest = os.path.join(test_api_dir, "conftest.py")
            with open(real_conftest) as f:
                lines = f.read().splitlines(keepends=True)
            with open(neg_dir / "conftest.py", "w") as f:
                for line in lines:
                    if line.startswith("import common"):
                        continue
                    f.write(line)
            conftest_dir = str(neg_dir)

        # Use a SEPARATE child_tmp so the helper creates its own neutral CWD.
        child_tmp = tmp_path / "child_env"
        child_tmp.mkdir(exist_ok=True)
        ok, output = self._run_conftest_check(
            test_api_dir, spec_path, child_tmp, conftest_dir=conftest_dir,
        )

        # Sentinel must NOT have been imported (ambient TOML isolated).
        assert not marker.exists(), (
            "Sentinel module was imported — helper's CWD isolation failed"
        )
        # Conftest assertions.
        if with_import_common:
            assert ok, f"Positive conftest should apply constraints:\n{output}"
            assert "propertyNames=True" in output
        else:
            assert not ok, (
                "Negative conftest should lack constraints. "
                f"Got: {output}"
            )
            assert "propertyNames=False" in output, (
                "Expected 'propertyNames=False', not an unrelated error. "
                f"Got: {output}"
            )


# ===========================================================================
# Cross-document ($ref into another file) regression coverage
#
# Definitions imported from another document are absent from the root
# document's ``components/schemas``, so a pass that only walks that section
# never sees them.  The real specs do exactly this, e.g.
# ``api/core/v1alpha1/openapi.yaml`` refers to
# ``../v1beta1/openapi.yaml#/components/schemas/ObjectMeta``.
#
# These tests go through the public ``from_path`` loader and prove the
# constraints from publicly generated request bodies, so they assert
# observable behaviour rather than any loader-internal layout.
# ===========================================================================

# Independent restatements of the contracts under test.  They are written
# from the RFC 1123 / Kubernetes label / int64 definitions on purpose and must
# NOT be derived from the production helpers: otherwise a regression in those
# helpers would silently redefine what the tests expect.
_EXPECTED_RFC1123 = re.compile(
    r"^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$")
_EXPECTED_LABEL_KEY = re.compile(r"^[a-zA-Z]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$")
_EXPECTED_LABEL_VALUE = re.compile(r"^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$")
_EXPECTED_INT64_MIN = -9223372036854775808
_EXPECTED_INT64_MAX = 9223372036854775807

_EXTERNAL_DOC = """\
openapi: 3.0.3
info:
  title: Shared definitions
  version: '1'
paths: {}
components:
  schemas:
    ObjectMeta:
      type: object
      required: [name, labels, generation]
      properties:
        name:
          type: string
          maxLength: 253
        labels:
          type: object
          minProperties: 1
          additionalProperties:
            type: string
        generation:
          type: integer
          format: int64
"""

_ROOT_DOC = """\
openapi: 3.0.3
info:
  title: Importing document
  version: '1'
paths:
  /resources:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Resource'
      responses:
        '201':
          description: Created
components:
  schemas:
    Resource:
      type: object
      required: [metadata, localCount]
      properties:
        metadata:
          $ref: '../external/openapi.yaml#/components/schemas/ObjectMeta'
        localCount:
          type: integer
          format: int64
"""


def _write_cross_file_specs(tmp_path, root_doc=_ROOT_DOC, external_doc=_EXTERNAL_DOC):
    """Write a root document that imports from a sibling directory.

    The relative ``../external/openapi.yaml`` hop mirrors the real repository
    layout.  Returns the path of the root document — the file the public
    loader is pointed at.
    """
    root_dir = tmp_path / "root"
    external_dir = tmp_path / "external"
    root_dir.mkdir()
    external_dir.mkdir()
    (external_dir / "openapi.yaml").write_text(external_doc)
    root_path = root_dir / "openapi.yaml"
    root_path.write_text(root_doc)
    return root_path


def _generate_bodies(spec_path, path="/resources", method="POST", count=15):
    """Return request bodies generated through the public loader + strategy.

    ``Unsatisfiable`` is turned into an explicit failure: the directory-wide
    conftest hook would otherwise convert it into an xfail and hide a real
    regression.
    """
    from hypothesis import given as h_given, settings as h_settings
    from hypothesis.errors import Unsatisfiable

    loaded = schemathesis.openapi.from_path(str(spec_path))
    operation = loaded[path][method]
    bodies = []

    @h_settings(max_examples=count, derandomize=True, deadline=None)
    @h_given(case=operation.as_strategy())
    def _collect(case):
        bodies.append(case.body)

    try:
        _collect()
    except Unsatisfiable as exc:  # pragma: no cover - regression guard
        pytest.fail(f"Body generation became unsatisfiable: {exc}")
    assert bodies, "no request bodies were generated"
    return bodies


def _assert_responses_match_document(spec_path, path="/resources",
                                     method="POST"):
    """Assert the loaded ``responses`` are identical to the document's own.

    The hooks must not touch responses.  Comparing the loaded operation's
    ``responses`` with the same subtree parsed straight from the file makes
    that a single equality rather than a list of "this keyword is absent"
    checks, so a constraint from any family — or any future one — shows up.
    """
    import yaml

    with open(spec_path) as handle:
        on_disk = yaml.safe_load(handle)
    expected = on_disk["paths"][path][method.lower()]["responses"]

    loaded = schemathesis.openapi.from_path(str(spec_path))
    actual = loaded[path][method].definition.raw["responses"]

    assert actual == expected, (
        "the loaded responses differ from the document:\n"
        f"  document: {expected!r}\n  loaded:   {actual!r}")
    return expected


def _resolved_body_schema(spec_path, path="/resources", method="POST"):
    """Return the request-body schema of an operation, as the loader resolved it.

    Reached through the public loader and the public ``operation.body``
    accessor, so no assumption is made about how the loader stores what it
    pulled in from another document.
    """
    loaded = schemathesis.openapi.from_path(str(spec_path))
    for body in loaded[path][method].body:
        schema = body.definition.get("schema")
        if isinstance(schema, dict):
            return schema
    raise AssertionError("operation has no request-body schema")


def _iter_schema_nodes(root):
    """Yield every schema node reachable from *root* by JSON Schema keywords.

    Deliberately written independently of the production walker so the two
    cannot drift into agreeing by construction.  It enters only standard
    keywords and document-local ``$ref`` targets, so ``example``, ``default``
    and vendor extensions are never mistaken for schemas — a generic walk
    over every dict value would report data that merely looks like a schema
    and make the assertions below quietly wrong in both directions.
    """
    nodes = []
    seen = set()

    def resolve(ref):
        if not isinstance(ref, str) or not ref.startswith("#/"):
            return None
        node = root
        for raw in unquote(ref[2:]).split("/"):
            token = raw.replace("~1", "/").replace("~0", "~")
            if isinstance(node, list):
                if not re.fullmatch(r"0|[1-9][0-9]*", token) \
                        or int(token) >= len(node):
                    return None
                node = node[int(token)]
            elif isinstance(node, dict) and token in node:
                node = node[token]
            else:
                return None
        return node

    def walk(schema):
        if not isinstance(schema, dict) or id(schema) in seen:
            return
        seen.add(id(schema))
        nodes.append(schema)
        target = resolve(schema.get("$ref"))
        if isinstance(target, dict):
            walk(target)
        for key in ("items", "additionalProperties", "not"):
            if key in schema:
                walk(schema[key])
        for key in ("properties", "patternProperties"):
            if isinstance(schema.get(key), dict):
                for sub in schema[key].values():
                    walk(sub)
        for key in ("allOf", "anyOf", "oneOf", "prefixItems"):
            if isinstance(schema.get(key), list):
                for sub in schema[key]:
                    walk(sub)

    walk(root)
    return nodes


def _find_label_maps(root):
    """Collect every ``labels`` property map reachable from *root*.

    Searches by JSON Schema structure rather than by container name, so it
    does not depend on where the loader placed a resolved definition.
    """
    found = []
    for node in _iter_schema_nodes(root):
        props = node.get("properties")
        if isinstance(props, dict):
            labels = props.get("labels")
            if isinstance(labels, dict) and labels.get("type") == "object":
                found.append(labels)
    return found


def _find_integer_schemas(root):
    """Collect every ``type: integer`` schema node reachable from *root*."""
    return [n for n in _iter_schema_nodes(root) if n.get("type") == "integer"]


def _find_name_schemas(root):
    """Collect every ``properties.name`` the name-pattern rule applies to."""
    found = []
    for node in _iter_schema_nodes(root):
        props = node.get("properties")
        if isinstance(props, dict):
            name = props.get("name")
            if isinstance(name, dict) and name.get("maxLength") == 253:
                found.append(name)
    return found


def _find_composition_branches(root):
    """Collect every direct ``allOf``/``anyOf``/``oneOf`` branch under *root*."""
    found = []
    for node in _iter_schema_nodes(root):
        for key in ("allOf", "anyOf", "oneOf"):
            branches = node.get(key)
            if isinstance(branches, list):
                found.extend(b for b in branches if isinstance(b, dict))
    return found


class TestCrossFileImportedMetadataConstraints:
    """Imported definitions receive the same constraints as local ones."""

    def test_imported_name_gets_rfc1123_pattern(self, tmp_path):
        """``ObjectMeta.name`` imported from another file is RFC 1123 valid.

        The local ``localCount`` assertion is the positive control: it proves
        the hooks ran at all, so a failure on ``name`` is specific to the
        imported definition rather than a dead harness.
        """
        spec_path = _write_cross_file_specs(tmp_path)
        bodies = _generate_bodies(spec_path)

        for body in bodies:
            count = body["localCount"]
            assert _EXPECTED_INT64_MIN <= count <= _EXPECTED_INT64_MAX, (
                f"positive control failed: local integer {count} outside int64"
            )

        offenders = [
            body["metadata"]["name"] for body in bodies
            if not _EXPECTED_RFC1123.fullmatch(body["metadata"]["name"])
        ]
        assert not offenders, (
            f"{len(offenders)}/{len(bodies)} generated names violate RFC 1123 — "
            f"the imported ObjectMeta.name lost its pattern. Sample: {offenders[:3]!r}"
        )

    def test_imported_label_values_are_generated_valid(self, tmp_path):
        """Imported ``labels`` maps generate Kubernetes-valid values."""
        spec_path = _write_cross_file_specs(tmp_path)
        bodies = _generate_bodies(spec_path)

        bad_values = []
        for body in bodies:
            labels = body["metadata"]["labels"]
            assert labels, "minProperties: 1 should keep this map non-empty"
            bad_values.extend(
                value for value in labels.values()
                if not _EXPECTED_LABEL_VALUE.fullmatch(value)
            )
        assert not bad_values, f"invalid generated label values: {bad_values[:3]!r}"

    def test_imported_label_keys_are_constrained(self, tmp_path):
        """Imported ``labels`` maps carry the label-key constraint.

        Key validity is expressed with ``propertyNames``, which an OpenAPI
        3.0 document cannot express to the generator — the generated keys
        are therefore unconstrained no matter what the hooks do.  This case
        consequently asserts on the resolved request body reached through
        the public operation, which is the same thing the suite enforces at
        run time; the generated-value cases above carry the behavioural
        proof.
        """
        spec_path = _write_cross_file_specs(tmp_path)
        label_maps = _find_label_maps(_resolved_body_schema(spec_path))

        assert label_maps, "no imported labels map found in the resolved body"
        for labels in label_maps:
            property_names = labels.get("propertyNames", {})
            assert property_names.get("pattern") == _EXPECTED_LABEL_KEY.pattern, (
                "imported labels map lost its key constraint: "
                f"{property_names!r}"
            )

    def test_imported_integer_gets_int64_limits(self, tmp_path):
        """Every integer in the resolved body carries its Go range.

        An unconstrained integer usually still generates a small value, so
        generation alone cannot tell a missing range from a present one.
        The range is therefore asserted on the resolved request body, and
        the generated values are checked as a secondary guard.  The local
        ``localCount`` field is included, so this also keeps the existing
        local-integer behaviour under test.
        """
        spec_path = _write_cross_file_specs(tmp_path)
        integers = _find_integer_schemas(_resolved_body_schema(spec_path))

        assert len(integers) >= 2, (
            "expected the imported integer and the local control, "
            f"found {len(integers)}")
        for schema in integers:
            assert schema.get("minimum") == _EXPECTED_INT64_MIN, \
                f"integer missing int64 minimum: {schema!r}"
            assert schema.get("maximum") == _EXPECTED_INT64_MAX, \
                f"integer missing int64 maximum: {schema!r}"

        for body in _generate_bodies(spec_path):
            value = body["metadata"]["generation"]
            assert _EXPECTED_INT64_MIN <= value <= _EXPECTED_INT64_MAX

    def test_existing_imported_constraints_are_not_overwritten(self, tmp_path):
        """Every family the pass applies defers to a stated constraint.

        All three families use ``setdefault``, and each is checked here:
        asserting only the name pattern would leave the label-value pattern
        and the integer bounds free to overwrite a document's own narrower
        choice without any test noticing.
        """
        external = _EXTERNAL_DOC.replace(
            "          maxLength: 253\n",
            "          maxLength: 253\n          pattern: '^fixed-[a-z]+$'\n",
        ).replace(
            """          additionalProperties:
            type: string
""",
            """          additionalProperties:
            type: string
            pattern: '^v[0-9]+$'
""",
        ).replace(
            """        generation:
          type: integer
          format: int64
""",
            """        generation:
          type: integer
          format: int64
          minimum: 10
          maximum: 20
""",
        )
        spec_path = _write_cross_file_specs(tmp_path, external_doc=external)

        # Stated values survive in the resolved body ...
        name_schemas = _find_name_schemas(_resolved_body_schema(spec_path))
        assert name_schemas, "imported name schema not found in resolved body"
        for schema in name_schemas:
            assert schema.get("pattern") == "^fixed-[a-z]+$", schema
        for labels in _find_label_maps(_resolved_body_schema(spec_path)):
            assert labels["additionalProperties"].get("pattern") == "^v[0-9]+$"
        bounded = [s for s in _find_integer_schemas(_resolved_body_schema(spec_path))
                   if s.get("maximum") == 20]
        assert bounded, "the document's own integer bounds were replaced"
        for schema in bounded:
            assert schema.get("minimum") == 10, schema

        # ... and in what is actually generated from it.
        own_name = re.compile(r"^fixed-[a-z]+$")
        own_value = re.compile(r"^v[0-9]+$")
        for body in _generate_bodies(spec_path):
            name = body["metadata"]["name"]
            assert own_name.fullmatch(name), f"name pattern replaced: {name!r}"
            for value in body["metadata"]["labels"].values():
                assert own_value.fullmatch(value), \
                    f"label-value pattern replaced: {value!r}"
            assert 10 <= body["metadata"]["generation"] <= 20, \
                f"integer bounds replaced: {body['metadata']['generation']}"

    def test_responses_are_untouched_while_the_body_is_constrained(self, tmp_path):
        """Constraining the body leaves the operation's responses alone."""
        spec_path = _write_cross_file_specs(tmp_path)
        assert _find_name_schemas(_resolved_body_schema(spec_path)), \
            "precondition: the body must be the constrained one"
        _assert_responses_match_document(spec_path)

    def test_repeated_loads_stay_consistent(self, tmp_path):
        """Loading the same document twice applies constraints identically."""
        spec_path = _write_cross_file_specs(tmp_path)
        first = _generate_bodies(spec_path)
        second = _generate_bodies(spec_path)

        for body in first + second:
            assert _EXPECTED_RFC1123.fullmatch(body["metadata"]["name"])
        first_names = [b["metadata"]["name"] for b in first]
        second_names = [b["metadata"]["name"] for b in second]
        assert first_names == second_names, \
            "derandomised generation should be stable across loads"


_ALLOF_EXTERNAL_DOC = """\
openapi: 3.0.3
info:
  title: Shared base
  version: '1'
paths: {}
components:
  schemas:
    Base:
      type: object
      required: [core, meta]
      properties:
        core:
          type: string
        meta:
          $ref: '#/components/schemas/ObjectMeta'
    ObjectMeta:
      type: object
      required: [name]
      properties:
        name:
          type: string
          maxLength: 253
"""

_ALLOF_ROOT_DOC = """\
openapi: 3.0.3
info:
  title: Composing document
  version: '1'
paths:
  /resources:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Resource'
      responses:
        '201':
          description: Created
components:
  schemas:
    Resource:
      allOf:
        - $ref: '../external/openapi.yaml#/components/schemas/Base'
        - type: object
          required: [extra]
          properties:
            extra:
              type: string
"""


class TestCrossFileRefWalkerSafety:
    """The resolved-body walk stays within standard JSON Schema semantics."""

    def test_escaped_pointer_tokens_resolve(self):
        """``~1``/``~0`` and percent-encoding follow RFC 6901 / URI rules."""
        target = {"type": "integer"}
        root = {"a/b": {"~tilde": target}, "with space": {"type": "string"}}
        assert hooks._resolve_local_pointer(root, "#/a~1b/~0tilde") is target
        assert hooks._resolve_local_pointer(root, "#/with%20space") is \
            root["with space"]

    def test_percent_decoding_precedes_token_splitting(self):
        """``%2F`` is a separator, as RFC 6901 section 6 defines it.

        The fragment is decoded first and only then split, so ``%2F``
        becomes a step in the path.  A literal ``/`` inside one token is
        spelled ``~1``; reading ``%2F`` as part of a token would make the
        two spellings mean the same thing, which they do not.
        """
        target = {"type": "integer"}
        root = {"a": {"b": target}, "a/b": {"type": "string"}}
        assert hooks._resolve_local_pointer(root, "#/a%2Fb") is target
        assert hooks._resolve_local_pointer(root, "#/a~1b") is root["a/b"]

    def test_escape_unescaping_order_is_not_reentrant(self):
        """``~01`` means a literal ``~1``, not a separator."""
        target = {"type": "integer"}
        root = {"~1": target, "/": {"type": "string"}}
        assert hooks._resolve_local_pointer(root, "#/~01") is target

    def test_list_index_tokens_resolve(self):
        target = {"type": "string"}
        root = {"allOf": [{"type": "integer"}, target]}
        assert hooks._resolve_local_pointer(root, "#/allOf/0") is root["allOf"][0]
        assert hooks._resolve_local_pointer(root, "#/allOf/1") is target
        assert hooks._resolve_local_pointer(root, "#/allOf/9") is None

    @pytest.mark.parametrize("token", ["01", "+1", "1.0", "-1", "١", ""])
    def test_malformed_list_indices_are_rejected(self, token):
        """Only RFC 6901 array indices index a list.

        ``str.isdigit`` would accept ``01`` and non-ASCII digits such as
        ``١``, silently resolving a pointer the standard does not define.
        """
        root = {"allOf": [{"type": "integer"}, {"type": "string"}]}
        assert hooks._resolve_local_pointer(root, f"#/allOf/{token}") is None

    def test_unresolvable_references_are_left_alone(self):
        """Missing and non-local references resolve to nothing, not to {}."""
        root = {"components": {}}
        for ref in ("#/components/schemas/Absent",
                    "../other/openapi.yaml#/components/schemas/ObjectMeta",
                    "http://example.test/schema.json#/x",
                    "#", None, 17):
            assert hooks._resolve_local_pointer(root, ref) is None, ref

    def test_reference_cycle_terminates(self):
        """A reference cycle is walked once per node and terminates."""
        root = {
            "defs": {
                "A": {"type": "object",
                      "properties": {"b": {"$ref": "#/defs/B"}}},
                "B": {"type": "object",
                      "properties": {"a": {"$ref": "#/defs/A"}}},
            },
        }
        visited = []
        hooks._walk_resolved_schema(root["defs"]["A"], root, visited.append)
        assert sum(node is root["defs"]["A"] for node in visited) == 1
        assert sum(node is root["defs"]["B"] for node in visited) == 1

    def test_example_default_and_extension_data_untouched(self):
        """Data that merely looks like a schema is never modified."""
        lookalike = {"type": "integer", "format": "int64"}
        root = {
            "type": "object",
            "properties": {"real": {"type": "integer", "format": "int64"}},
            "example": {"type": "integer", "format": "int64"},
            "default": {"type": "integer", "format": "int64"},
            "x-vendor": {"schemas": {"fake": dict(lookalike)}},
        }
        hooks._walk_resolved_schema(root, root, hooks._value_constraints_visitor)

        assert root["properties"]["real"]["minimum"] == hooks.INT64_MIN
        for key in ("example", "default"):
            assert root[key] == lookalike, f"{key} data was modified"
        assert root["x-vendor"]["schemas"]["fake"] == lookalike, \
            "vendor extension data was modified"

    def test_cross_file_composition_generates_both_branches(self, tmp_path):
        """An imported allOf branch stays satisfiable and gains constraints.

        Guards the composition policy: if ``additionalProperties: false``
        were applied to the imported branch, one branch would reject the
        other's properties and generation would become unsatisfiable.
        """
        spec_path = _write_cross_file_specs(
            tmp_path, root_doc=_ALLOF_ROOT_DOC,
            external_doc=_ALLOF_EXTERNAL_DOC,
        )
        bodies = _generate_bodies(spec_path)

        for body in bodies:
            assert "core" in body, "imported branch's required field missing"
            assert "extra" in body, "local branch's required field missing"
            assert _EXPECTED_RFC1123.fullmatch(body["meta"]["name"]), (
                "nested imported ObjectMeta.name lost its pattern: "
                f"{body['meta']['name']!r}")

        resolved = _resolved_body_schema(spec_path)
        for branch in _find_composition_branches(resolved):
            assert branch.get("additionalProperties") is not False, (
                "a composition branch was closed, which can make the "
                f"schema unsatisfiable: {branch!r}")

    def test_response_only_import_is_not_pulled_into_request_body(self, tmp_path):
        """A definition imported only for a response stays out of the body pass."""
        root_doc = _ROOT_DOC.replace(
            """        metadata:
          $ref: '../external/openapi.yaml#/components/schemas/ObjectMeta'
""",
            "",
        ).replace("      required: [metadata, localCount]",
                  "      required: [localCount]").replace(
            """        '201':
          description: Created""",
            """        '201':
          description: Created
          content:
            application/json:
              schema:
                $ref: '../external/openapi.yaml#/components/schemas/ObjectMeta'""")
        spec_path = _write_cross_file_specs(tmp_path, root_doc=root_doc)

        resolved = _resolved_body_schema(spec_path)
        assert not _find_label_maps(resolved), (
            "a response-only import was pulled into the request body")
        for body in _generate_bodies(spec_path):
            assert set(body) == {"localCount"}, (
                f"unexpected request-body fields: {sorted(body)}")

        # The body staying clean is only half the claim: assert directly
        # that the response definition was not modified either, which is
        # what "response-only definitions keep today's behaviour" means.
        # Equality against the document's own text is the strongest form —
        # it fails on any added keyword anywhere under ``responses``, not
        # only on the three families this pass happens to apply.
        responses = _assert_responses_match_document(spec_path)
        assert "ObjectMeta" in json.dumps(responses), (
            "the response must still import ObjectMeta, otherwise this "
            "case proves nothing about response-only definitions")


def _sibling_spec_path(relative):
    """Return a real spec path alongside the one ``_find_spec_path`` locates.

    Works in both layouts the suite supports: ``<repo>/api/<relative>`` and
    the container's ``/app/specs/<relative>``.  ``_find_spec_path`` resolves
    in both — from ``SPEC_PATH`` in the container and from this file's
    location in a checkout — so a missing base is a broken environment and
    is reported as a failure rather than skipped away.
    """
    base = _find_spec_path()
    assert base is not None, (
        "no OpenAPI spec found: expected SPEC_PATH (container) or "
        "<repo>/api/core/v1beta1/openapi.yaml (checkout)")
    specs_root = os.path.dirname(os.path.dirname(os.path.dirname(base)))
    return os.path.join(specs_root, *relative.split("/"))


def _assert_resolved_labels_constrained(spec_path):
    """Assert every labels map in every resolved request body is constrained.

    Goes through the public loader and the public per-operation request-body
    accessor, so imported definitions are covered exactly as the suite sees
    them at run time.
    """
    loaded = schemathesis.openapi.from_path(str(spec_path))
    label_maps = []
    for result in loaded.get_all_operations():
        operation = result.ok() if hasattr(result, "ok") else result
        if operation is None:
            continue
        for body in operation.body:
            schema = body.definition.get("schema")
            if isinstance(schema, dict):
                label_maps.extend(_find_label_maps(schema))

    assert label_maps, f"no labels map found in any request body of {spec_path}"
    for labels in label_maps:
        assert labels.get("propertyNames", {}).get("pattern") == \
            _EXPECTED_LABEL_KEY.pattern, \
            f"labels map missing key constraint in {spec_path}: {labels!r}"
        additional = labels.get("additionalProperties")
        if isinstance(additional, dict) and additional.get("type") == "string":
            assert additional.get("pattern") == _EXPECTED_LABEL_VALUE.pattern, \
                f"labels map missing value constraint in {spec_path}"
    return len(label_maps)


_SELF_CONTAINED_DOC = """\
openapi: 3.0.3
info:
  title: Self-contained document
  version: '1'
paths:
  /resources:
    post:
      requestBody:
        required: true
        content:
          application/json:
            schema:
              $ref: '#/components/schemas/Resource'
      responses:
        '201':
          description: Created
components:
  schemas:
    Resource:
      type: object
      required: [metadata]
      properties:
        metadata:
          $ref: '#/components/schemas/ObjectMeta'
    ObjectMeta:
      type: object
      required: [name, labels, generation]
      properties:
        name:
          type: string
          maxLength: 253
        labels:
          type: object
          minProperties: 1
          additionalProperties:
            type: string
        generation:
          type: integer
          format: int64
"""


class TestLocalDefinitionsAreAlsoConstrained:
    """A document that imports nothing stays constrained.

    This is a positive guard, not a regression test: it is not known to
    have failed before the imported-definition pass was added.  It pins the
    plainer half of the contract — a definition the document declares
    itself, referenced through ``components/schemas`` — so that work on the
    imported-definition path cannot quietly cost the local one its
    constraints.
    """

    def test_local_definitions_are_constrained(self, tmp_path):
        spec_path = tmp_path / "openapi.yaml"
        spec_path.write_text(_SELF_CONTAINED_DOC)

        resolved = _resolved_body_schema(spec_path)

        names = _find_name_schemas(resolved)
        assert names, "no name schema in the resolved body"
        for schema in names:
            assert schema.get("pattern") == _EXPECTED_RFC1123.pattern, schema

        label_maps = _find_label_maps(resolved)
        assert label_maps, "no labels map in the resolved body"
        for labels in label_maps:
            assert labels.get("propertyNames", {}).get("pattern") == \
                _EXPECTED_LABEL_KEY.pattern, labels
            assert labels["additionalProperties"].get("pattern") == \
                _EXPECTED_LABEL_VALUE.pattern, labels

        integers = _find_integer_schemas(resolved)
        assert integers, "no integer schema in the resolved body"
        for schema in integers:
            assert schema.get("minimum") == _EXPECTED_INT64_MIN, schema
            assert schema.get("maximum") == _EXPECTED_INT64_MAX, schema

    def test_local_definitions_generate_valid_values(self, tmp_path):
        spec_path = tmp_path / "openapi.yaml"
        spec_path.write_text(_SELF_CONTAINED_DOC)

        for body in _generate_bodies(spec_path):
            metadata = body["metadata"]
            assert _EXPECTED_RFC1123.fullmatch(metadata["name"]), metadata["name"]
            for value in metadata["labels"].values():
                assert _EXPECTED_LABEL_VALUE.fullmatch(value), value
            assert _EXPECTED_INT64_MIN <= metadata["generation"] <= _EXPECTED_INT64_MAX


class TestRealSpecImportedLabelCoverage:
    """Real specs that import ObjectMeta are covered, not skipped.

    ``core/v1alpha1`` and ``imagebuilder/v1alpha1`` take ``ObjectMeta`` from
    another document, so a check that only reads the local
    ``components/schemas`` section finds no ``labels`` at all and passes
    without testing anything.  These cases assert through the resolved
    request bodies instead.
    """

    @pytest.mark.parametrize("relative", [
        "core/v1alpha1/openapi.yaml",
        "imagebuilder/v1alpha1/openapi.yaml",
    ])
    def test_imported_labels_constrained_in_real_spec(self, relative):
        spec_path = _sibling_spec_path(relative)
        assert os.path.isfile(spec_path), (
            f"expected a real spec at {spec_path}; the suite must not pass "
            "by silently finding nothing")
        assert _assert_resolved_labels_constrained(spec_path) > 0
