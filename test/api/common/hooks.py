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
from copy import deepcopy
from urllib.parse import unquote

import schemathesis
import tracecov
import urllib3

urllib3.disable_warnings(urllib3.exceptions.InsecureRequestWarning)

tracecov.schemathesis.install()

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
    """Walk a JSON schema tree, calling visitor(schema) at each node.

    Traverses standard JSON Schema keywords only.  Does not follow $ref
    strings or Schemathesis-internal storage — callers that need every
    reachable definition should iterate the raw ``components/schemas``
    section instead (see ``before_load_schema``).
    """
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


# RFC 6901 section 4: an array index is "0" or a decimal with no
# leading zero, ASCII digits only.
_ARRAY_INDEX_RE = re.compile(r"0|[1-9][0-9]*")


def _resolve_local_pointer(root, ref):
    """Resolve a document-local ``$ref`` against *root* using RFC 6901 rules.

    Only same-document references (``#/...``) are followed.  The fragment is
    percent-decoded as a whole first (RFC 6901 section 6), then split on
    ``/``, then each token is unescaped ``~1`` -> ``/`` before ``~0`` -> ``~``
    (section 4) — doing it in that order is what keeps ``~01`` meaning a
    literal ``~1`` rather than ``/``.  Resolution is purely positional, so no
    container name anywhere along the pointer is assumed or searched for.

    Returns ``None`` when the pointer does not resolve — an unknown or
    external target is left untouched rather than treated as an empty
    schema, so a missing reference can never silently satisfy a constraint.
    """
    if not isinstance(ref, str) or not ref.startswith("#/"):
        return None
    node = root
    for raw_token in unquote(ref[2:]).split("/"):
        token = raw_token.replace("~1", "/").replace("~0", "~")
        if isinstance(node, list):
            # RFC 6901 array indices are "0" or have no leading zero;
            # str.isdigit() would also accept "01" and non-ASCII digits.
            if not _ARRAY_INDEX_RE.fullmatch(token) or int(token) >= len(node):
                return None
            node = node[int(token)]
        elif isinstance(node, dict):
            if token not in node:
                return None
            node = node[token]
        else:
            return None
    return node


def _walk_resolved_schema(schema, root, visitor, seen=None):
    """Walk a schema tree like :func:`_walk_schema`, but follow local ``$ref``.

    Traverses the same standard JSON Schema keywords — so ``example``,
    ``default`` and vendor extensions are still never entered — and
    additionally follows document-local ``$ref`` pointers, which is how a
    definition the document reaches only through a reference becomes
    visible.  ``seen`` tracks node identity, so reference cycles and shared
    targets terminate and are visited exactly once.
    """
    if not isinstance(schema, dict):
        return
    if seen is None:
        seen = set()
    if id(schema) in seen:
        return
    seen.add(id(schema))
    visitor(schema)
    target = _resolve_local_pointer(root, schema.get("$ref"))
    if isinstance(target, dict):
        _walk_resolved_schema(target, root, visitor, seen)
    for key in ("items", "additionalProperties", "not"):
        if key in schema:
            _walk_resolved_schema(schema[key], root, visitor, seen)
    for key in ("properties", "patternProperties"):
        if key in schema and isinstance(schema[key], dict):
            for prop_schema in schema[key].values():
                _walk_resolved_schema(prop_schema, root, visitor, seen)
    for key in ("allOf", "anyOf", "oneOf", "prefixItems"):
        if key in schema and isinstance(schema[key], list):
            for subschema in schema[key]:
                _walk_resolved_schema(subschema, root, visitor, seen)


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
    """Find schema keys referenced by allOf/anyOf/oneOf via $ref.

    Walks nested properties, items, and composition branches so that
    schemas referenced from *any* depth are detected — not only those
    composed at the top level of another schema definition.
    """
    refs = set()

    def _collect(node):
        if not isinstance(node, dict):
            return
        for key in ("allOf", "anyOf", "oneOf"):
            for branch in node.get(key, []):
                if isinstance(branch, dict):
                    if "$ref" in branch:
                        ref = branch["$ref"]
                        if ref.startswith(prefix + "/"):
                            refs.add(ref[len(prefix) + 1:])
                    _collect(branch)
        for key in ("properties", "patternProperties"):
            if key in node and isinstance(node[key], dict):
                for prop_schema in node[key].values():
                    _collect(prop_schema)
        for key in ("items", "additionalProperties", "not"):
            if key in node and isinstance(node[key], dict):
                _collect(node[key])

    for s in schemas.values():
        if isinstance(s, dict):
            _collect(s)
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


def _value_constraints_visitor(schema):
    """Apply the three value-level constraint families to one schema node."""
    _integer_limits_visitor(schema)
    _label_constraints_visitor(schema)
    _name_pattern_visitor(schema)


