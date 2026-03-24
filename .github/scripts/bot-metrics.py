#!/usr/bin/env python3
"""Collect adoption metrics for the AI issue solver bot.

Runs in CI (GitHub Actions) or locally with `gh` CLI authenticated.
Outputs a markdown report to the GitHub Actions job summary, or stdout.

The report splits PRs into "Legacy Bot" (before cutoff) and "Redesigned Bot"
(on or after cutoff) sections for side-by-side comparison.

Configuration via environment variables:
  BOT_AUTHOR         - GitHub login of the bot (default: app/bugs-buddy-jira-ai-issue-solver)
  EXCLUDED_TICKETS   - Comma-separated ticket keys to exclude (default: "")
  CUTOFF_DATE        - YYYY-MM-DD boundary between legacy and redesigned bot (default: 2026-03-23)
"""

import json
import os
import subprocess
import sys
from collections import Counter, defaultdict
from datetime import datetime, timezone, date

BOT_AUTHOR = os.environ.get(
    "BOT_AUTHOR", "app/bugs-buddy-jira-ai-issue-solver"
)
EXCLUDED_TICKETS = [
    t.strip()
    for t in os.environ.get("EXCLUDED_TICKETS", "").split(",")
    if t.strip()
]
CUTOFF_DATE = date.fromisoformat(
    os.environ.get("CUTOFF_DATE", "2026-03-23")
)


def run_gh(*args):
    """Run a gh CLI command and return parsed JSON."""
    result = subprocess.run(
        ["gh", *args], capture_output=True, text=True, check=True
    )
    return json.loads(result.stdout) if result.stdout.strip() else []


def parse_time(iso_str):
    """Parse an ISO-8601 timestamp from the GitHub API."""
    if not iso_str:
        return None
    return datetime.fromisoformat(iso_str.replace("Z", "+00:00"))


def extract_ticket(title):
    """Extract ticket key from PR title (e.g., 'EDM-1234: ...' -> 'EDM-1234')."""
    colon_idx = title.find(":")
    if colon_idx == -1:
        return None
    candidate = title[:colon_idx].strip()
    if "-" in candidate and any(c.isdigit() for c in candidate):
        return candidate
    return None


def classify_size(additions, deletions):
    """Classify a PR by total lines changed."""
    total = additions + deletions
    if total <= 10:
        return "XS (1-10)"
    if total <= 50:
        return "S (11-50)"
    if total <= 200:
        return "M (51-200)"
    if total <= 500:
        return "L (201-500)"
    return "XL (500+)"


def ci_passed(status_checks):
    """Determine CI pass/fail from statusCheckRollup."""
    if not status_checks:
        return None
    for check in status_checks:
        conclusion = (check.get("conclusion") or "").upper()
        if conclusion == "FAILURE":
            return False
    return True


def fetch_prs():
    """Fetch all bot PRs (metadata only, no CI status)."""
    fields = ",".join([
        "number", "title", "state", "createdAt", "mergedAt", "closedAt",
        "additions", "deletions", "reviews",
    ])
    return run_gh(
        "pr", "list",
        "--state", "all",
        "--author", BOT_AUTHOR,
        "--limit", "500",
        "--json", fields,
    )


def fetch_ci_status(pr_numbers):
    """Fetch CI check status for specific PRs individually.

    statusCheckRollup is too large to fetch in bulk (causes GitHub API
    timeouts), so we query one PR at a time.
    """
    results = {}
    for num in pr_numbers:
        try:
            data = run_gh(
                "pr", "view", str(num),
                "--json", "number,statusCheckRollup",
            )
            results[num] = data.get("statusCheckRollup", [])
        except subprocess.CalledProcessError:
            results[num] = []
    return results


def filter_and_split(prs):
    """Exclude test tickets and split into legacy/redesigned buckets."""
    excluded_count = 0
    legacy = []
    redesigned = []

    for pr in prs:
        ticket = extract_ticket(pr["title"])
        if ticket and ticket in EXCLUDED_TICKETS:
            excluded_count += 1
            continue

        created = parse_time(pr["createdAt"])
        if created and created.date() >= CUTOFF_DATE:
            redesigned.append(pr)
        else:
            legacy.append(pr)

    return legacy, redesigned, excluded_count


