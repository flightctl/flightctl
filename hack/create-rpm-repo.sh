#!/usr/bin/env bash

# Create RPM Repository Structure Script
# Converts COPR downloads into a flightctl-rpm repository structure

set -euo pipefail

# Configuration
OUTPUT_DIR=".output"
COPR_DOWNLOAD_DIR="${1:-$OUTPUT_DIR/copr-rpms-temp}"
REPO_OUTPUT_DIR="${2:-$OUTPUT_DIR/flightctl-rpm}"
REPO_OWNER="${3:-flightctl}"
REPO_NAME="${4:-flightctl}"

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${BLUE}[INFO]${NC} $1"; }
success() { echo -e "${GREEN}[SUCCESS]${NC} $1"; }
error() { echo -e "${RED}[ERROR]${NC} $1"; }

# Show usage
if [ -z "$REPO_OWNER" ] || [ -z "$REPO_NAME" ]; then
    echo "Usage: $0 [copr_download_dir] [repo_output_dir] <repo_owner> <repo_name>"
    echo "Example: $0 flightctl flightctl"
    echo "Example: $0 .output/copr-rpms-temp .output/flightctl-rpm flightctl flightctl"
    exit 1
fi

# Create output directory
mkdir -p "$OUTPUT_DIR"

# Validate inputs
if [ ! -d "$COPR_DOWNLOAD_DIR" ]; then
    error "COPR download directory not found: $COPR_DOWNLOAD_DIR"
    exit 1
fi

rpm_count=$(find "$COPR_DOWNLOAD_DIR" -name "*.rpm" | wc -l)
if [ $rpm_count -eq 0 ]; then
    error "No RPM files found in $COPR_DOWNLOAD_DIR"
    exit 1
fi

log "Processing $rpm_count RPM files from $COPR_DOWNLOAD_DIR"

# Auto-detect the latest version
mapfile -t _VERSIONS < <(
  find "$COPR_DOWNLOAD_DIR" -name '*.rpm' -exec sh -c '
    rpm -qp --qf "%{VERSION}\n" "$1"
  ' _ {} \;
)
if command -v rpmdev-sort &>/dev/null; then
  LATEST_VERSION=$(printf '%s\n' "${_VERSIONS[@]}" | rpmdev-sort | tail -1)
else
  LATEST_VERSION=$(printf '%s\n' "${_VERSIONS[@]}" | sort -V | tail -1)
fi

log "Detected latest version: $LATEST_VERSION"

# Create repository structure (one-level, RPMs only)
log "Creating RPM repository structure..."
rm -rf "$REPO_OUTPUT_DIR"
mkdir -p "$REPO_OUTPUT_DIR"

