#!/usr/bin/env python3
"""Generate a markdown report from Schemathesis test results.

Reads structured data sources per suite:
  - schema-coverage.json  (TraceCov: operation counts, coverage percentages)
  - har-*.json            (HAR: actual HTTP response codes including undocumented)
  - junit-*.xml           (JUnit: pass/fail per endpoint, failure messages)
  - junit-probes.xml      (Pytest: custom version/probe test results)

Usage: report.py <results_dir>
"""

import html
import json
import re
import sys
import xml.etree.ElementTree as ET
from collections import defaultdict
from pathlib import Path

SERVICES = [
    "core/v1alpha1",
    "core/v1beta1",
    "imagebuilder/v1alpha1",
]

API_PREFIX = "/api/v1"


def load_coverage(results_dir):
    """Read schema-coverage.json. Returns dict or None."""
    p = results_dir / "schema-coverage.json"
    if not p.exists():
        return None
    with open(p) as f:
        return json.load(f)


def load_har_responses(results_dir, operations):
    """Parse HAR file and return response counts per operation.

    Returns {(method, template_path): {status_code: count}}.
    Coverage JSON omits undocumented status codes (e.g. 500); HAR captures all.
    """
    har_files = sorted(results_dir.glob("har-*.json"))
    if not har_files or not operations:
        return {}

    # Build regex matchers from operation template paths
    matchers = []
    for op in operations:
        parts = op["path"].split("/")
        regex_parts = [
            "[^/]+" if (p.startswith("{") and p.endswith("}")) else re.escape(p)
            for p in parts
        ]
        matchers.append((
            op["method"],
            re.compile("^" + "/".join(regex_parts) + "$"),
            op["path"],
        ))

    with open(har_files[0]) as f:
        har = json.load(f)

    counts = defaultdict(lambda: defaultdict(int))
    for entry in har["log"]["entries"]:
        method = entry["request"]["method"]
        path = re.sub(r"https?://[^/]+", "", entry["request"]["url"]).split("?")[0]
        status = str(entry["response"]["status"])
        for m_method, m_regex, m_template in matchers:
            if method == m_method and m_regex.match(path):
                counts[(method, m_template)][status] += 1
                break

    return counts


def _extract_raw_failure_messages(xml_path):
    """Extract failure message attributes from raw XML, preserving newlines.

    xml.etree.ElementTree normalizes newlines in attribute values to spaces
    per the XML spec. This reads the raw file to preserve the original
    multi-line formatting.
    """
    with open(xml_path) as f:
        content = f.read()
    msgs = []
    for m in re.finditer(
        r'<failure\b[^>]*?\bmessage="(.*?)"', content, re.DOTALL
    ):
        msgs.append(html.unescape(m.group(1)))
    return msgs


def _is_schemathesis_junit(filename):
    """Check if a JUnit XML filename matches the schemathesis timestamp pattern."""
    return bool(re.match(r"junit-\d{8}T\d{6}Z\.xml$", filename))


def load_junit(results_dir):
    """Read schemathesis JUnit XML. Returns {passed, failed, stateful}."""
    passed = set()
    failed = {}
    stateful = []

    for p in sorted(results_dir.rglob("*.xml")):
        if not _is_schemathesis_junit(p.name):
            continue
        try:
            root = ET.parse(p).getroot()
        except ET.ParseError:
            continue
        if root.tag not in ("testsuites", "testsuite"):
            continue

        raw_msgs = _extract_raw_failure_messages(p)
        raw_iter = iter(raw_msgs)

        for tc in root.iter("testcase"):
            name = tc.get("name", "")

            if name == "Stateful tests":
                for f in tc.findall("failure"):
                    msg = next(raw_iter, f.get("message", "") or f.text or "")
                    if msg:
                        stateful.append(msg)
                continue

            parts = name.split()
            ep = f"{parts[0]} {parts[1]}" if len(parts) >= 2 else name

            failure_elems = tc.findall("failure")
            error_elems = tc.findall("error")
            if failure_elems or error_elems:
                msgs = []
                for _ in failure_elems:
                    msgs.append(next(raw_iter, ""))
                for e in error_elems:
                    msgs.append(e.get("message", "") or e.text or "")
                failed[ep] = msgs
            else:
                passed.add(ep)

        break  # only first valid schemathesis JUnit file

    return {
        "passed": passed - set(failed),
        "failed": failed,
        "stateful": stateful,
    }


