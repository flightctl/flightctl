"""Core API service hooks.

Core-specific schemathesis hooks for enrollment requests, certificate signing
requests, auth providers, device actions, and approval endpoints.
"""

import base64
import re
import subprocess
from datetime import datetime, timezone
from pathlib import Path

import schemathesis

import common  # noqa: F401 -- triggers common hooks + checks registration
from common.hooks import register_body_mutator, register_path_kinds

# ---------------------------------------------------------------------------
# Path-to-kind registration for core API resources
# ---------------------------------------------------------------------------
register_path_kinds({
    "/catalogs/{catalog}/items": "CatalogItem",
    "/catalogs": "Catalog",
    "/devices": "Device",
    "/fleets": "Fleet",
    "/repositories": "Repository",
    "/resourcesyncs": "ResourceSync",
    "/enrollmentrequests": "EnrollmentRequest",
    "/certificatesigningrequests": "CertificateSigningRequest",
    "/authproviders": "AuthProvider",
})


# ---------------------------------------------------------------------------
# Constants
# ---------------------------------------------------------------------------
KEY_PATH = Path("/app/certs/client-enrollment.key")
OPENSSL_TIMEOUT_SECONDS = 15

_VALID_CSR_SIGNERS = [
    "flightctl.io/enrollment",
    "flightctl.io/device-management-renewal",
    "flightctl.io/device-svc-client",
    "flightctl.io/server-svc",
]

_VALID_EXTERNAL_ROLES = [
    "flightctl-admin", "flightctl-org-admin", "flightctl-operator",
    "flightctl-viewer", "flightctl-installer",
]


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------
def _ensure_nonempty(d, key, default):
    if not d.get(key):
        d[key] = default


def _utcnow_iso():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


# ---------------------------------------------------------------------------
# CSR generation
# ---------------------------------------------------------------------------
_csr_cache = {}


def _run_openssl(command, *, input_bytes=None):
    try:
        return subprocess.run(
            command,
            input=input_bytes,
            capture_output=True,
            check=True,
            timeout=OPENSSL_TIMEOUT_SECONDS,
        )
    except FileNotFoundError as exc:
        raise RuntimeError("openssl is required for CSR generation") from exc
    except subprocess.TimeoutExpired as exc:
        raise RuntimeError(f"openssl command timed out: {' '.join(command)}") from exc
    except subprocess.CalledProcessError as exc:
        stderr = exc.stderr.decode("utf-8", errors="replace").strip()
        raise RuntimeError(f"openssl command failed: {' '.join(command)}: {stderr}") from exc


def _generate_csr_bytes():
    if not KEY_PATH.is_file():
        return None
    create_result = _run_openssl([
        "openssl", "req", "-new", "-batch", "-sha256",
        "-key", str(KEY_PATH),
        "-subj", "/CN=schemathesis-test",
    ])
    csr_bytes = create_result.stdout
    if not csr_bytes:
        raise RuntimeError("openssl returned empty CSR output")
    _run_openssl(
        ["openssl", "req", "-verify", "-noout", "-in", "/dev/stdin"],
        input_bytes=csr_bytes,
    )
    return csr_bytes


def _csr_pem():
    if "pem" not in _csr_cache:
        csr_bytes = _generate_csr_bytes()
        if csr_bytes is None:
            return ""
        _csr_cache["pem"] = csr_bytes.decode("utf-8")
    return _csr_cache["pem"]


def _csr_base64():
    if "b64" not in _csr_cache:
        csr_bytes = _generate_csr_bytes()
        if csr_bytes is None:
            return ""
        _csr_cache["b64"] = base64.b64encode(csr_bytes).decode("ascii")
    return _csr_cache["b64"]


# ---------------------------------------------------------------------------
# AuthProvider helpers
# ---------------------------------------------------------------------------
_DEFAULT_ORG_ASSIGNMENT = {
    "type": "static", "organizationName": "default",
}
_DEFAULT_ROLE_ASSIGNMENT = {
    "type": "static", "roles": ["flightctl-admin"],
}


