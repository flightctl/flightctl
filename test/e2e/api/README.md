# RESTler API Versioning Tests

This suite runs [RESTler](https://github.com/microsoft/restler-fuzzer) against the APIs defined in our service OpenAPI specs to verify version negotiation behavior and OpenAPI-based response validation. When new endpoints are added to those specs, they are automatically included in RESTler grammar generation and coverage reporting on the next run.

## How the test run works

- `make restler-test` builds the RESTler image (if needed), prepares fixtures, and runs `test/e2e/api/run_restler_tests.sh`.
- The runner discovers test targets from `test/e2e/api/<service>/dict_<version>.json`.
- For each `service/version`, RESTler compiles grammar from the configured OpenAPI spec (`SwaggerSpecFilePath`) and executes it in `test` mode.
- Coverage is automatic and spec-based: `testing_summary.json` reports what part of the generated grammar/spec was exercised.

RESTler compiler configs enable `UseBodyExamples` and `UseQueryExamples`, so OpenAPI `example` values are used during request generation.

## Versioning checks

`version_checker.py` runs on successful requests and validates:

1. `Flightctl-API-Version` in the response matches the requested version.
2. `apiVersion` in the body matches the expected version (`vX` or `flightctl.io/vX`).
3. `Vary` includes `Flightctl-API-Version`.
4. Replayed requests with an invalid version (`v999`) are rejected with `406`.
5. Replayed requests without a version header still return `Flightctl-API-Version`.

## Prerequisites

- Running flightctl deployment (`make deploy`)
- `~/.config/flightctl/client.yaml`
- Podman

## Run

```bash
make restler-test
```

## Extend coverage

- **Add a version**: add `dict_<version>.json` and `compiler_config_<version>.json` under the service directory (optional `annotations_<version>.json`). Versions are auto-discovered from `dict_*.json`.
- **Add a service**: add a new service directory with the same files, then update service URL mapping in `run_restler_tests.sh`.

## Results

Outputs are written to `reports/restler/{service}/{version}/`:

- `testing_summary.json`
- `bug_buckets.txt`
- `VersionChecker*.txt`

## Cleanup

```bash
make clean-restler
```