def compute_metrics(prs, ci_status):
    """Compute adoption metrics from a list of PRs. Returns None if empty."""
    if not prs:
        return None

    total = len(prs)
    merged = [p for p in prs if p["state"] == "MERGED"]
    closed = [p for p in prs if p["state"] == "CLOSED"]
    open_prs = [p for p in prs if p["state"] == "OPEN"]

    # -- Ticket-level aggregation --
    tickets = defaultdict(list)
    for pr in prs:
        ticket = extract_ticket(pr["title"]) or f"(no ticket) PR#{pr['number']}"
        tickets[ticket].append(pr)

    resolved = {
        t for t, t_prs in tickets.items()
        if any(p["state"] == "MERGED" for p in t_prs)
    }

    # -- CI pass rate --
    ci_results = [ci_passed(ci_status.get(pr["number"])) for pr in prs]
    ci_known = [r for r in ci_results if r is not None]
    ci_pass_count = sum(1 for r in ci_known if r)

    # -- Merge time --
    merge_hours = []
    for pr in merged:
        created = parse_time(pr["createdAt"])
        merged_at = parse_time(pr["mergedAt"])
        if created and merged_at:
            merge_hours.append((merged_at - created).total_seconds() / 3600)

    # -- Review counts on merged PRs --
    review_counts = [len(pr.get("reviews") or []) for pr in merged]

    # -- Size distribution --
    size_dist = Counter(
        classify_size(pr.get("additions", 0), pr.get("deletions", 0))
        for pr in prs
    )

    # -- Monthly trend --
    monthly = defaultdict(lambda: {"total": 0, "merged": 0, "closed": 0, "open": 0})
    for pr in prs:
        month = pr["createdAt"][:7]
        monthly[month]["total"] += 1
        monthly[month][pr["state"].lower()] += 1

    # -- Per-ticket detail --
    ticket_rows = []
    for ticket, t_prs in sorted(tickets.items()):
        m = sum(1 for p in t_prs if p["state"] == "MERGED")
        c = sum(1 for p in t_prs if p["state"] == "CLOSED")
        o = sum(1 for p in t_prs if p["state"] == "OPEN")
        ci_ok = sum(
            1 for p in t_prs
            if ci_passed(ci_status.get(p["number"])) is True
        )
        ci_total = sum(
            1 for p in t_prs
            if ci_passed(ci_status.get(p["number"])) is not None
        )
        ticket_rows.append({
            "ticket": ticket,
            "total": len(t_prs),
            "merged": m,
            "closed": c,
            "open": o,
            "resolved": ticket in resolved,
            "ci_pass": f"{ci_ok}/{ci_total}" if ci_total else "n/a",
        })

    prs_per_ticket_vals = [len(t_prs) for t_prs in tickets.values()]

    return {
        "total_prs": total,
        "merged": len(merged),
        "closed": len(closed),
        "open": len(open_prs),
        "merge_rate": len(merged) / total * 100,
        "unique_tickets": len(tickets),
        "resolved_tickets": len(resolved),
        "resolution_rate": len(resolved) / len(tickets) * 100 if tickets else 0,
        "avg_prs_per_ticket": (
            sum(prs_per_ticket_vals) / len(prs_per_ticket_vals)
            if prs_per_ticket_vals else 0
        ),
        "merged_additions": sum(p.get("additions", 0) for p in merged),
        "merged_deletions": sum(p.get("deletions", 0) for p in merged),
        "avg_reviews_merged": (
            sum(review_counts) / len(review_counts) if review_counts else 0
        ),
        "avg_merge_hours": (
            sum(merge_hours) / len(merge_hours) if merge_hours else None
        ),
        "ci_pass_rate": (
            ci_pass_count / len(ci_known) * 100 if ci_known else None
        ),
        "ci_total_checked": len(ci_known),
        "size_distribution": dict(size_dist),
        "monthly_trend": dict(monthly),
        "ticket_details": ticket_rows,
    }


def format_merge_time(hours):
    """Format hours as a human-readable duration."""
    if hours is None:
        return "n/a"
    if hours > 48:
        return f"{hours / 24:.1f} days"
    return f"{hours:.1f} hours"


