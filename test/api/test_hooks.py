"""Unit tests for shared Schemathesis API hooks."""

import json
import os
from copy import deepcopy
from types import SimpleNamespace
from urllib.parse import urlsplit

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
            pytest.skip("No labels property found in spec")


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