def load_probes(results_dir):
    """Read junit-probes.xml (and other non-schemathesis JUnit files).

    Returns list of {func, endpoint, status, message} dicts.
    """
    results = []

    for p in sorted(results_dir.rglob("*.xml")):
        if _is_schemathesis_junit(p.name):
            continue
        try:
            root = ET.parse(p).getroot()
        except ET.ParseError:
            continue
        if root.tag not in ("testsuites", "testsuite"):
            continue

        for tc in root.iter("testcase"):
            name = tc.get("name", "")

            # Parse "test_func_name[METHOD /path]"
            m = re.match(r"(.+?)\[(.+)]$", name)
            if m:
                func = m.group(1)
                endpoint = m.group(2)
            else:
                func = name
                endpoint = ""

            failure = tc.find("failure")
            skipped = tc.find("skipped")

            if failure is not None:
                msg = failure.get("message", "") or failure.text or ""
                status = "failed"
            elif skipped is not None:
                msg = skipped.get("message", "") or skipped.text or ""
                # Strip pytest internal path prefixes from skip messages
                msg = re.sub(r".*/site-packages/.+?: ", "", msg)
                status = "skipped"
            else:
                msg = ""
                status = "passed"

            results.append({
                "func": func,
                "endpoint": endpoint,
                "status": status,
                "message": msg,
            })

    return results


def render_endpoint_matrix(w, operations, har_responses, junit):
    """Render the endpoint coverage matrix table."""
    w("### \U0001f4cb Endpoint Coverage\n")

    method_order = {"GET": 0, "POST": 1, "PUT": 2, "PATCH": 3, "DELETE": 4}
    junit_has_data = bool(junit["passed"] or junit["failed"])

    def sort_key(op):
        path = op["path"]
        display = path[len(API_PREFIX):] if path.startswith(API_PREFIX) else path
        return (display, method_order.get(op["method"], 9))

    sorted_ops = sorted(operations, key=sort_key)

    # Build per-operation rows and collect seen response codes
    seen_codes = set()
    rows = []
    for op in sorted_ops:
        method = op["method"]
        path = op["path"]
        display_path = path[len(API_PREFIX):] if path.startswith(API_PREFIX) else path
        hits = op["hits"]

        har_key = (method, path)
        if har_key in har_responses:
            response_hits = dict(har_responses[har_key])
        else:
            response_hits = {r["status"]: r["hits"] for r in op["responses"]}

        has_2xx = any(
            c.startswith("2") and h > 0 for c, h in response_hits.items()
        )

        ep_key = f"{method} {display_path}"
        if ep_key in junit["failed"]:
            junit_status = "failed"
        elif ep_key in junit["passed"]:
            junit_status = "passed"
        else:
            junit_status = None

        if junit_status == "failed":
            icon = "\u274c"
        elif junit_status == "passed" or (not junit_has_data and has_2xx):
            icon = "\u2705"
        elif hits > 0 and not has_2xx:
            icon = "\u26a0\ufe0f"
        elif hits == 0:
            icon = "\u2b1c"
        else:
            icon = "\u2705"

        for code, h in response_hits.items():
            if h > 0:
                seen_codes.add(code)

        rows.append((icon, method, display_path, hits, has_2xx, response_hits))

    codes = sorted(seen_codes)

    # Header row: bold 2xx column names
    code_headers = []
    for c in codes:
        code_headers.append(f"**{c}**" if c.startswith("2") else c)
    w("| | Method | Path | Hits | " + " | ".join(code_headers) + " |")
    w("|---|--------|------|-----:" + "".join("|----:" for _ in codes) + "|")

    for icon, method, display_path, hits, has_2xx, response_hits in rows:
        cells = []
        for c in codes:
            h = response_hits.get(c, 0)
            cells.append(str(h) if h > 0 else "")
        w(f"| {icon} | {method} | `{display_path}` "
          f"| {hits} | " + " | ".join(cells) + " |")

    # Summary line
    tested = sum(1 for _, _, _, hits, _, _ in rows if hits > 0)
    with_2xx = sum(1 for _, _, _, _, has_2xx, _ in rows if has_2xx)
    not_reached = sum(1 for _, _, _, hits, _, _ in rows if hits == 0)
    parts = []
    if tested > 0:
        parts.append(f"\u2705 {with_2xx}/{tested} tested endpoints got 2xx.")
    if not_reached > 0:
        parts.append(f"\u2b1c {not_reached} endpoints not reached.")
    if parts:
        w(f"\n> {' '.join(parts)}\n")