# Copy RPM files directly to repository root structure
log "Copying RPM files..."
for platform_dir in "$COPR_DOWNLOAD_DIR"/*/; do
    if [ -d "$platform_dir" ]; then
        platform=$(basename "$platform_dir")
        target_dir="$REPO_OUTPUT_DIR/$platform"

        mkdir -p "$target_dir"
        find "$platform_dir" -name "*.rpm" -exec cp {} "$target_dir/" \;

        if [ -d "$platform_dir/repodata" ]; then
            cp -r "$platform_dir/repodata" "$target_dir/"
        fi

        rpm_count=$(find "$target_dir" -name "*.rpm" | wc -l)
        log "  $platform: copied $rpm_count RPM files"
    fi
done

# Create repository configuration files
log "Creating repository configuration files..."

cat > "$REPO_OUTPUT_DIR/flightctl-epel.repo" << EOF
[flightctl]
name=FlightCtl RPM Repository (EPEL)
type=rpm-md
baseurl=https://flightctl.github.io/flightctl-rpm/epel-9-\$basearch/
gpgcheck=1
gpgkey=https://download.copr.fedorainfracloud.org/results/@redhat-et/flightctl/pubkey.gpg
enabled=1
enabled_metadata=1
metadata_expire=1d
EOF

cat > "$REPO_OUTPUT_DIR/flightctl-fedora.repo" << EOF
[flightctl]
name=FlightCtl RPM Repository (Fedora)
type=rpm-md
baseurl=https://flightctl.github.io/flightctl-rpm/fedora-\$releasever-\$basearch/
gpgcheck=1
gpgkey=https://download.copr.fedorainfracloud.org/results/@redhat-et/flightctl/pubkey.gpg
enabled=1
enabled_metadata=1
metadata_expire=1d
EOF

# Analyze repository content
total_rpms=$(find "$REPO_OUTPUT_DIR" -name "*.rpm" | wc -l)
mapfile -t _REPO_VERSIONS < <(
  find "$REPO_OUTPUT_DIR" -name '*.rpm' -exec sh -c '
    rpm -qp --qf "%{VERSION}\n" "$1"
  ' _ {} \;
)
if command -v rpmdev-sort &>/dev/null; then
  versions=$(printf '%s\n' "${_REPO_VERSIONS[@]}" | rpmdev-sort | uniq | tr '\n' ' ')
else
  versions=$(printf '%s\n' "${_REPO_VERSIONS[@]}" | sort -V | uniq | tr '\n' ' ')
fi

# Create main repository index
log "Creating main repository index..."
cat > "$REPO_OUTPUT_DIR/index.html" << EOF
<!DOCTYPE html>
<html>
<head>
    <title>FlightCtl RPM Repository</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            line-height: 1.6;
            color: #333;
            max-width: 1000px;
            margin: 0 auto;
            padding: 20px;
            background-color: #f8f9fa;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 30px;
            border-radius: 10px;
            margin-bottom: 30px;
            text-align: center;
        }
        .header h1 { margin: 0; font-size: 2.5em; }
        .instructions {
            background: white;
            border-radius: 10px;
            padding: 25px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            margin: 20px 0;
        }
        .code-block {
            background: #f1f3f4;
            border: 1px solid #e1e5e9;
            border-radius: 6px;
            padding: 16px;
            margin: 10px 0;
            font-family: 'SFMono-Regular', Consolas, monospace;
            font-size: 14px;
            overflow-x: auto;
        }
        .platforms {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(300px, 1fr));
            gap: 20px;
            margin: 30px 0;
        }
        .platform-card {
            background: white;
            border-radius: 10px;
            padding: 20px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            transition: transform 0.2s;
        }
        .platform-card:hover { transform: translateY(-5px); }
        .platform-card h3 {
            margin-top: 0;
            color: #667eea;
            border-bottom: 2px solid #eee;
            padding-bottom: 10px;
        }
        .platform-card a {
            display: inline-block;
            background: #667eea;
            color: white;
            padding: 10px 20px;
            text-decoration: none;
            border-radius: 5px;
            margin-top: 10px;
            transition: background 0.2s;
        }
        .platform-card a:hover { background: #5a67d8; }
        .version-badge {
            display: inline-block;
            background: #6c757d;
            color: white;
            padding: 4px 8px;
            border-radius: 12px;
            font-size: 0.8em;
            margin: 2px;
        }
        .stats {
            background: #e8f4fd;
            border-radius: 10px;
            padding: 20px;
            margin: 20px 0;
        }
        .footer {
            text-align: center;
            margin-top: 30px;
            padding: 20px;
            color: #666;
            font-size: 0.9em;
        }
    </style>
</head>
<body>
    <div class="header">
        <h1>🚁 FlightCtl RPM Repository</h1>
        <p>Production-ready RPM packages for FlightCtl</p>
    </div>

    <div class="instructions">
        <h2>Installation</h2>

        <h3>EPEL (RHEL 9, CentOS Stream 9, Rocky Linux 9)</h3>
        <div class="code-block">sudo dnf config-manager addrepo --from-repofile=https://flightctl.github.io/flightctl-rpm/flightctl-epel.repo<br />sudo dnf install flightctl-agent flightctl-cli</div>

        <h3>Fedora</h3>
        <div class="code-block">sudo dnf config-manager addrepo --from-repofile=https://flightctl.github.io/flightctl-rpm/flightctl-fedora.repo<br />sudo dnf install flightctl-agent flightctl-cli</div>

        <h3>Install Specific Version</h3>
        <div class="code-block">sudo dnf install flightctl-agent-$LATEST_VERSION flightctl-cli-$LATEST_VERSION</div>
    </div>

    <div class="stats">
        <h3>Repository Overview</h3>
        <p><strong>Available versions:</strong></p>
        <div style="margin-top: 10px;">
EOF

# Add version badges
if [ -n "$versions" ]; then
    for version in $versions; do
        echo "            <span class=\"version-badge\">$version</span>" >> "$REPO_OUTPUT_DIR/index.html"
    done
fi

cat >> "$REPO_OUTPUT_DIR/index.html" << EOF
        </div>
        <p><strong>Packages:</strong> flightctl-agent, flightctl-cli</p>
    </div>

    <h2>Available Platforms</h2>
    <div class="platforms">
EOF

# Create platform cards and individual platform pages
for platform_dir in "$REPO_OUTPUT_DIR"/*/; do
    if [ -d "$platform_dir" ]; then
        platform=$(basename "$platform_dir")

        # Skip if it's not a platform directory
        if [[ "$platform" == "repodata" ]]; then
            continue
        fi

        platform_rpms=$(find "$platform_dir" -name "*.rpm" | wc -l)
        display_name=$(echo "$platform" | sed 's/-/ /g' | sed 's/\b\w/\U&/g')

        # Add platform card
        cat >> "$REPO_OUTPUT_DIR/index.html" << EOF
        <div class="platform-card">
            <h3>$display_name</h3>
            <p>$platform_rpms packages available</p>
            <a href="$platform/">View Packages</a>
        </div>