def format_comparison_table(legacy, redesigned):
    """Render a side-by-side comparison of legacy vs redesigned metrics."""
    lines = []

    def val(m, key, fmt=None):
        if m is None:
            return "—"
        v = m[key]
        if v is None:
            return "—"
        if fmt:
            return fmt(v)
        return str(v)

    def pct(m, key):
        return val(m, key, lambda v: f"{v:.1f}%")

    def ci_rate(m):
        if m is None or m["ci_pass_rate"] is None:
            return "—"
        return f"{m['ci_pass_rate']:.1f}% ({m['ci_total_checked']} checked)"

    lines.append("## Comparison: Legacy vs Redesigned")
    lines.append("")
    lines.append(f"| Metric | Legacy (before {CUTOFF_DATE}) | Redesigned (from {CUTOFF_DATE}) |")
    lines.append("|--------|------|------------|")
    lines.append(f"| Total PRs | {val(legacy, 'total_prs')} | {val(redesigned, 'total_prs')} |")
    lines.append(f"| Merged | {val(legacy, 'merged')} | {val(redesigned, 'merged')} |")
    lines.append(f"| Closed (rejected) | {val(legacy, 'closed')} | {val(redesigned, 'closed')} |")
    lines.append(f"| Open | {val(legacy, 'open')} | {val(redesigned, 'open')} |")
    lines.append(f"| **Merge rate** | **{pct(legacy, 'merge_rate')}** | **{pct(redesigned, 'merge_rate')}** |")
    lines.append(f"| CI pass rate | {ci_rate(legacy)} | {ci_rate(redesigned)} |")
    lines.append(f"| Unique tickets | {val(legacy, 'unique_tickets')} | {val(redesigned, 'unique_tickets')} |")
    lines.append(f"| Tickets resolved | {val(legacy, 'resolved_tickets')} | {val(redesigned, 'resolved_tickets')} |")
    lines.append(f"| **Resolution rate** | **{pct(legacy, 'resolution_rate')}** | **{pct(redesigned, 'resolution_rate')}** |")
    lines.append(f"| Avg PRs per ticket | {val(legacy, 'avg_prs_per_ticket', lambda v: f'{v:.1f}')} | {val(redesigned, 'avg_prs_per_ticket', lambda v: f'{v:.1f}')} |")
    lines.append(f"| Avg reviews (merged) | {val(legacy, 'avg_reviews_merged', lambda v: f'{v:.1f}')} | {val(redesigned, 'avg_reviews_merged', lambda v: f'{v:.1f}')} |")
    lines.append(f"| Avg time to merge | {format_merge_time(legacy['avg_merge_hours'] if legacy else None)} | {format_merge_time(redesigned['avg_merge_hours'] if redesigned else None)} |")
    lines.append(f"| Lines added (merged) | {val(legacy, 'merged_additions', lambda v: f'+{v}')} | {val(redesigned, 'merged_additions', lambda v: f'+{v}')} |")
    lines.append(f"| Lines removed (merged) | {val(legacy, 'merged_deletions', lambda v: f'-{v}')} | {val(redesigned, 'merged_deletions', lambda v: f'-{v}')} |")
    lines.append("")

    return lines


def format_section_detail(label, m):
    """Render per-ticket breakdown and monthly trend for one section."""
    if m is None:
        return []

    lines = []

    # -- PR size distribution --
    lines.append(f"### {label}: PR Size Distribution")
    lines.append("")
    lines.append("| Size | Count |")
    lines.append("|------|-------|")
    for size in ["XS (1-10)", "S (11-50)", "M (51-200)", "L (201-500)", "XL (500+)"]:
        count = m["size_distribution"].get(size, 0)
        if count > 0:
            lines.append(f"| {size} | {count} |")
    lines.append("")

    # -- Monthly trend --
    if m["monthly_trend"]:
        lines.append(f"### {label}: Monthly Trend")
        lines.append("")
        lines.append("| Month | Total | Merged | Closed | Open | Merge Rate |")
        lines.append("|-------|-------|--------|--------|------|------------|")
        for month in sorted(m["monthly_trend"]):
            d = m["monthly_trend"][month]
            rate = d["merged"] / d["total"] * 100 if d["total"] else 0
            lines.append(
                f"| {month} | {d['total']} | {d['merged']} "
                f"| {d['closed']} | {d['open']} | {rate:.0f}% |"
            )
        lines.append("")

    # -- Per-ticket breakdown --
    lines.append(f"### {label}: Per-Ticket Breakdown")
    lines.append("")
    lines.append("| Ticket | PRs | Merged | Closed | Open | CI Pass | Resolved |")
    lines.append("|--------|-----|--------|--------|------|---------|----------|")
    for t in sorted(m["ticket_details"], key=lambda x: x["total"], reverse=True):
        resolved = "Yes" if t["resolved"] else "No"
        lines.append(
            f"| {t['ticket']} | {t['total']} | {t['merged']} "
            f"| {t['closed']} | {t['open']} | {t['ci_pass']} | {resolved} |"
        )
    lines.append("")

    return lines