def render_probes(w, probes):
    """Render the pytest probes section."""
    if not probes:
        return

    w("### \U0001f52c Custom Tests\n")

    # Aggregate by function name
    funcs = {}
    for p in probes:
        func = p["func"]
        if func not in funcs:
            funcs[func] = {"passed": 0, "failed": 0, "skipped": 0, "total": 0}
        funcs[func][p["status"]] += 1
        funcs[func]["total"] += 1

    # Summary table
    w("| Test | \u2705 | \u274c | \u23ed\ufe0f | Total |")
    w("|------|-----|-----|-----|-------|")
    total_passed = total_failed = total_skipped = total_total = 0
    for func in sorted(funcs):
        f = funcs[func]
        w(f"| {func} | {f['passed']} | {f['failed']} | {f['skipped']} | {f['total']} |")
        total_passed += f["passed"]
        total_failed += f["failed"]
        total_skipped += f["skipped"]
        total_total += f["total"]
    w(f"| **Total** | **{total_passed}** | **{total_failed}** "
      f"| **{total_skipped}** | **{total_total}** |")
    w("")

    # Failures (open)
    failures = [p for p in probes if p["status"] == "failed"]
    if failures:
        w("<details open>")
        w(f"<summary>\u274c Failures ({len(failures)})</summary>\n")
        for p in failures:
            ep = p["endpoint"]
            msg = p["message"].split("\n")[0]  # first line only
            w(f"- `{p['func']}[{ep}]`")
            w(f"  > {msg}")
        w("\n</details>\n")

    # Skipped (collapsed)
    skipped = [p for p in probes if p["status"] == "skipped"]
    if skipped:
        w("<details>")
        w(f"<summary>\u23ed\ufe0f Skipped ({len(skipped)})</summary>\n")
        for p in skipped:
            ep = p["endpoint"]
            msg = p["message"]
            if msg:
                w(f"- `{p['func']}[{ep}]` -- {msg}")
            else:
                w(f"- `{p['func']}[{ep}]`")
        w("\n</details>\n")


def failure_summary(message):
    """Extract first check name from failure message for the <summary> tag.

    Schemathesis formats messages as multi-line (newline-separated) or
    single-line (double-space-separated) depending on the phase.
    """
    # Multi-line: look for "- Check name" on its own line
    for line in message.split("\n"):
        s = line.strip()
        if s.startswith("- "):
            return s[2:].split("  ")[0].strip()
    # Single-line: look for "  - Check name  " pattern
    m = re.search(r"\s{2,}- (.+?)(?:\s{2,}|$)", message)
    if m:
        return m.group(1).split("  ")[0].strip()
    return "Test failure"


def trim_schema_dumps(message):
    """Replace verbose 'Schema at ...' blocks with a short placeholder."""
    # Multi-line format (blank lines may appear within the schema block)
    message = re.sub(
        r"    Schema at [^\n]*\n(?:(?:        .*|)\n)*",
        "    (schema omitted)\n",
        message,
    )
    # Single-line format: "Schema at /path:  {  ...  }  Value:"
    message = re.sub(
        r"Schema at \S+:\s+\{[^}]*(?:\{[^}]*\}[^}]*)*\}\s+Value:",
        "(schema omitted)  Value:",
        message,
    )
    return message