EOF

        # Create individual platform page
        log "Creating platform page for $platform..."
        cat > "$platform_dir/index.html" << EOF
<!DOCTYPE html>
<html>
<head>
    <title>FlightCtl RPMs - $display_name</title>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <style>
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            line-height: 1.6;
            color: #333;
            max-width: 800px;
            margin: 0 auto;
            padding: 20px;
            background-color: #f8f9fa;
        }
        .header {
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            padding: 20px;
            border-radius: 10px;
            margin-bottom: 20px;
        }
        .header h1 { margin: 0; }
        .back-link {
            display: inline-block;
            background: rgba(255,255,255,0.2);
            color: white;
            padding: 8px 16px;
            text-decoration: none;
            border-radius: 5px;
            margin-bottom: 15px;
        }
        .back-link:hover { background: rgba(255,255,255,0.3); }
        .package-list {
            background: white;
            border-radius: 10px;
            padding: 25px;
            box-shadow: 0 4px 6px rgba(0, 0, 0, 0.1);
            margin: 20px 0;
        }
        .package-list h2 {
            margin-top: 0;
            color: #667eea;
            border-bottom: 2px solid #eee;
            padding-bottom: 10px;
        }
        .rpm-item {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 12px 0;
            border-bottom: 1px solid #eee;
        }
        .rpm-item:last-child { border-bottom: none; }
        .rpm-info {
            flex-grow: 1;
        }
        .rpm-name {
            font-family: 'SFMono-Regular', Consolas, monospace;
            color: #333;
            font-weight: 500;
            font-size: 1.1em;
        }
        .rpm-version {
            color: #667eea;
            font-weight: 600;
            font-size: 0.9em;
        }
        .download-link {
            background: #667eea;
            color: white;
            padding: 6px 12px;
            text-decoration: none;
            border-radius: 4px;
            font-size: 0.9em;
            transition: background 0.2s;
        }
        .download-link:hover { background: #5a67d8; }
    </style>
</head>
<body>
    <div class="header">
        <a href="../" class="back-link">← Back to Repository</a>
        <h1>FlightCtl RPMs - $display_name</h1>
        <p>$platform_rpms packages available</p>
    </div>

    <div class="package-list">
        <h2>Available Packages</h2>
EOF

        # List RPMs in sorted order
        find "$platform_dir" -name "*.rpm" -exec basename {} \; | sort | while read rpm_file; do
            # Extract package info
            package_name=$(echo "$rpm_file" | sed -E 's/^([^-]+-[^-]+)-[0-9]+\.[0-9]+\.[0-9]+.*/\1/')
            if [[ "$package_name" == *"-"*"-"* ]] || [[ "$package_name" == "$rpm_file" ]]; then
                package_name=$(echo "$rpm_file" | sed -E 's/^([^-]+)-[0-9]+\.[0-9]+\.[0-9]+.*/\1/')
            fi

            version=$(rpm -qp --qf "%{VERSION}\n" "$platform_dir/$rpm_file")

            cat >> "$platform_dir/index.html" << EOF
        <div class="rpm-item">
            <div class="rpm-info">
                <div class="rpm-name">$package_name</div>
                <div class="rpm-version">$version</div>
            </div>
            <a href="$rpm_file" class="download-link">Download</a>
        </div>
EOF
        done

        cat >> "$platform_dir/index.html" << EOF
    </div>

    <div style="text-align: center; margin-top: 30px; color: #666; font-size: 0.9em;">
        <p>Generated on $(date -u '+%Y-%m-%d %H:%M:%S UTC')</p>
    </div>
</body>
</html>
EOF
    fi
done

# Close the main repository index
cat >> "$REPO_OUTPUT_DIR/index.html" << EOF
    </div>

    <div class="footer">
        <p><strong>Last updated:</strong> $(date -u '+%Y-%m-%d %H:%M:%S UTC')</p>
        <p><strong>Latest version:</strong> FlightCtl $LATEST_VERSION</p>
        <p><strong>Source:</strong> <a href="https://github.com/$REPO_OWNER/$REPO_NAME">$REPO_OWNER/$REPO_NAME</a></p>
    </div>
</body>
</html>
EOF

# Create README.md for the repository
log "Creating README.md..."
cat > "$REPO_OUTPUT_DIR/README.md" << EOF
# FlightCtl RPM Repository

This repository contains production-ready RPM packages for FlightCtl.

## Installation

### EPEL (RHEL 9, CentOS Stream 9, Rocky Linux 9)

\`\`\`bash
sudo dnf config-manager addrepo --from-repofile=https://flightctl.github.io/flightctl-rpm/flightctl-epel.repo
sudo dnf install flightctl-agent flightctl-cli
\`\`\`

### Fedora

\`\`\`bash
sudo dnf config-manager addrepo --from-repofile=https://flightctl.github.io/flightctl-rpm/flightctl-fedora.repo
sudo dnf install flightctl-agent flightctl-cli
\`\`\`

### Install Specific Version

\`\`\`bash
sudo dnf install flightctl-agent-$LATEST_VERSION flightctl-cli-$LATEST_VERSION
\`\`\`

## Available Packages

- **flightctl-agent**: FlightCtl agent for edge devices
- **flightctl-cli**: FlightCtl command-line interface

## Available Versions

$versions

## Repository Structure

This repository is automatically generated from COPR builds. Each platform directory contains:

- RPM packages for that platform
- Repository metadata (\`repodata/\`)
- Platform-specific index page

## Updates

This repository is automatically updated when new FlightCtl releases are published. PRs are created automatically and auto-merged after successful builds.

## Source

Generated from: https://github.com/$REPO_OWNER/$REPO_NAME
EOF

# Summary
success "RPM repository structure created successfully!"
echo ""
echo "Repository Summary:"
echo "  URL: https://flightctl.github.io/flightctl-rpm/"
echo "  Total packages: $total_rpms"
echo "  Output directory: $REPO_OUTPUT_DIR"
echo "  Latest version: $LATEST_VERSION"
echo ""
echo "Structure created:"
echo "  - index.html (main repository page)"
echo "  - flightctl-epel.repo, flightctl-fedora.repo (repository configs)"
echo "  - README.md (repository documentation)"
echo "  - Platform directories with RPMs and metadata"
echo ""
echo "Ready for PR to flightctl/flightctl-rpm repository!"
