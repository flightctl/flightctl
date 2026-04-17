"""Shared Schemathesis hooks for flightctl API testing.

Provides common functionality used by ALL API suites (core + imagebuilder):
- TraceCov schema coverage integration
- Int64 range limit patching
- Protocol field injection (apiVersion, kind, metadata.name)
- Query parameter fixing
- Version and kind registration plumbing

Core-specific hooks live in core/hooks.py.
Response validation checks live in common/checks.py.
"""

import os
import re
import uuid
from urllib.parse import unquote

import schemathesis
import tracecov
import urllib3

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

tracecov.schemathesis.install()

# Patch TraceCov handler to also save JSON coverage report alongside HTML.
# TraceCov only saves HTML by default; JSON is needed by report.py.
# The @cli.handler() decorator returns None and appends to CUSTOM_HANDLERS,
# so we look up the class by name from the registry.
from schemathesis.cli.commands.run.executor import CUSTOM_HANDLERS
from schemathesis.engine import events as _engine_events

_TracecovHandler = next(h for h in CUSTOM_HANDLERS if h.__name__ == "TracecovHandler")
_orig_handle_event = _TracecovHandler.handle_event


def _handle_event_with_json(self, ctx, event):
    _orig_handle_event(self, ctx, event)
    if isinstance(event, _engine_events.EngineFinished) and self.coverage_map is not None:
        json_report = self.coverage_map.generate_report(format="json")
        html_path = (
            self.report_path
            or os.environ.get("SCHEMATHESIS_COVERAGE_REPORT_HTML_PATH")
            or "./schema-coverage.html"
        )
        json_path = html_path.replace(".html", ".json")
        with open(json_path, "w") as f:
            f.write(json_report)


_TracecovHandler.handle_event = _handle_event_with_json

# ---------------------------------------------------------------------------
# Configuration from environment
# ---------------------------------------------------------------------------
TOKEN = os.environ.get("SCHEMATHESIS_TOKEN", "")
CORE_URL = os.environ.get("CORE_URL", "")

VERSION_HEADER = "Flightctl-API-Version"
API_GROUP = "flightctl.io"

# ---------------------------------------------------------------------------
# Registration functions (called by service/version hooks)
# ---------------------------------------------------------------------------
VERSION = ""


def register_version(v):
    global VERSION
    VERSION = v


_PATH_TO_KIND = {}


def register_path_kinds(mapping):
    _PATH_TO_KIND.update(mapping)


# ---------------------------------------------------------------------------
# Integer range limits for schema patching
# ---------------------------------------------------------------------------
INT32_MIN = -(2 ** 31)
INT32_MAX = 2 ** 31 - 1
INT64_MIN = -(2 ** 63)
INT64_MAX = 2 ** 63 - 1


def _walk_schema(schema, visitor):
    """Walk a JSON schema tree, calling visitor(schema) at each node."""
    if not isinstance(schema, dict):
        return
    visitor(schema)
    for key in ("items", "additionalProperties", "not"):
        if key in schema:
            _walk_schema(schema[key], visitor)
    for key in ("properties", "patternProperties"):
        if key in schema and isinstance(schema[key], dict):
            for prop_schema in schema[key].values():
                _walk_schema(prop_schema, visitor)
    for key in ("allOf", "anyOf", "oneOf", "prefixItems"):
        if key in schema and isinstance(schema[key], list):
            for subschema in schema[key]:
                _walk_schema(subschema, visitor)
    if "x-bundled" in schema and isinstance(schema["x-bundled"], dict):
        for bundled_schema in schema["x-bundled"].values():
            _walk_schema(bundled_schema, visitor)


def _integer_limits_visitor(schema):
    if schema.get("type") == "integer":
        fmt = schema.get("format")
        if fmt == "int32":
            schema.setdefault("minimum", INT32_MIN)
            schema.setdefault("maximum", INT32_MAX)
        else:
            schema.setdefault("minimum", INT64_MIN)
            schema.setdefault("maximum", INT64_MAX)


def apply_integer_limits(schema):
    """Constrain all integer fields to their Go type range."""
    _walk_schema(schema, _integer_limits_visitor)


# ---------------------------------------------------------------------------
# Label key/value constraints for schema patching
# ---------------------------------------------------------------------------
_LABEL_KEY_PATTERN = r"^[a-zA-Z]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$"
_LABEL_VALUE_PATTERN = r"^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])?$"


def _find_composition_refs(schemas, prefix):
    """Find schema keys referenced by allOf/anyOf/oneOf via $ref."""
    refs = set()
    for s in schemas.values():
        if not isinstance(s, dict):
            continue
        for key in ("allOf", "anyOf", "oneOf"):
            for branch in s.get(key, []):
                if isinstance(branch, dict) and "$ref" in branch:
                    ref = branch["$ref"]
                    if ref.startswith(prefix + "/"):
                        refs.add(ref[len(prefix) + 1:])
    return refs