def apply_imported_body_constraints(operation):
    """Apply value constraints to request-body definitions after ref resolution.

    ``before_load_schema`` sees the root document, and a definition the
    document imports from *another file* is not in it: it never appears in
    ``components/schemas``, so a pass over that section cannot reach it.
    The real specs rely on such imports — ``api/core/v1alpha1`` and
    ``api/imagebuilder/v1alpha1`` both take ``ObjectMeta`` from the core
    v1beta1 specification, each by its own relative path — which left the
    imported ``name``, ``labels`` and integer fields unconstrained in
    generated request bodies.

    By the time an operation is initialised the loader has resolved those
    imports into the operation's own request-body definition, where they are
    reachable through ordinary document-local ``$ref`` pointers.  Walking
    from the body definition therefore reaches them using standard JSON
    Schema and RFC 6901 semantics only, through the public ``operation.body``
    accessor, naming no loader-internal storage.  This is the route that was
    verified to work; it is not a claim that no other public wiring could
    reach these definitions.

    Scope is deliberately narrow:

    * Only the three value-level families are applied (integer ranges, label
      patterns, name pattern).  Each uses ``setdefault``, so re-application
      is a no-op and anything already constrained in the root document keeps
      its own value — repeated loads stay consistent.
    * ``additionalProperties`` is NOT applied here.  Tightening it once
      composition has been resolved can make ``allOf``/``oneOf`` branches
      unsatisfiable, so the existing composition policy is left untouched.
      ``disallow_extra_properties`` therefore keeps exactly the reach it has
      today; changing that needs its own satisfiability work.
    * Only resolved request bodies are traversed; response schemas are not,
      and no change to response validation is introduced here.  Checked on
      one fixture where a single external definition is referenced by both
      a request body and a response: after the body is constrained, a
      response that the added constraints would reject still passes
      ``response_schema_conformance``, while a response that breaks the
      original schema still fails.  That is a single case, not a general
      guarantee about how the loader shares or copies resolved nodes.
    """
    for body in operation.body:
        schema = body.definition.get("schema")
        if isinstance(schema, dict):
            # The body definition is its own reference root: the loader
            # rewrites imports into pointers relative to this node.
            _walk_resolved_schema(schema, schema, _value_constraints_visitor)


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


def _collect_schema_refs(schema):
    """Return component schema names referenced by ``$ref`` inside *schema*.

    Uses ``_walk_schema`` so only standard JSON Schema keywords are
    traversed — ``example``, ``default``, and extension fields are
    never entered.
    """
    prefix = "#/components/schemas/"
    found = set()

    def _visitor(node):
        ref = node.get("$ref")
        if isinstance(ref, str) and ref.startswith(prefix):
            found.add(ref[len(prefix):])

    _walk_schema(schema, _visitor)
    return found


def _request_body_schemas(raw_schema):
    """Return inline request-body schema nodes and reachable component names.

    Walks operation ``requestBody`` entries in ``paths`` and follows
    ``$ref: #/components/requestBodies/…`` to reusable bodies that are
    actually used by an operation.  Unused reusable requestBodies are
    **not** scanned, so their referenced schemas are not marked
    request-reachable and do not receive ``additionalProperties``
    tightening.  (Integer limits, label patterns, and name patterns
    still apply to all component schemas regardless.)

    Returns ``(inline_schemas, component_names)`` where *inline_schemas*
    is a list of dict schema nodes that are NOT ``$ref``-only (they need
    direct patching) and *component_names* is the set of
    ``components/schemas`` keys transitively reachable from any request
    body.
    """
    schemas = raw_schema.get("components", {}).get("schemas", {})
    reusable_bodies = raw_schema.get("components", {}).get("requestBodies", {})
    inline = []   # schema dicts that need direct patching
    pending = set()

    def _process_content(content):
        """Walk media-type entries inside a requestBody ``content`` map."""
        if not isinstance(content, dict):
            return
        for media_schema_wrapper in content.values():
            if not isinstance(media_schema_wrapper, dict):
                continue
            s = media_schema_wrapper.get("schema")
            if not isinstance(s, dict):
                continue
            if "$ref" in s and len(s) == 1:
                # Pure ref — handled via component patching.
                ref = s["$ref"]
                prefix = "#/components/schemas/"
                if ref.startswith(prefix):
                    pending.add(ref[len(prefix):])
            else:
                # Inline schema — needs direct constraint application.
                inline.append(s)
                pending.update(_collect_schema_refs(s))

    def _resolve_request_body(rb):
        """Process a requestBody, following $ref to components/requestBodies."""
        if not isinstance(rb, dict):
            return
        ref = rb.get("$ref")
        if isinstance(ref, str):
            prefix = "#/components/requestBodies/"
            if ref.startswith(prefix):
                name = ref[len(prefix):]
                if name not in visited_bodies:
                    visited_bodies.add(name)
                    target = reusable_bodies.get(name)
                    if isinstance(target, dict):
                        # Recurse: target may be another $ref (alias chain)
                        # or a concrete body with content (handled below).
                        _resolve_request_body(target)
            return
        # Concrete requestBody (inline or resolved reusable) — process content.
        _process_content(rb.get("content"))

    visited_bodies = set()  # tracks visited components/requestBodies names

    # Walk requestBody in each path operation.
    for path_item in raw_schema.get("paths", {}).values():
        if not isinstance(path_item, dict):
            continue
        for op in path_item.values():
            if not isinstance(op, dict):
                continue
            rb = op.get("requestBody")
            if rb is not None:
                _resolve_request_body(rb)

    # Transitively follow $refs inside collected component schemas.
    visited = set()
    while pending - visited:
        name = (pending - visited).pop()
        visited.add(name)           # always mark — even absent names
        target = schemas.get(name)
        if isinstance(target, dict):
            pending.update(_collect_schema_refs(target))

    return inline, visited


