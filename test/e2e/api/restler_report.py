#!/usr/bin/env python3
"""Generate a markdown report from RESTler test results."""

import json
import sys
from pathlib import Path


def find_file(base, name):
    for p in base.rglob(name):
        return p
    return None


def load_json(path):
    if path and path.exists():
        with open(path) as f:
            return json.load(f)
    return None


def parse_coverage(cov_str):
    parts = cov_str.split("/")
    return int(parts[0].strip()), int(parts[1].strip())


def percent(a, b):
    return round(a / b * 100) if b else 0


def is_json(s):
    try:
        json.loads(s)
        return True
    except (json.JSONDecodeError, TypeError):
        return False


def endpoint_status(entry):
    if entry["valid"] == 1:
        return "passed"
    if entry.get("status_code") is not None:
        return "not_tested"
    return "unreached"


def collect_bugs(version_dir):
    bugs = []
    for bug_dir in version_dir.rglob("bug_buckets"):
        if not bug_dir.is_dir():
            continue
        for f in sorted(bug_dir.glob("*.json")):
            if f.name in ("bug_buckets.json", "Bugs.json"):
                continue
            data = load_json(f)
            if data and "checker_name" in data:
                bugs.append(data)
    return bugs


def collect_suites(reports_dir):
    suites = []
    for service_dir in sorted(reports_dir.iterdir()):
        if not service_dir.is_dir():
            continue
        for version_dir in sorted(service_dir.iterdir()):
            if not version_dir.is_dir():
                continue
            summary = load_json(find_file(version_dir, "testing_summary.json"))
            if not summary:
                continue
            suites.append({
                "name": f"{service_dir.name}/{version_dir.name}",
                "summary": summary,
                "run_summary": load_json(find_file(version_dir, "runSummary.json")),
                "speccov": load_json(find_file(version_dir, "speccov.json")),
                "bugs": collect_bugs(version_dir),
            })
    return suites


def write_summary_table(out, suites):
    out.write("## Summary\n\n")
    out.write("| Suite | Coverage | % | Rendered | Valid | Failures |\n")
    out.write("|-------|----------|---|----------|-------|----------|\n")

    tot = {"cov": [0, 0], "rend": [0, 0], "valid": [0, 0], "failures": 0}

    for s in suites:
        sm = s["summary"]
        c = parse_coverage(sm["final_spec_coverage"])
        r = parse_coverage(sm["rendered_requests"])
        v = parse_coverage(sm["rendered_requests_valid_status"])
        failures = len(s["bugs"])

        tot["cov"][0] += c[0]; tot["cov"][1] += c[1]
        tot["rend"][0] += r[0]; tot["rend"][1] += r[1]
        tot["valid"][0] += v[0]; tot["valid"][1] += v[1]
        tot["failures"] += failures

        pct = percent(c[0], c[1])
        out.write(
            f"| {s['name']} "
            f"| {c[0]} / {c[1]} | {pct}% "
            f"| {r[0]} / {r[1]} "
            f"| {v[0]} / {v[1]} "
            f"| {failures} |\n"
        )

    tc, tr, tv = tot["cov"], tot["rend"], tot["valid"]
    pct = percent(tc[0], tc[1])
    out.write(
        f"| **Totals** "
        f"| **{tc[0]} / {tc[1]}** | **{pct}%** "
        f"| **{tr[0]} / {tr[1]}** "
        f"| **{tv[0]} / {tv[1]}** "
        f"| **{tot['failures']}** |\n"
    )
    out.write("\n")


def write_response_codes_table(out, suites):
    out.write("## Response Codes\n\n")

    all_codes = set()
    for s in suites:
        rs = s.get("run_summary") or {}
        all_codes.update(rs.get("codeCounts", {}).keys())
    if not all_codes:
        return

    codes = sorted(all_codes, key=lambda c: int(c))
    out.write("| Suite | " + " | ".join(codes) + " | Total |\n")
    out.write("|-------|" + "|".join(["------"] * len(codes)) + "|-------|\n")

    for s in suites:
        cc = (s.get("run_summary") or {}).get("codeCounts", {})
        total = sum(cc.get(c, 0) for c in codes)
        cells = " | ".join(str(cc.get(c, 0)) for c in codes)
        out.write(f"| {s['name']} | {cells} | {total} |\n")

    out.write("\n")


def split_endpoints(speccov):
    entries = list(speccov.values()) if isinstance(speccov, dict) else speccov
    passed, not_tested, unreached = [], [], []
    for e in entries:
        st = endpoint_status(e)
        if st == "passed":
            passed.append(e)
        elif st == "not_tested":
            not_tested.append(e)
        else:
            unreached.append(e)

    passed.sort(key=lambda e: (e["endpoint"], e["verb"]))
    not_tested.sort(key=lambda e: (e.get("status_code") or "", e["endpoint"], e["verb"]))
    unreached.sort(key=lambda e: (e["endpoint"], e["verb"]))
    return passed, not_tested, unreached


def write_passed(out, entries):
    if not entries:
        return
    out.write(f"### ✅ Passed ({len(entries)})\n\n")
    out.write("| # | Method | Endpoint | Response |\n")
    out.write("|---|--------|----------|----------|\n")
    for i, e in enumerate(entries, 1):
        code = e.get("status_code") or ""
        text = e.get("status_text") or ""
        response = f"{code} {text}".strip()
        out.write(f"| {i} | `{e['verb']}` | `{e['endpoint']}` | {response} |\n")
    out.write("\n")