def format_report(legacy_metrics, redesigned_metrics, excluded_count):
    """Render the full before/after comparison report."""
    lines = []
    now = datetime.now(timezone.utc).strftime("%Y-%m-%d %H:%M UTC")

    lines.append("# AI Bot Adoption Metrics")
    lines.append("")
    lines.append(f"*Generated: {now}*")
    lines.append(f"*Cutoff date: {CUTOFF_DATE} (legacy before, redesigned from)*")
    if excluded_count:
        lines.append(
            f"*Excluded {excluded_count} PRs from test tickets: "
            f"{', '.join(EXCLUDED_TICKETS)}*"
        )
    lines.append("")

    # -- Side-by-side comparison --
    lines.extend(format_comparison_table(legacy_metrics, redesigned_metrics))

    # -- Detail sections --
    if redesigned_metrics:
        lines.extend(format_section_detail("Redesigned Bot", redesigned_metrics))
    if legacy_metrics:
        lines.extend(format_section_detail("Legacy Bot", legacy_metrics))

    return "\n".join(lines)


def write_output(key, value):
    """Write a key=value pair to GITHUB_OUTPUT if available."""
    path = os.environ.get("GITHUB_OUTPUT")
    if path:
        with open(path, "a") as f:
            f.write(f"{key}={value}\n")


def main():
    print("Fetching bot PRs...", file=sys.stderr)
    prs = fetch_prs()
    if not prs:
        print("No bot PRs found.")
        sys.exit(0)
    print(f"Found {len(prs)} total bot PRs", file=sys.stderr)

    legacy_prs, redesigned_prs, excluded_count = filter_and_split(prs)
    print(
        f"Legacy: {len(legacy_prs)}, Redesigned: {len(redesigned_prs)}, "
        f"Excluded: {excluded_count}",
        file=sys.stderr,
    )

    if not legacy_prs and not redesigned_prs:
        print(f"No PRs remaining after filtering ({excluded_count} excluded).")
        sys.exit(0)

    # Fetch CI status for all non-excluded PRs
    all_pr_numbers = (
        [pr["number"] for pr in legacy_prs]
        + [pr["number"] for pr in redesigned_prs]
    )
    print(
        f"Fetching CI status for {len(all_pr_numbers)} PRs...",
        file=sys.stderr,
    )
    ci_status = fetch_ci_status(all_pr_numbers)

    legacy_metrics = compute_metrics(legacy_prs, ci_status)
    redesigned_metrics = compute_metrics(redesigned_prs, ci_status)

    report = format_report(legacy_metrics, redesigned_metrics, excluded_count)

    # Write to GitHub Actions job summary if available
    summary_path = os.environ.get("GITHUB_STEP_SUMMARY")
    if summary_path:
        with open(summary_path, "a") as f:
            f.write(report)
        total = len(legacy_prs) + len(redesigned_prs)
        print(f"Report written to job summary ({total} PRs analyzed)")
    else:
        print(report)

    # Export redesigned bot metrics as step outputs (primary interest)
    if redesigned_metrics:
        write_output("total_prs", redesigned_metrics["total_prs"])
        write_output("merge_rate", f"{redesigned_metrics['merge_rate']:.1f}")
        write_output("resolution_rate", f"{redesigned_metrics['resolution_rate']:.1f}")
        write_output("unique_tickets", redesigned_metrics["unique_tickets"])
        write_output("resolved_tickets", redesigned_metrics["resolved_tickets"])
    else:
        write_output("total_prs", 0)
        write_output("merge_rate", "0.0")
        write_output("resolution_rate", "0.0")
        write_output("unique_tickets", 0)
        write_output("resolved_tickets", 0)


if __name__ == "__main__":
    main()