def disallow_extra_properties(schema, _inside_composition=False):
    """Set additionalProperties: false on object schemas that define properties.

    Schemas that are direct children of allOf/anyOf/oneOf are skipped to avoid
    making composed schemas unsatisfiable (properties from one branch would be
    rejected as "additional" by another branch).

    Bundled schemas referenced by allOf/anyOf/oneOf via $ref are also skipped
    for the same reason (e.g. CatalogItemConfigurable referenced by
    CatalogItemVersion's allOf).
    """
    if not isinstance(schema, dict):
        return

    if (not _inside_composition
            and schema.get("type") == "object"
            and "properties" in schema
            and "additionalProperties" not in schema
            and not any(k in schema for k in ("allOf", "anyOf", "oneOf"))):
        schema["additionalProperties"] = False

    for key in ("items", "additionalProperties", "not"):
        if key in schema and isinstance(schema[key], dict):
            disallow_extra_properties(schema[key])

    for key in ("properties", "patternProperties"):
        if key in schema and isinstance(schema[key], dict):
            for prop_schema in schema[key].values():
                disallow_extra_properties(prop_schema)

    for key in ("allOf", "anyOf", "oneOf", "prefixItems"):
        if key in schema and isinstance(schema[key], list):
            for subschema in schema[key]:
                disallow_extra_properties(subschema, _inside_composition=True)

    if "x-bundled" in schema and isinstance(schema["x-bundled"], dict):
        bundled = schema["x-bundled"]
        skip = _find_composition_refs(bundled, "#/x-bundled")
        for key, bundled_schema in bundled.items():
            disallow_extra_properties(bundled_schema, _inside_composition=key in skip)


def _label_constraints_visitor(schema):
    if "properties" in schema and isinstance(schema["properties"], dict):
        labels = schema["properties"].get("labels")
        if isinstance(labels, dict) and labels.get("type") == "object":
            ap = labels.get("additionalProperties")
            if isinstance(ap, dict) and ap.get("type") == "string":
                ap.setdefault("pattern", _LABEL_VALUE_PATTERN)
            labels.setdefault("propertyNames", {"pattern": _LABEL_KEY_PATTERN})


def apply_label_constraints(schema):
    """Add regex patterns to labels properties so schemathesis generates valid k8s labels."""
    _walk_schema(schema, _label_constraints_visitor)


def _name_pattern_visitor(schema):
    props = schema.get("properties")
    if isinstance(props, dict):
        name_prop = props.get("name")
        if isinstance(name_prop, dict) and name_prop.get("maxLength") == 253:
            name_prop.setdefault("pattern", _RFC1123_RE.pattern)


def apply_name_pattern(schema):
    """Add RFC 1123 pattern to ObjectMeta.name so schemathesis generates valid resource names."""
    _walk_schema(schema, _name_pattern_visitor)


# ---------------------------------------------------------------------------
# Make status required for PUT /{name}/status endpoints
# ---------------------------------------------------------------------------
def _require_status_for_status_endpoints(operation):
    if operation.method.upper() != "PUT" or not operation.path.endswith("/status"):
        return
    for body in operation.body:
        schema = body.definition.get("schema")
        if isinstance(schema, dict):
            required = schema.get("required")
            if isinstance(required, list) and "status" not in required:
                required.append("status")


_RESOURCE_NAME_PATH_PARAMS = {"name", "catalog", "fleet", "providername"}


@schemathesis.hook
def before_init_operation(context, operation):
    for parameter in operation.iter_parameters():
        schema = parameter.definition.get("schema")
        if schema is not None:
            apply_integer_limits(schema)
        if (parameter.definition.get("in") == "path"
                and parameter.definition.get("name") in _RESOURCE_NAME_PATH_PARAMS
                and isinstance(schema, dict)
                and schema.get("type") == "string"):
            schema.setdefault("pattern", _RFC1123_RE.pattern)
    for body in operation.body:
        schema = body.definition.get("schema")
        if schema is not None:
            apply_integer_limits(schema)
            apply_label_constraints(schema)
            apply_name_pattern(schema)
            disallow_extra_properties(schema)
    _require_status_for_status_endpoints(operation)


# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------
def _kind_for_path(path):
    """Return the resource kind for a given path template."""
    for prefix in sorted(_PATH_TO_KIND, key=len, reverse=True):
        if path == prefix or path.startswith(prefix + "/"):
            return _PATH_TO_KIND[prefix]
    return None


# ---------------------------------------------------------------------------
# Body mutation registry — scoped sub-functions called from mutate_body
# ---------------------------------------------------------------------------
_BODY_MUTATORS = []  # list of (fn, path_regex, methods, order)