def main(results_base):
    lines = []
    w = lines.append

    w("# \U0001f9ea Schemathesis API Test Report\n")
    w("## Summary\n")

    totals = {
        "endpoints": 0, "tested": 0, "got_2xx": 0,
        "checks_ok": 0, "checks_failed": 0,
    }
    suite_rows = []
    suite_data = []

    for suite_name in SERVICES:
        results_dir = results_base / suite_name
        cov = load_coverage(results_dir)
        junit = load_junit(results_dir)
        probes = load_probes(results_dir)
        har_responses = load_har_responses(
            results_dir, cov["operations"] if cov else []
        )

        # Endpoint counts from TraceCov JSON
        if cov:
            endpoints = cov["summary"]["operations"]["total"]
            tested = cov["summary"]["operations"]["covered"]
            pct = round(cov["summary"]["operations"]["percent"])
        else:
            tested = len(junit["passed"]) + len(junit["failed"])
            endpoints = tested
            pct = 100 if tested > 0 else 0

        # Count endpoints with 2xx from HAR data
        got_2xx = sum(
            1 for (m, p), codes in har_responses.items()
            if any(c.startswith("2") for c in codes)
        )

        checks_ok = len(junit["passed"])
        checks_failed = len(junit["failed"])

        totals["endpoints"] += endpoints
        totals["tested"] += tested
        totals["got_2xx"] += got_2xx
        totals["checks_ok"] += checks_ok
        totals["checks_failed"] += checks_failed

        # Format checks columns: show "--" if no per-endpoint JUnit data
        if checks_ok or checks_failed:
            ok_str = str(checks_ok)
            fail_str = str(checks_failed)
        else:
            ok_str = "--"
            fail_str = "--"

        suite_rows.append(
            f"| {suite_name} | {endpoints} | {tested} | {got_2xx} "
            f"| {ok_str} | {fail_str} | {pct}% |"
        )

        suite_data.append({
            "name": suite_name,
            "endpoints": endpoints,
            "tested": tested,
            "cov": cov,
            "har_responses": har_responses,
            "junit": junit,
            "probes": probes,
            "has_cov_html": (results_dir / "schema-coverage.html").exists(),
        })

    # Summary table
    w("| Suite | Endpoints | Tested | 2xx Seen | Checks OK | Checks Failed | Coverage |")
    w("|-------|-----------|--------|----------|-----------|---------------|----------|")
    for row in suite_rows:
        w(row)
    tot_pct = (
        round(totals["tested"] / totals["endpoints"] * 100)
        if totals["endpoints"]
        else 0
    )
    w(
        f"| **Total** | **{totals['endpoints']}** | **{totals['tested']}** "
        f"| **{totals['got_2xx']}** | **{totals['checks_ok']}** "
        f"| **{totals['checks_failed']}** | **{tot_pct}%** |"
    )
    w("")

    # Per-suite details
    for sd in suite_data:
        w(f"---\n\n## {sd['name']}\n")
        junit = sd["junit"]

        if sd["has_cov_html"]:
            w(f"> Schema coverage report: `{sd['name']}/schema-coverage.html`\n")

        # Endpoint coverage matrix
        if sd["cov"] and sd["cov"]["operations"]:
            render_endpoint_matrix(
                w, sd["cov"]["operations"], sd["har_responses"], sd["junit"]
            )

        # Failed endpoints (open, each endpoint collapsed)
        failed = junit["failed"]
        if failed:
            w("<details open>")
            w(f"<summary>\u274c Failed Checks ({len(failed)} endpoints)</summary>\n")
            for ep in sorted(failed):
                msgs = failed[ep]
                summary = failure_summary(msgs[0]) if msgs else "Test failure"
                w("<details>")
                w(f"<summary><code>{ep}</code> -- {summary}</summary>\n")
                for msg in msgs:
                    cleaned = trim_schema_dumps(msg)
                    w("```")
                    w(cleaned)
                    w("```\n")
                w("</details>\n")
            w("</details>\n")

        # Passed endpoints (collapsed)
        passed = sorted(junit["passed"])
        if passed:
            w("<details>")
            w(f"<summary>\u2705 Passed Checks ({len(passed)} endpoints)</summary>\n")
            for ep in passed:
                w(f"- `{ep}`")
            w("\n</details>\n")

        # Stateful tests (collapsed)
        stateful = junit["stateful"]
        if stateful:
            w("<details>")
            w(f"<summary>\U0001f517 Stateful Tests ({len(stateful)} failure(s))</summary>\n")
            for msg in stateful:
                cleaned = trim_schema_dumps(msg)
                w("```")
                w(cleaned)
                w("```\n")
            w("</details>\n")

        # Pytest probes
        if sd["probes"]:
            render_probes(w, sd["probes"])

        # Not reached
        not_reached = sd["endpoints"] - sd["tested"]
        if not_reached > 0:
            w(
                f"**Not reached:** {not_reached} endpoint(s) from the spec "
                f"were not tested.\n"
            )

    report = "\n".join(lines)

    output_path = results_base / "report.md"
    with open(output_path, "w") as f:
        f.write(report)

    print(f"\nReport written to {output_path}")


if __name__ == "__main__":
    if len(sys.argv) != 2:
        print(f"Usage: {sys.argv[0]} <results_dir>", file=sys.stderr)
        sys.exit(1)
    main(Path(sys.argv[1]))