def _fix_assignment(spec):
    org = spec.get("organizationAssignment")
    if isinstance(org, dict):
        if org.get("type") == "static":
            _ensure_nonempty(org, "organizationName", "default")
        elif org.get("type") == "dynamic":
            org.setdefault("claimPath", ["groups"])
    role = spec.get("roleAssignment")
    if isinstance(role, dict):
        if role.get("type") == "static":
            roles = role.get("roles")
            if not roles or not isinstance(roles, list):
                role["roles"] = ["flightctl-admin"]
            else:
                role["roles"] = [
                    r if r in _VALID_EXTERNAL_ROLES else "flightctl-admin"
                    for r in roles
                ]
        elif role.get("type") == "dynamic":
            role.setdefault("claimPath", ["roles"])


def _fill_authprovider_spec(spec):
    if not isinstance(spec, dict):
        return
    provider_type = spec.get("providerType")
    if provider_type == "oauth2":
        _ensure_nonempty(spec, "authorizationUrl", "https://example.com/auth")
        _ensure_nonempty(spec, "tokenUrl", "https://example.com/token")
        _ensure_nonempty(spec, "userinfoUrl", "https://example.com/userinfo")
    if provider_type in ("oidc", "oauth2"):
        if provider_type == "oidc":
            _ensure_nonempty(spec, "issuer", "https://example.com")
        _ensure_nonempty(spec, "clientId", "st-test")
        _ensure_nonempty(spec, "clientSecret", "st-secret")
        spec.setdefault("organizationAssignment", _DEFAULT_ORG_ASSIGNMENT)
        spec.setdefault("roleAssignment", _DEFAULT_ROLE_ASSIGNMENT)
        _fix_assignment(spec)


# ---------------------------------------------------------------------------
# CatalogItem helpers
# ---------------------------------------------------------------------------
_TYPE_TO_CATEGORY = {
    "os": "system", "firmware": "system", "driver": "system",
    "container": "application", "helm": "application",
    "quadlet": "application", "compose": "application", "data": "application",
}

_REPO_KNOWN_PROPS = {
    "git": {"type", "url", "httpConfig", "sshConfig"},
    "http": {"type", "url", "httpConfig", "validationSuffix"},
    "oci": {"type", "registry", "scheme", "accessMode", "ociAuth",
            "ca.crt", "skipServerVerification"},
}


def _is_valid_uri(uri):
    return isinstance(uri, str) and len(uri) >= 3 and ("/" in uri or "." in uri)


# ===========================================================================
# filter_body hooks
# ===========================================================================

@schemathesis.hook("filter_body").apply_to(
    method="POST",
    path_regex=r'/(deviceactions/resume|auth/[^/]+/token)$',
)
def filter_action_bodies_with_status(ctx, body):
    if isinstance(body, dict) and "status" in body:
        return False
    return True


# ===========================================================================
# Body mutators — registered with the single map_body hook in common/hooks.py
# Scope (path/method) is declared in the decorator.
# Signature: fn(body, *, path, method, **_)
# ===========================================================================

@register_body_mutator(path=r'/enrollmentrequests')
def _fix_enrollment_csr(body, **_):
    if not isinstance(body.get("spec"), dict):
        body["spec"] = {}
    body["spec"]["csr"] = _csr_pem()


@register_body_mutator(path=r'/certificatesigningrequests')
def _fix_csr_values(body, **_):
    spec = body.get("spec")
    if isinstance(spec, dict):
        if "request" in spec:
            spec["request"] = _csr_base64()
        if "signerName" in spec and spec["signerName"] not in _VALID_CSR_SIGNERS:
            spec["signerName"] = _VALID_CSR_SIGNERS[0]


@register_body_mutator(path=r'/authproviders')
def _fix_authprovider(body, **_):
    spec = body.get("spec")
    if isinstance(spec, dict) and spec.get("providerType") not in ("oidc", "oauth2"):
        spec["providerType"] = "oidc"
    _fill_authprovider_spec(spec)