def register_body_mutator(path=None, method=None, order=0):
    """Register a scoped body mutator.

    Args:
        path: regex to match operation path (None = all paths).
        method: str or list of HTTP methods (None = all methods).
        order: execution order; lower runs first (default 0).

    Mutator signature: fn(body, *, path, method, **_)
    Use **_ to ignore kwargs you don't need.
    """
    if isinstance(method, str):
        method = [method]

    def decorator(fn):
        _BODY_MUTATORS.append((fn, path, method, order))
        _BODY_MUTATORS.sort(key=lambda entry: entry[3])
        return fn
    return decorator


# ---------------------------------------------------------------------------
# Common mutators (registered with negative order to run first)
# ---------------------------------------------------------------------------
@register_body_mutator(order=-20)
def _set_protocol_fields(body, path, method, **_):
    """Set apiVersion, kind, metadata for resource operations."""
    kind = _kind_for_path(path)
    if kind:
        body["apiVersion"] = f"{API_GROUP}/{VERSION}"
        body["kind"] = kind
        if not isinstance(body.get("metadata"), dict):
            body["metadata"] = {}
        if method == "POST" and "name" not in body["metadata"]:
            body["metadata"]["name"] = f"st-{uuid.uuid4().hex[:8]}"


@register_body_mutator(order=-15)
def _strip_resource_version(body, **_):
    """Strip resourceVersion — server-managed, random strings cause 400s."""
    metadata = body.get("metadata")
    if isinstance(metadata, dict):
        metadata.pop("resourceVersion", None)


@register_body_mutator(method="POST", order=-10)
def _strip_status_on_create(body, **_):
    """Strip status from POST bodies (server-managed)."""
    body.pop("status", None)


@register_body_mutator(method="PUT", order=-10)
def _strip_status_on_non_status_put(body, path, **_):
    """Strip status from PUT on non-/status endpoints (status is immutable)."""
    if not path.endswith("/status"):
        body.pop("status", None)


@register_body_mutator(method="PUT", order=-9)
def _ensure_status_conditions(body, path, **_):
    """Ensure status.conditions exists for PUT /status bodies."""
    if not path.endswith("/status"):
        return
    status = body.get("status")
    if isinstance(status, dict) and "conditions" not in status:
        status["conditions"] = []


# ===========================================================================
# Single map_body hook — dispatches to all registered mutators
# ===========================================================================
@schemathesis.hook("map_body").apply_to(method=["POST", "PUT"])
def mutate_body(ctx, body):
    if not isinstance(body, dict):
        return body

    path = ctx.operation.path
    method = ctx.operation.method.upper()

    for fn, path_regex, methods, _order in _BODY_MUTATORS:
        if path_regex and not re.search(path_regex, path):
            continue
        if methods and method not in methods:
            continue
        fn(body, path=path, method=method)

    return body


# ===========================================================================
# map_query hook - fixes up query parameters that cause 400s
# ===========================================================================
_VALID_SELECTOR_RE = re.compile(r'^[a-zA-Z][a-zA-Z0-9._-]*[!=]=.+$')


@schemathesis.hook("map_query").apply_to(method="GET")
def mutate_query(ctx, query):
    if not query:
        return query
    query.pop("continue", None)
    for key in ("fieldSelector", "labelSelector"):
        val = query.get(key)
        if val is not None and (not val or not _VALID_SELECTOR_RE.match(val)):
            query.pop(key, None)
    return query


# ===========================================================================
# before_call hooks — run in ALL phases (including Examples)
# ===========================================================================

_RFC1123_RE = re.compile(r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$')


# --- POST: ensure protocol + service fields are set (covers Examples phase)
@schemathesis.hook("before_call").apply_to(method="POST")
def fix_post_body(ctx, case, **kwargs):
    if not case.body or not isinstance(case.body, dict):
        return
    path = ctx.operation.path
    kind = _kind_for_path(path)
    if not kind:
        return
    for fn, path_regex, methods, _order in _BODY_MUTATORS:
        if path_regex and not re.search(path_regex, path):
            continue
        if methods and "POST" not in methods:
            continue
        fn(case.body, path=path, method="POST")


# --- PUT: sync metadata.name from path {name} ----------------------------
@schemathesis.hook("before_call").apply_to(method="PUT")
def sync_put_name(ctx, case, **kwargs):
    if not case.body or not isinstance(case.body, dict):
        return
    path_name = (case.path_parameters or {}).get("name")
    if path_name:
        path_name = unquote(path_name)
        if not isinstance(case.body.get("metadata"), dict):
            case.body["metadata"] = {}
        case.body["metadata"]["name"] = path_name
