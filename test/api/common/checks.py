"""Response validation checks for flightctl API testing.

These @schemathesis.check decorators run on every response and validate
version negotiation headers and body apiVersion fields.
"""

import schemathesis

from common.hooks import VERSION, VERSION_HEADER, API_GROUP


# ---------------------------------------------------------------------------
# Response header helper
# ---------------------------------------------------------------------------
def _get_header(response, name):
    """Get a single header value from schemathesis Response.

    Schemathesis v4 normalizes headers: keys are lowercase, values are list[str].
    """
    values = response.headers.get(name.lower())
    if values:
        return values[0]
    return None


# ---------------------------------------------------------------------------
# Passive version checks (run on every response)
# ---------------------------------------------------------------------------
@schemathesis.check
def check_version_header_echo(ctx, response, case):
    """Response header Flightctl-API-Version must echo the requested version."""
    if response.status_code >= 400 or not VERSION:
        return

    value = _get_header(response, VERSION_HEADER)
    if value is None:
        raise AssertionError(
            f"Missing {VERSION_HEADER} response header"
        )
    if value.strip() != VERSION:
        raise AssertionError(
            f"{VERSION_HEADER} header is '{value.strip()}', expected '{VERSION}'"
        )


@schemathesis.check
def check_body_api_version(ctx, response, case):
    """Body apiVersion must match the requested version (qualified or unqualified)."""
    if response.status_code >= 400 or not VERSION:
        return

    content_type = _get_header(response, "Content-Type") or ""
    if "json" not in content_type:
        return

    try:
        parsed = response.json()
    except (ValueError, AttributeError):
        return

    api_version = None
    if isinstance(parsed, dict):
        api_version = parsed.get("apiVersion")
    elif isinstance(parsed, list) and parsed and isinstance(parsed[0], dict):
        api_version = parsed[0].get("apiVersion")

    if api_version is None:
        return

    accepted = {VERSION, f"{API_GROUP}/{VERSION}"}
    if api_version not in accepted:
        raise AssertionError(
            f"body apiVersion is '{api_version}', expected one of {sorted(accepted)}"
        )


@schemathesis.check
def check_vary_header(ctx, response, case):
    """Vary header must include Flightctl-API-Version."""
    if response.status_code >= 400:
        return

    values = response.headers.get("vary") or []
    vary = ", ".join(values)
    if VERSION_HEADER.lower() not in vary.lower():
        raise AssertionError(
            f"Vary header missing '{VERSION_HEADER}', got '{vary}'"
        )
