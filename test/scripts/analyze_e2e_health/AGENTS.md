# analyze_e2e_health – AI assistant guidance

## Purpose

This tool produces a unified weekly HTML health report for the e2e CI pipeline,
covering flaky tests, pipeline timing, spec timing intelligence, and optimization
opportunities.

## Architecture: three strict phases

```
collect  →  e2e-raw-jobs.json    (all runs, job/step timings + collection metadata)
         →  e2e-raw-junit.json   (one entry per shard JUnit XML, completely unprocessed)
              ↓
compute  →  aggregates raw files → produces reportData in memory
              ↓
render   →  HTML report, Slack JSON, snapshot
```

### Phase 1 — Collect (`collect.go`)

- Hits the GitHub Actions API and downloads JUnit XML artifacts.
- Writes **raw, unfiltered GitHub data** to two JSON files. Nothing is aggregated,
  merged, or filtered here.
- `e2e-raw-jobs.json` — a `rawJobsFile` with collection metadata and one
  `rawRunEntry` per analyzed run. Every run is present (successful runs have full
  job/step data; failed runs have an empty `Jobs` array so their IDs are tracked).
- `e2e-raw-junit.json` — a flat `[]rawJUnitFile`, one entry per downloaded JUnit
  XML file. A run with 11 shards produces 11 entries. No shard merging is done.
- Collect is skipped automatically if `e2e-raw-jobs.json` already exists on disk.
  Delete both raw files to trigger a fresh fetch.

**Rule: collect must not perform any aggregation, filtering, or business logic.
It is a pure data dump of what the GitHub API returned.**

### Phase 2 — Compute (`compute.go`)

- Reads the two raw files produced by collect.
- Performs all business logic:
  - `aggregateRawJobs` — derives pipeline phase timings and wall times (only from
    successful runs; failed runs skew averages downward).
  - `aggregateJUnit` — groups shard files by run, merges shard results
    (failure wins; pass overrides Ginkgo's cross-shard skips), detects infra
    instability runs, and accumulates per-spec pass/fail counts.
  - `computeTimings` — derives per-spec avg duration and stddev from the JUnit
    `<testcase time="">` attribute (same logic as `update_test_timings`).
  - `computeFlakes`, `computeOptimizations`, `computeNewTests`, etc.
- Produces a `reportData` struct — pure Go values, no HTML or SVG.

**Rule: compute always receives the raw unfiltered data created by collect.
Never pre-process or filter data before passing it to compute. All logic lives here.**

### Phase 3 — Render (`render.go`, `template.html`)

- Receives `reportData` from compute.
- Generates charts (inline SVG), executes the Go HTML template, writes
  the self-contained `e2e-health-report.html`.
- Also writes `e2e-health-summary.json` (Slack) and `e2e-health-snapshot.json`
  (spec list for next week's new-test detection).

**Rule: render must not perform any computation or data transformation.
It only formats and presents the values it receives.**

## Raw file formats

### `e2e-raw-jobs.json` (`rawJobsFile`)

```json
{
  "meta": {
    "branch": "main",
    "workflow": "pr-e2e-testing.yaml",
    "repo_url": "https://github.com/owner/repo",
    "generated_at": "2026-06-01T09:00:00Z",
    "analyzed_runs": 14
  },
  "runs": [
    {
      "run_id": 123456,
      "run_url": "https://github.com/owner/repo/actions/runs/123456",
      "date": "2026-05-28",
      "started_at": "2026-05-28T10:00:00Z",
      "conclusion": "success",
      "jobs": [
        {
          "name": "e2e-tests (cs9-bootc, helm, sanity, 1) / e2e-test",
          "started_at": "2026-05-28T10:05:00Z",
          "completed_at": "2026-05-28T10:53:00Z",
          "steps": [
            { "name": "deploy env",  "duration_sec": 190 },
            { "name": "run tests",   "duration_sec": 2415 }
          ]
        }
      ]
    }
  ]
}
```

Failed runs have an empty `jobs` array so `aggregateJUnit` can detect which
run IDs produced no JUnit output.

### `e2e-raw-junit.json` (`[]rawJUnitFile`)

```json
[
  {
    "run_id": 123456,
    "run_url": "https://github.com/owner/repo/actions/runs/123456",
    "date": "2026-05-28",
    "specs": {
      "Agent Suite Device should enroll and become online": {
        "passed": true,
        "duration_sec": 42.1,
        "ginkgo_label": "78753"
      },
      "Fleet Suite should deploy application": {
        "skipped": true
      }
    }
  }
]
```

One entry per shard XML file. A run with 11 shards has 11 entries here.
Specs not assigned to a shard appear as `"skipped": true` in that shard's entry —
this is Ginkgo's normal cross-shard output; `aggregateJUnit` handles it.

## Local iteration

```bash
# Full collect + compute + render (requires GH_TOKEN and GITHUB_REPOSITORY):
go run ./test/scripts/analyze_e2e_health

# Re-run compute + render from existing raw files (no API calls needed):
go run ./test/scripts/analyze_e2e_health \
    --raw-jobs  e2e-raw-jobs.json \
    --raw-junit e2e-raw-junit.json

# Run tests:
go test ./test/scripts/analyze_e2e_health/...
```

## Key invariants

- **collect never aggregates.** All shard XMLs are saved as separate entries.
- **compute always starts from raw.** There is no intermediate pre-processed file.
- **render never computes.** It only formats `reportData`.
- **Only successful runs contribute to pipeline timing** (failed runs abort early
  and skew phase averages).
- **All runs (success and failure) contribute to flake counting** via the JUnit
  raw files, so a spec that always fails is correctly classified.
- **Infra instability detection** happens in `aggregateJUnit`: if >30% of a run's
  merged specs fail, the entire run is excluded from per-spec flake counts (but
  still counted in the infra instability event total).
