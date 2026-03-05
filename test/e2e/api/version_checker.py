"""Custom RESTler checker that validates API version negotiation.

Validates on every successful response:
  1. Response header Flightctl-API-Version echoes the requested version.
  2. Response body apiVersion field matches (qualified or unqualified).
  3. Vary header includes Flightctl-API-Version.

Probes by sending modified requests:
  4. Replays with an invalid version header and asserts 406.
  5. Replays without a version header and asserts the default version is returned.
"""
from __future__ import print_function
import json
import re

from checkers.checker_base import *
from engine.bug_bucketing import BugBuckets
import engine.core.sequences as sequences
from engine.errors import TimeOutException

VERSION_HEADER = "Flightctl-API-Version"
SUPPORTED_HEADER = "Flightctl-API-Versions-Supported"
API_GROUP = "flightctl.io"
INVALID_VERSION = "v999"


def _get_header(headers_dict, name):
    """Case-insensitive header lookup."""
    target = name.lower()
    for key, value in headers_dict.items():
        if key.lower() == target:
            return value
    return None


class VersionChecker(CheckerBase):
    """Checker that validates API version negotiation headers and body fields."""

    generation_executed_requests = dict()

    def __init__(self, req_collection, fuzzing_requests):
        CheckerBase.__init__(self, req_collection, fuzzing_requests, enabled=True)
        self._expected_version = Settings().get_checker_arg(
            self._friendly_name, "expected_version"
        )
        if not self._expected_version:
            raise ValueError("VersionChecker requires checker arg 'expected_version'")
        self._accepted_body_versions = {
            self._expected_version,
            f"{API_GROUP}/{self._expected_version}",
        }

    def apply(self, rendered_sequence, lock):
        if not rendered_sequence.sequence or not rendered_sequence.valid:
            return

        self._sequence = rendered_sequence.sequence
        last_request = self._sequence.last_request
        generation = self._sequence.length
        response = rendered_sequence.final_request_response

        if not response or not response.status_code or not response.has_valid_code():
            return

        # Deduplicate: one check per endpoint+method per generation
        request_hash = last_request.method_endpoint_hex_definition
        seen = VersionChecker.generation_executed_requests
        if seen.get(generation) is None:
            seen[generation] = set()
        elif request_hash in seen[generation]:
            return
        seen[generation].add(request_hash)

        endpoint = f"{last_request.method} {last_request.endpoint}"
        self._checker_log.checker_print(f"\n[VersionChecker] {endpoint}")

        self._check_response_version_header(response, endpoint)
        self._check_body_api_version(response, endpoint)
        self._check_vary_header(response, endpoint)
        self._check_invalid_version_rejected(last_request, endpoint)
        self._check_no_header_fallback(last_request, endpoint)

    # -- Passive checks (inspect existing response) --

    def _check_response_version_header(self, response, endpoint):
        """Response header must echo the expected version."""
        value = _get_header(response.headers_dict, VERSION_HEADER)
        if value is None:
            self._fail(f"Missing {VERSION_HEADER} response header", response, endpoint)
        elif value.strip() != self._expected_version:
            self._fail(
                f"{VERSION_HEADER} header is '{value.strip()}', "
                f"expected '{self._expected_version}'",
                response, endpoint,
            )
        else:
            self._ok(f"{VERSION_HEADER} header = '{self._expected_version}'")

    def _check_body_api_version(self, response, endpoint):
        """Body apiVersion must be the expected version (qualified or unqualified)."""
        body = response.json_body
        if not body:
            return

        try:
            parsed = json.loads(body) if isinstance(body, str) else body
        except (json.JSONDecodeError, TypeError):
            return

        api_version = None
        if isinstance(parsed, dict):
            api_version = parsed.get("apiVersion")
        elif isinstance(parsed, list) and parsed and isinstance(parsed[0], dict):
            api_version = parsed[0].get("apiVersion")

        if api_version is None:
            return

        if api_version not in self._accepted_body_versions:
            self._fail(
                f"body apiVersion is '{api_version}', "
                f"expected one of {sorted(self._accepted_body_versions)}",
                response, endpoint,
            )
        else:
            self._ok(f"body apiVersion = '{api_version}'")

    def _check_vary_header(self, response, endpoint):
        """Vary header must include Flightctl-API-Version."""
        vary = _get_header(response.headers_dict, "Vary") or ""
        if VERSION_HEADER.lower() not in vary.lower():
            self._fail(f"Vary header missing '{VERSION_HEADER}'", response, endpoint)
        else:
            self._ok(f"Vary header includes '{VERSION_HEADER}'")

    # -- Active checks (send modified requests) --

    def _check_invalid_version_rejected(self, last_request, endpoint):
        """Request with an unsupported version must return 406."""
        try:
            rendered_data = self._render_request(last_request)
            if not rendered_data:
                return

            modified = self._replace_version_header(rendered_data, INVALID_VERSION)
            if modified == rendered_data:
                modified = self._inject_version_header(rendered_data, INVALID_VERSION)

            response = self._send_request(None, modified)
            if not response or not response.status_code:
                return

            if response.status_code != "406":
                self._fail(
                    f"Invalid version '{INVALID_VERSION}' returned "
                    f"{response.status_code} instead of 406",
                    response, endpoint,
                )
            else:
                supported = _get_header(response.headers_dict, SUPPORTED_HEADER)
                suffix = f", supported: {supported.strip()}" if supported else ""
                self._ok(f"Invalid version rejected with 406{suffix}")
        except TimeOutException:
            raise
        except Exception as e:
            self._checker_log.checker_print(f"  ERROR: Invalid version check: {e}")

    def _check_no_header_fallback(self, last_request, endpoint):
        """Request without a version header should still return a version."""
        try:
            rendered_data = self._render_request(last_request)
            if not rendered_data:
                return

            modified = self._remove_version_header(rendered_data)
            response = self._send_request(None, modified)
            if not response or not response.status_code or not response.has_valid_code():
                return

            value = _get_header(response.headers_dict, VERSION_HEADER)
            if value is None:
                self._fail(
                    f"No {VERSION_HEADER} in response when no version requested",
                    response, endpoint,
                )
            else:
                self._ok(f"Default version without header = '{value.strip()}'")
        except TimeOutException:
            raise
        except Exception as e:
            self._checker_log.checker_print(f"  ERROR: No-header fallback check: {e}")

    # -- Helpers --

    def _render_request(self, last_request):
        """Re-render a request to get raw HTTP data."""
        try:
            rendered_data, _, _, _, _ = next(
                last_request.render_iter(
                    self._req_collection.candidate_values_pool,
                    skip=last_request._current_combination_id - 1,
                    preprocessing=False,
                )
            )
            if not Settings().ignore_dependencies:
                seq = sequences.Sequence(last_request)
                rendered_data = seq.resolve_dependencies(rendered_data)
            return rendered_data
        except StopIteration:
            return None

    def _replace_version_header(self, data, version):
        return re.sub(
            rf"({VERSION_HEADER}:\s*)\S+", rf"\g<1>{version}", data,
            flags=re.IGNORECASE,
        )

    def _inject_version_header(self, data, version):
        parts = data.split("\r\n", 1)
        if len(parts) < 2:
            return data
        return f"{parts[0]}\r\n{VERSION_HEADER}: {version}\r\n{parts[1]}"

    def _remove_version_header(self, data):
        return re.sub(
            rf"{VERSION_HEADER}:\s*\S+\r\n", "", data, flags=re.IGNORECASE,
        )

    def _ok(self, message):
        self._checker_log.checker_print(f"  OK: {message}")

    def _fail(self, message, response, endpoint):
        self._checker_log.checker_print(f"  FAIL: {message} for {endpoint}")
        self._checker_log.checker_print(f"  BUG: {message}")
        if self._sequence:
            seq = sequences.Sequence(self._sequence.last_request)
            BugBuckets.Instance().update_bug_buckets(
                seq,
                response.status_code if response else "N/A",
                origin=self.__class__.__name__,
                reproduce=False,
            )