@register_body_mutator(path=r'/repositories')
def _fix_repository_spec(body, **_):
    spec = body.get("spec")
    if isinstance(spec, dict):
        repo_type = spec.get("type")
        known = _REPO_KNOWN_PROPS.get(repo_type)
        if known:
            for key in list(spec.keys()):
                if key not in known:
                    del spec[key]


@register_body_mutator(path=r'/resourcesyncs')
def _fix_resourcesync_spec(body, **_):
    spec = body.get("spec")
    if not isinstance(spec, dict):
        return
    repo = spec.get("repository")
    if not repo or not re.match(r'^[a-z0-9]([-a-z0-9]*[a-z0-9])?$', str(repo)):
        spec["repository"] = "default"
    rev = spec.get("targetRevision")
    if not rev or not re.match(r'^[a-zA-Z0-9][a-zA-Z0-9.\-_/]*$', str(rev)):
        spec["targetRevision"] = "main"


@register_body_mutator(path=r'/devices', method="POST")
def _fix_device_create(body, **_):
    spec = body.get("spec")
    if isinstance(spec, dict):
        spec.pop("decommissioning", None)


@register_body_mutator(path=r'/deviceactions/resume$', method="POST")
def _fix_resume_request(body, **_):
    for key in list(body.keys()):
        if key not in ("labelSelector", "fieldSelector"):
            del body[key]


@register_body_mutator(path=r'/approval$', method="PUT")
def _fix_approval(body, **_):
    approved_condition = {
        "type": "Approved",
        "status": "True",
        "lastTransitionTime": _utcnow_iso(),
        "reason": "Approved",
        "message": "Approved by schemathesis test",
    }
    status = body.get("status")
    if not isinstance(status, dict):
        body["status"] = {"conditions": [approved_condition]}
    elif not status.get("conditions"):
        status["conditions"] = [approved_condition]


@register_body_mutator(path=r'/catalogs/[^/]+/items')
def _fix_catalog_item(body, **_):
    spec = body.get("spec")
    if not isinstance(spec, dict):
        return

    item_type = spec.get("type")
    if item_type not in _TYPE_TO_CATEGORY:
        item_type = "container"
        spec["type"] = item_type
    spec["category"] = _TYPE_TO_CATEGORY.get(item_type, "application")

    artifacts = spec.get("artifacts")
    if not isinstance(artifacts, list) or not artifacts:
        spec["artifacts"] = [{"type": "container", "uri": "quay.io/schemathesis/test"}]
    else:
        for art in artifacts:
            if isinstance(art, dict) and not _is_valid_uri(art.get("uri")):
                art["uri"] = "quay.io/schemathesis/test"

    art_types = [a["type"] for a in spec["artifacts"] if isinstance(a, dict) and a.get("type")]
    ref_type = art_types[0] if art_types else "container"

    versions = spec.get("versions")
    if not isinstance(versions, list) or not versions:
        versions = [{"version": "1.0.0"}]
        spec["versions"] = versions
    for i, v in enumerate(versions):
        if isinstance(v, dict):
            v["references"] = {ref_type: f"v1.{i}.0"}
            v.pop("tag", None)
            v.pop("digest", None)

    spec.pop("reference", None)


# ===========================================================================
# before_call hooks — only for things that need path parameters
# ===========================================================================

@schemathesis.hook("before_call").apply_to(
    method=["POST", "PUT"],
    path_regex=r'/catalogs/[^/]+/items',
)
def sync_catalog_item_catalog(ctx, case, **kwargs):
    """Set metadata.catalog from the URI path parameter."""
    if not case.body or not isinstance(case.body, dict):
        return
    catalog = (case.path_parameters or {}).get("catalog")
    if catalog:
        if not isinstance(case.body.get("metadata"), dict):
            case.body["metadata"] = {}
        case.body["metadata"]["catalog"] = catalog
