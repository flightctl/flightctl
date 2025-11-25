# Documentation Link Checker

## Overview

The `check-doc-links.yml` workflow automatically validates all links in the documentation to ensure they remain accessible and up-to-date.

## Features

### Automated Link Checking
- **PR Validation**: Runs automatically on pull requests that modify documentation files
- **Scheduled Checks**: Runs weekly (Mondays at 9 AM UTC) to catch external links that break over time
- **Manual Trigger**: Can be run manually via GitHub Actions UI using `workflow_dispatch`

### Intelligent Reporting
- **PR Comments**: Posts broken link reports directly on pull requests
- **Issue Creation**: Automatically creates GitHub issues for broken links found during scheduled runs
- **Job Summaries**: Provides detailed reports in the GitHub Actions job summary

### Performance Optimizations
- **Caching**: Caches link check results for 1 day to reduce load on external sites
- **Ignore Patterns**: Uses `.lycheeignore` to skip known false positives

## How It Works

### On Pull Requests
1. Workflow triggers when docs are modified
2. Checks all markdown files in `docs/` directory
3. **Fails the workflow** if broken links are found
4. Posts a comment on the PR with broken link details
5. Developer fixes links before merging

### On Schedule (Weekly)
1. Workflow runs every Monday at 9 AM UTC
2. Checks all markdown files in `docs/` directory
3. **Does not fail** - continues even with broken links
4. Creates a GitHub issue with broken link report
5. Team triages and fixes broken links from the issue

## Configuration

### Modifying Check Frequency

Edit the `cron` schedule in `.github/workflows/check-doc-links.yml`:

```yaml
schedule:
  # Daily at 2 AM UTC
  - cron: '0 2 * * *'

  # Weekly on Wednesdays at 10 AM UTC
  - cron: '0 10 * * 3'
```

### Ignoring Specific URLs

Add patterns to `.lycheeignore` in the repository root:

```
# Ignore specific URL
https://example.com/requires-auth

# Ignore URL pattern
https://internal-site.company.com/*

# Ignore domain
*.internal.company.com
```

### Checking Additional File Types

Modify the `args` in the workflow to include other file types:

```yaml
args: --verbose --no-progress --cache --max-cache-age 1d 'docs/**/*.md' '**/*.html'
```

## Troubleshooting

### False Positives

If a valid URL is being reported as broken:

1. Verify the URL is actually accessible in a browser
2. Check if it requires authentication or has rate limiting
3. Add it to `.lycheeignore` with a comment explaining why

### Rate Limiting

If you see rate limiting errors:

1. The workflow uses `GITHUB_TOKEN` for GitHub API calls
2. For other sites, consider adding them to `.lycheeignore` if they frequently rate-limit
3. Increase the `max-cache-age` value to reduce repeated checks

### Workflow Not Running

Check that:

1. The workflow file is in `.github/workflows/` directory
2. The file has `.yml` or `.yaml` extension
3. The workflow has appropriate permissions set
4. Branch protection rules allow the workflow to run

## Manual Testing

To test locally (requires [lychee](https://github.com/lycheeverse/lychee) installation):

```bash
# Install lychee
cargo install lychee

# Check all docs
lychee --verbose --cache --max-cache-age 1d 'docs/**/*.md'

# Check specific file
lychee docs/user/README.md
```

## Additional Resources

- [Lychee Documentation](https://github.com/lycheeverse/lychee)
- [Lychee Action Documentation](https://github.com/lycheeverse/lychee-action)
- [GitHub Actions Workflow Syntax](https://docs.github.com/en/actions/using-workflows/workflow-syntax-for-github-actions)