def write_not_tested(out, entries):
    if not entries:
        return
    out.write(f"### ⚠️ Not Tested ({len(entries)})\n\n")
    out.write("| # | Method | Endpoint | Response |\n")
    out.write("|---|--------|----------|----------|\n")
    for i, e in enumerate(entries, 1):
        code = e.get("status_code") or ""
        text = e.get("status_text") or ""
        response = f"{code} {text}".strip()
        out.write(f"| {i} | `{e['verb']}` | `{e['endpoint']}` | {response} |\n")
    out.write("\n")

    has_errors = any(e.get("error_message") for e in entries)
    if not has_errors:
        return

    out.write("<details>\n<summary>Error details</summary>\n\n")
    for i, e in enumerate(entries, 1):
        error = e.get("error_message")
        if not error:
            continue
        code = e.get("status_code") or ""
        text = e.get("status_text") or ""
        out.write(f"**{i}. `{e['verb']} {e['endpoint']}`** - {code} {text}\n\n")
        lang = "json" if is_json(error) else ""
        out.write(f"```{lang}\n{error}\n```\n\n")
    out.write("</details>\n\n")


def write_unreached(out, entries):
    if not entries:
        return
    out.write(f"### ⏭️ Unreached ({len(entries)})\n\n")
    out.write("| # | Method | Endpoint |\n")
    out.write("|---|--------|----------|\n")
    for i, e in enumerate(entries, 1):
        out.write(f"| {i} | `{e['verb']}` | `{e['endpoint']}` |\n")
    out.write("\n")


def write_failures(out, bugs):
    if not bugs:
        return
    out.write(f"### ❌ Failures ({len(bugs)})\n\n")
    out.write("| # | Checker | Method | Endpoint | Reproducible |\n")
    out.write("|---|---------|--------|----------|-------------|\n")
    for i, b in enumerate(bugs, 1):
        repro = "Yes" if b.get("reproducible") else "No"
        out.write(
            f"| {i} | {b['checker_name']} "
            f"| `{b.get('verb', '-')}` "
            f"| `{b.get('endpoint', '-')}` "
            f"| {repro} |\n"
        )
    out.write("\n")


def write_code_distribution(out, code_counts):
    out.write("### Response Code Distribution\n\n")
    out.write("| Code | Count |\n")
    out.write("|------|-------|\n")
    for code in sorted(code_counts.keys(), key=lambda c: int(c)):
        out.write(f"| {code} | {code_counts[code]} |\n")
    out.write("\n")


def write_suite_detail(out, suite):
    out.write(f"## 📦 {suite['name']}\n\n")

    speccov = suite.get("speccov")
    if speccov:
        passed, not_tested, unreached = split_endpoints(speccov)
        write_passed(out, passed)
        write_not_tested(out, not_tested)
        write_unreached(out, unreached)

    write_failures(out, suite["bugs"])

    rs = suite.get("run_summary")
    if rs and "codeCounts" in rs:
        write_code_distribution(out, rs["codeCounts"])


def print_summary(suites):
    tot_cov, tot_spec, tot_400, tot_failures = 0, 0, 0, 0

    for s in suites:
        sm = s["summary"]
        cov, spec = parse_coverage(sm["final_spec_coverage"])
        valid_num, valid_den = parse_coverage(sm["rendered_requests_valid_status"])
        failures = len(s["bugs"])
        cc = (s.get("run_summary") or {}).get("codeCounts", {})

        tot_cov += cov
        tot_spec += spec
        tot_400 += int(cc.get("400", 0))
        tot_failures += failures

        print(f"\n{'=' * 60}")
        print(f"  {s['name']}")
        print(f"{'=' * 60}")
        pct = percent(cov, spec)
        print(f"Coverage: {cov} / {spec} ({pct}%)   Valid: {valid_num} / {valid_den}   Failures: {failures}")

        if cc:
            print("\nStatus codes:")
            for c in sorted(cc, key=lambda c: int(c)):
                print(f"  {c:<6} {cc[c]}")

        speccov = s.get("speccov")
        if speccov:
            _, not_tested, unreached = split_endpoints(speccov)
            if not_tested:
                print("\nNot tested:")
                for e in not_tested:
                    code = e.get("status_code", "?")
                    print(f"  {code:<6} {e['verb']:<8} {e['endpoint']}")
            if unreached:
                print("\nUnreached:")
                for e in unreached:
                    print(f"  {e['verb']:<8} {e['endpoint']}")

    print(f"\n{'=' * 60}")
    print(f"  TOTALS")
    print(f"{'=' * 60}")
    tot_pct = percent(tot_cov, tot_spec)
    print(f"Coverage: {tot_cov} / {tot_spec} ({tot_pct}%)   400 errors: {tot_400}   Failures: {tot_failures}")


def main():
    reports_dir = Path(sys.argv[1]) if len(sys.argv) > 1 else Path("reports/restler")
    if not reports_dir.is_dir():
        print(f"No reports found at {reports_dir}", file=sys.stderr)
        sys.exit(1)

    suites = collect_suites(reports_dir)
    if not suites:
        print(f"No test results found under {reports_dir}", file=sys.stderr)
        sys.exit(1)

    print_summary(suites)

    output_path = reports_dir / "report.md"
    with open(output_path, "w") as out:
        out.write("# RESTler API Test Report\n\n")
        write_summary_table(out, suites)
        write_response_codes_table(out, suites)
        for suite in suites:
            write_suite_detail(out, suite)

    print(f"\nReport written to {output_path}")


if __name__ == "__main__":
    main()
