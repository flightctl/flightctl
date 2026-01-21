# API Versioning

This document describes the API versioning architecture in Flight Control.

## Version Negotiation

Flight Control uses HTTP header-based versioning rather than URL-based versioning. Clients specify their desired API version using the `Flightctl-API-Version` header, and the server negotiates the appropriate version based on endpoint support.

API versions are per-resource (e.g., Device, Fleet, Repository). Each resource can have different supported versions, allowing resources to evolve independently.

### Headers

| Header | Direction | Purpose |
|--------|-----------|---------|
| `Flightctl-API-Version` | Request | Client specifies desired version |
| `Flightctl-API-Version` | Response | Server returns negotiated version |
| `Flightctl-API-Versions-Supported` | Response (406 only) | Lists supported versions on error |
| `Deprecation` | Response | [RFC 9745](https://www.rfc-editor.org/rfc/rfc9745.html) header with [RFC 9651](https://www.rfc-editor.org/rfc/rfc9651.html) date format `@<epoch-seconds>` indicating when the version was or will be deprecated |
| `Vary` | Response | Set to `Flightctl-API-Version` for cache differentiation |

### Negotiation Flow

1. Client sends request with optional `Flightctl-API-Version` header
1. Server determines negotiated version based on endpoint metadata derived from OpenAPI specs (`api/{group}/{version}/openapi.yaml`):
   - If version requested and supported: use requested version
   - If version requested but not supported: return HTTP 406 Not Acceptable
   - If no version requested: use first (most preferred) version from metadata
1. For versioned resources, server responds with `Flightctl-API-Version` header indicating negotiated version

### Example Request

```bash
# Request specific version
curl -H "Flightctl-API-Version: v1beta1" \
     https://api.flightctl.example.com/api/v1/devices

# Response headers include:
# Flightctl-API-Version: v1beta1
# Vary: Flightctl-API-Version
```

### Error Response (406 Not Acceptable)

```bash
# Request unsupported version
curl -H "Flightctl-API-Version: v2" \
     https://api.flightctl.example.com/api/v1/devices

# Response headers include:
# Flightctl-API-Versions-Supported: v1beta1
# HTTP/1.1 406 Not Acceptable
```

## Version Support Levels

| Level | Description | Support Guarantee |
|-------|-------------|-------------------|
| `v1alphaX` | Alpha versions | TBD |
| `v1betaX` | Beta versions | Supported throughout the 1.x.x major version |
| `v1` | Stable version | TBD |

## Architecture

```text
Request with Flightctl-API-Version header
                    |
                    v
        +------------------------+
        |      Negotiator        |
        +------------------------+
                    |
                    v
        +------------------------+
        |      Dispatcher        |
        +------------------------+
               /         \
              v           v
    +-----------+    +-----------+
    | v1beta1   |    | v1        |
    | Router    |    | (future)  |
    +-----------+    +-----------+
         |                |
         v                v
    +-----------+    +-----------+
    | v1beta1   |    | v1        |
    | Transport |    | Transport |
    +-----------+    +-----------+
         |                |
         v                v
    +-----------+    +-----------+
    | v1beta1   |    | v1        |
    | Converter |    | Converter |
    +-----------+    +-----------+
          \              /
           v            v
        +------------------+
        |   Domain Types   |
        +------------------+
```

### Components

* **Negotiator** - Middleware that handles version negotiation and sets response headers
* **Dispatcher** - Routes requests to the appropriate version-specific router
* **Transport Handlers** - Implement the API endpoints for each version, located in `internal/transport/{version}/`
* **Converters** - Translate between versioned API types and internal domain types, located in `internal/api/convert/{version}/`
* **Domain Types** - Internal representation of resources in `internal/domain/`, currently type aliases to API types

## Adding a New API Version

1. **Create OpenAPI spec** - Add a new version directory under `api/{group}/{version}/` with the OpenAPI specification
1. **Add version constant** - Register the new version in `internal/api_server/versioning/version.go`
1. **Generate types** - Run `make generate` to create API types and endpoint metadata
1. **Create transport handlers** - Implement API endpoint handlers in `internal/transport/{version}/`
1. **Create converters** - Add converters in `internal/api/convert/{version}/` to translate between API and domain types
1. **Update domain types** - If the new version has breaking changes, update domain types to support both versions
1. **Register the router** - Wire up the new version router in `internal/api_server/server.go`

