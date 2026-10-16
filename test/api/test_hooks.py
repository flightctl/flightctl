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

def _make_raw_spec(schemas=None, *, paths=None):
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
        spec["components"] = {"schemas": schemas}
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
        """Composition target stays unconstrained through the public from_dict loader."""
        spec = _make_raw_spec({
            "Resource": {"allOf": [
                {"$ref": "#/components/schemas/Base"},
                {"type": "object", "properties": {"extra": {"type": "string"}}},
            ]},
            "Base": {"type": "object", "properties": {"core": {"type": "string"}}},
        })
        # from_dict fires before_load_schema, which modifies the spec dict in-place
        loaded = schemathesis.openapi.from_dict(spec)
        # Assert the raw component was modified by the hook (not via x-bundled)
        assert "additionalProperties" not in spec["components"]["schemas"]["Base"], \
            "Composition target Base must remain unconstrained after from_dict loading"
        # Loaded schema produces a usable operation
        op = loaded["/resources"]["POST"]
        assert op.body, "POST /resources must have a body definition"


class TestBeforeLoadSchemaOnRealSpec:
    """Smoke tests against the actual flightctl OpenAPI spec using raw hook."""

    @pytest.fixture
    def real_raw(self):
        import os
        import yaml
        spec_path = os.path.join(
            os.path.dirname(__file__), "..", "..", "api", "core", "v1beta1", "openapi.yaml",
        )
        if not os.path.isfile(spec_path):
            pytest.skip("openapi.yaml not found")
        with open(spec_path) as f:
            return yaml.safe_load(f)

    def test_device_schema_has_integer_limits(self, real_raw):
        hooks.before_load_schema(None, real_raw)
        device = real_raw["components"]["schemas"]["Device"]
        int_missing = []
        hooks._walk_schema(device, lambda s: (
            int_missing.append(s) if s.get("type") == "integer" and "minimum" not in s else None
        ))
        assert not int_missing, f"Device: {len(int_missing)} integer fields missing limits"

    def test_objectmeta_has_label_constraints(self, real_raw):
        hooks.before_load_schema(None, real_raw)
        meta = real_raw["components"]["schemas"].get("ObjectMeta", {})
        labels = meta.get("properties", {}).get("labels", {})
        assert "propertyNames" in labels, "ObjectMeta.labels should have propertyNames"


class TestConftestHookRegistration:
    """Verify conftest.py registers hooks before schema load (fresh-process)."""

    def test_conftest_registers_hooks_before_from_path(self):
        """In a fresh process, conftest's 'import common' must register
        before_load_schema so that from_path applies constraints.

        Without the import, the hook count is 0 and constraints are missing.
        This test spawns a subprocess to simulate conftest collection.
        """
        import subprocess
        import sys

        spec_path = os.path.join(
            os.path.dirname(__file__), "..", "..", "api", "core", "v1beta1", "openapi.yaml",
        )
        if not os.path.isfile(spec_path):
            pytest.skip("openapi.yaml not found")

        pythonpath = os.path.join(os.path.dirname(__file__), "..", "..")
        api_path = os.path.join(os.path.dirname(__file__), "..")

        script = (
            "import sys, os\n"
            f"sys.path.insert(0, {api_path!r})\n"
            f"os.environ['SPEC_PATH'] = {spec_path!r}\n"
            "os.environ['BASE_URL'] = 'http://localhost:3443'\n"
            "\n"
            "# Simulate conftest.py: import common, then load schema\n"
            "import common  # noqa: F401 -- registers hooks\n"
            "import schemathesis\n"
            "hook_count = len(schemathesis.hooks.get_all_by_name('before_load_schema'))\n"
            "assert hook_count > 0, 'before_load_schema not registered'\n"
            "\n"
            "schema = schemathesis.openapi.from_path(os.environ['SPEC_PATH'])\n"
            "raw = schema.raw_schema\n"
            "meta = raw['components']['schemas'].get('ObjectMeta', {})\n"
            "labels = meta.get('properties', {}).get('labels', {})\n"
            "assert 'propertyNames' in labels, (\n"
            "    'ObjectMeta.labels missing propertyNames — '\n"
            "    'before_load_schema did not fire during from_path'\n"
            ")\n"
            "print('PASS: hooks registered and constraints applied')\n"
        )
        result = subprocess.run(
            [sys.executable, "-c", script],
            capture_output=True, text=True, timeout=30,
        )
        assert result.returncode == 0, (
            f"Fresh-process conftest simulation failed:\n{result.stderr}"
        )