def _apply_all_constraints(schema, *, inside_composition=False):
    """Apply all four schema constraints to a single schema node."""
    apply_integer_limits(schema)
    apply_label_constraints(schema)
    apply_name_pattern(schema)
    disallow_extra_properties(schema, _inside_composition=inside_composition)


@schemathesis.hook
def before_load_schema(context, raw_schema):
    """Apply schema-wide constraints to the raw OpenAPI document.

    Uses the public ``before_load_schema`` hook so that constraints are
    applied to the raw OpenAPI document before the loader resolves it.

    Integer limits, label patterns, and name patterns apply to **all**
    component schemas — these constraints are semantically valid in both
    request and response contexts.

    ``disallow_extra_properties`` is scoped to component schemas
    transitively reachable from request bodies **and** to inline
    request-body schemas, matching the previous per-operation
    body-patching scope.  Response-only schemas are not constrained,
    avoiding unintended response-validation strictness.

    Only the root document is addressable here.  A definition the document
    imports from another file is not in ``components/schemas``, so it is
    out of reach of this pass; the three value-level families are applied
    to those in :func:`apply_imported_body_constraints`, from the resolved
    request body.
    """
    schemas = raw_schema.get("components", {}).get("schemas", {})
    inline_bodies, request_names = _request_body_schemas(raw_schema)

    # --- Inline request-body schemas (not $ref to components). ---
    # Collect composition targets from both component and inline roots.
    all_schema_roots = dict(schemas)
    for i, s in enumerate(inline_bodies):
        all_schema_roots[f"__inline_{i}"] = s
    composition_targets = _find_composition_refs(
        all_schema_roots, "#/components/schemas",
    )
    for body_schema in inline_bodies:
        _apply_all_constraints(body_schema)

    # --- Component schemas. ---
    for name, schema_def in schemas.items():
        if not isinstance(schema_def, dict):
            continue
        apply_integer_limits(schema_def)
        apply_label_constraints(schema_def)
        apply_name_pattern(schema_def)
        if name in request_names:
            disallow_extra_properties(
                schema_def, _inside_composition=name in composition_targets,
            )


@schemathesis.hook
def before_init_operation(context, operation):
    # Parameter-level constraints (simple schemas, no bundled refs).
    for parameter in operation.iter_parameters():
        schema = parameter.definition.get("schema")
        if schema is not None:
            apply_integer_limits(schema)
        if (parameter.definition.get("in") == "path"
                and parameter.definition.get("name") in _RESOURCE_NAME_PATH_PARAMS
                and isinstance(schema, dict)
                and schema.get("type") == "string"):
            schema.setdefault("pattern", _RFC1123_RE.pattern)
    # Body-schema constraints are applied in before_load_schema on the raw
    # OpenAPI components.  Definitions imported from another file are not in
    # that section, so the value-level families are applied here as well,
    # where the loader has resolved those imports into the request body.
    apply_imported_body_constraints(operation)
    _require_status_for_status_endpoints(operation)


# ---------------------------------------------------------------------------
# Shared helpers
# ---------------------------------------------------------------------------
def _kind_for_path(path):
    """Return the resource kind for collection, item, and status resource paths."""
    for prefix in sorted(_PATH_TO_KIND, key=len, reverse=True):
        if path == prefix:
            return _PATH_TO_KIND[prefix]
        if not path.startswith(prefix + "/"):
            continue
        suffix = path[len(prefix) + 1 :]
        if suffix.startswith("{") and suffix.endswith("}") and "/" not in suffix:
            return _PATH_TO_KIND[prefix]
        if suffix == "{name}/status":
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

    # Linked bodies can share nested objects with a cached response used by later links.
    body = deepcopy(body)
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


@schemathesis.hook("before_call").apply_to(method=["POST", "PUT"])
def isolate_request_body(ctx, case, **kwargs):
    # Stateful links can merge response objects after map_body. Detach them
    # before any of the following hooks modify metadata or spec in place.
    if isinstance(case.body, dict):
        case.body = deepcopy(case.body)


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
    if not _kind_for_path(ctx.operation.path):
        return
    path_name = (case.path_parameters or {}).get("name")
    if path_name:
        path_name = unquote(path_name)
        if not isinstance(case.body.get("metadata"), dict):
            case.body["metadata"] = {}
        case.body["metadata"]["name"] = path_name
