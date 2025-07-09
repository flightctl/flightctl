#!/usr/bin/env bash

# Create GitHub Pages Structure Script
# Converts COPR downloads into a GitHub Pages RPM repository

set -euo pipefail

# Arguments
COPR_DOWNLOAD_DIR="${1:-copr-rpms-temp}"
PAGES_OUTPUT_DIR="${2:-pages-content}"
REPO_OWNER="${3:-}"
REPO_NAME="${4:-}"
# VERSION will be auto-detected from RPMs

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

log() { echo -e "${BLUE}[INFO]${NC} $1"; }
success() { echo -e "${GREEN}✅${NC} $1"; }
error() { echo -e "${RED}❌${NC} $1"; }

# Show usage
if [ -z "$REPO_OWNER" ] || [ -z "$REPO_NAME" ]; then
    echo "Usage: $0 <copr_download_dir> <pages_output_dir> <repo_owner> <repo_name>"
    echo "Example: $0 copr-rpms-temp pages-content flightctl flightctl"
    echo ""
    echo "The script will automatically detect the version from downloaded RPMs."
    exit 1
fi

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

# Auto-detect the latest version from new downloads
log "Auto-detecting version from downloaded RPMs..."
LATEST_VERSION="unknown"
if [ $rpm_count -gt 0 ]; then
    LATEST_VERSION=$(find "$COPR_DOWNLOAD_DIR" -name "*.rpm" -exec basename {} \; | \
        sed -E 's/.*-([0-9]+\.[0-9]+\.[0-9]+[^-]*)-[0-9]+\..*/\1/' | \
        sort -V | tail -1)
fi

if [ "$LATEST_VERSION" = "unknown" ]; then
    error "Could not detect version from RPM files"
    exit 1
fi

log "Detected latest version: $LATEST_VERSION"

# Create/update directory structure (preserve existing content, copy only RPMs)
log "Updating GitHub Pages structure..."
mkdir -p "$PAGES_OUTPUT_DIR"

# Copy only RPM files, preserving directory structure
log "Copying RPM files from $COPR_DOWNLOAD_DIR..."
for platform_dir in "$COPR_DOWNLOAD_DIR"/*/; do
    if [ -d "$platform_dir" ]; then
        platform=$(basename "$platform_dir")
        target_dir="$PAGES_OUTPUT_DIR/$platform"

        # Create platform directory
        mkdir -p "$target_dir"

        # Copy only RPM files
        find "$platform_dir" -name "*.rpm" -exec cp {} "$target_dir/" \;

        # Copy repodata if it exists
        if [ -d "$platform_dir/repodata" ]; then
            cp -r "$platform_dir/repodata" "$target_dir/"
        fi

        rpm_count=$(find "$target_dir" -name "*.rpm" | wc -l)
        log "  $platform: copied $rpm_count RPM files"
    fi
done

# Create platform-specific repository files
log "Creating platform-specific repository files..."
cat > "$PAGES_OUTPUT_DIR/flightctl-centos9.repo" << EOF
[flightctl]
name=FlightCtl RPM Repository (CentOS Stream 9)
baseurl=https://$REPO_OWNER.github.io/$REPO_NAME/centos-stream-9-\$basearch/
enabled=1
gpgcheck=0
repo_gpgcheck=0
type=rpm
metadata_expire=1d
EOF

cat > "$PAGES_OUTPUT_DIR/flightctl-fedora.repo" << EOF
[flightctl]
name=FlightCtl RPM Repository (Fedora)
baseurl=https://$REPO_OWNER.github.io/$REPO_NAME/fedora-\$releasever-\$basearch/
enabled=1
gpgcheck=0
repo_gpgcheck=0
type=rpm
metadata_expire=1d
EOF

# Create repository configuration file
log "Creating repository configuration..."
cat > "$PAGES_OUTPUT_DIR/flightctl.repo" << EOF
[flightctl]
name=FlightCtl RPM Repository
baseurl=https://$REPO_OWNER.github.io/$REPO_NAME/\$basearch/
enabled=1
gpgcheck=0
repo_gpgcheck=0
type=rpm
metadata_expire=1d
EOF

# Create platform-agnostic symbolic directories for DNF compatibility
log "Creating platform-agnostic directories for DNF compatibility..."
mkdir -p "$PAGES_OUTPUT_DIR/x86_64"
mkdir -p "$PAGES_OUTPUT_DIR/aarch64"

# Copy all RPMs and create unified metadata for each architecture
for arch in x86_64 aarch64; do
    arch_dir="$PAGES_OUTPUT_DIR/$arch"

    # Copy RPMs from all platforms of this architecture
    for platform_dir in "$PAGES_OUTPUT_DIR"/*-"$arch"/; do
        if [ -d "$platform_dir" ]; then
            platform=$(basename "$platform_dir")
            if [[ "$platform" != "repodata" ]]; then
                log "  Copying RPMs from $platform to $arch/"
                find "$platform_dir" -name "*.rpm" -exec cp {} "$arch_dir/" \; 2>/dev/null || true
            fi
        fi
    done

    # Create unified repository metadata if we have RPMs
    if [ -n "$(find "$arch_dir" -name "*.rpm" 2>/dev/null)" ]; then
        log "  Creating unified metadata for $arch"
        createrepo_c "$arch_dir" 2>/dev/null || {
            warning "Failed to create repo metadata for $arch"
            continue
        }
    else
        log "  No RPMs found for $arch, skipping metadata creation"
        rmdir "$arch_dir" 2>/dev/null || true
    fi
done

# Analyze repository content
total_rpms=$(find "$PAGES_OUTPUT_DIR" -name "*.rpm" | wc -l)
total_platforms=$(find "$PAGES_OUTPUT_DIR" -maxdepth 1 -type d ! -name "." ! -name "repodata" ! -name "$(basename "$PAGES_OUTPUT_DIR")" | wc -l)

# Extract all available versions
log "Analyzing available versions..."
versions=""
if [ $total_rpms -gt 0 ]; then
    versions=$(find "$PAGES_OUTPUT_DIR" -name "*.rpm" -exec basename {} \; | \
        sed -E 's/.*-([0-9]+\.[0-9]+\.[0-9]+[^-]*)-[0-9]+\..*/\1/' | \
        sort -V | uniq | tr '\n' ' ')
fi

# Create individual platform index.html files
log "Creating platform-specific index.html files..."
for platform_dir in "$PAGES_OUTPUT_DIR"/*/; do
    if [ -d "$platform_dir" ]; then
        platform=$(basename "$platform_dir")

        # Skip repodata directories
        if [[ "$platform" == "repodata" ]]; then
            continue
        fi

        platform_rpms=$(find "$platform_dir" -name "*.rpm" | wc -l)
        display_name=$(echo "$platform" | sed 's/-/ /g' | sed 's/\b\w/\U&/g')

        # Get all versions for this platform
        platform_versions=""
        if [ $platform_rpms -gt 0 ]; then
            platform_versions=$(find "$platform_dir" -name "*.rpm" -exec basename {} \; | \
                sed -E 's/.*-([0-9]+\.[0-9]+\.[0-9]+[^-]*)-[0-9]+\..*/\1/' | \
                sort -V | uniq | tr '\n' ' ')
        fi

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
            max-width: 1000px;
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
        .package-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(400px, 1fr));
            gap: 15px;
            margin-top: 20px;
        }
        .package-card {
            background: white;
            border-radius: 8px;
            padding: 15px;
            box-shadow: 0 2px 4px rgba(0,0,0,0.1);
        }
        .package-name {
            font-weight: bold;
            color: #667eea;
            margin-bottom: 5px;
        }
        .package-info {
            font-size: 0.9em;
            color: #666;
            margin-bottom: 10px;
        }
        .download-link {
            display: inline-block;
            background: #667eea;
            color: white;
            padding: 6px 12px;
            text-decoration: none;
            border-radius: 4px;
            font-size: 0.9em;
        }
        .download-link:hover { background: #5a67d8; }
        .stats {
            background: #e8f4fd;
            border-radius: 8px;
            padding: 15px;
            margin-bottom: 20px;
        }
        .version-badge {
            display: inline-block;
            background: #6c757d;
            color: white;
            padding: 2px 6px;
            border-radius: 8px;
            font-size: 0.8em;
            margin: 2px;
        }
    </style>
</head>
<body>
    <div class="header">
        <a href="../" class="back-link">← Back to Repository</a>
        <h1>FlightCtl RPMs - $display_name</h1>
        <p>$platform_rpms packages available</p>
    </div>

    <div class="stats">
        <h3>Available Versions</h3>
EOF

        # Add version badges
        if [ -n "$platform_versions" ]; then
            for version in $platform_versions; do
                echo "        <span class=\"version-badge\">$version</span>" >> "$platform_dir/index.html"
            done
        fi

        cat >> "$platform_dir/index.html" << EOF
    </div>

    <h2>Package Downloads</h2>
    <div class="package-grid">
EOF

        # List all RPM files
        find "$platform_dir" -name "*.rpm" -exec basename {} \; | sort | while read rpm_file; do
            # Extract package info - get the full package name without version/arch
            # Example: flightctl-cli-0.5.1-1.el9.x86_64.rpm -> flightctl-cli
            package_name=$(echo "$rpm_file" | sed -E 's/^([^-]+-[^-]+)-[0-9]+\.[0-9]+\.[0-9]+.*/\1/')

            # If the above doesn't work (simple package name), fall back to basic extraction
            if [[ "$package_name" == *"-"*"-"* ]] || [[ "$package_name" == "$rpm_file" ]]; then
                package_name=$(echo "$rpm_file" | sed -E 's/^([^-]+)-[0-9]+\.[0-9]+\.[0-9]+.*/\1/')
            fi

            version=$(echo "$rpm_file" | sed -E 's/.*-([0-9]+\.[0-9]+\.[0-9]+[^-]*)-[0-9]+\..*/\1/')

            # Get file size
            file_size=$(stat -c%s "$platform_dir/$rpm_file" 2>/dev/null || echo "0")
            size_mb=$(echo "scale=1; $file_size/1024/1024" | bc 2>/dev/null || echo "0.0")

            cat >> "$platform_dir/index.html" << EOF
        <div class="package-card">
            <div class="package-name">$package_name</div>
            <div class="package-info">
                Version: $version<br>
                Size: ${size_mb} MB<br>
                File: $rpm_file
            </div>
            <a href="$rpm_file" class="download-link">Download RPM</a>
        </div>
EOF
        done

        cat >> "$platform_dir/index.html" << EOF
    </div>

    <div style="margin-top: 30px; text-align: center; color: #666; font-size: 0.9em;">
        <p>Generated on $(date -u '+%Y-%m-%d %H:%M:%S UTC')</p>
    </div>
</body>
</html>
EOF

        log "  Created index.html for $platform"
    fi
done

# Create main index.html
log "Creating main index.html..."
cat > "$PAGES_OUTPUT_DIR/index.html" << EOF
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
            max-width: 1200px;
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
        .badge {
            display: inline-block;
            background: #28a745;
            color: white;
            padding: 4px 8px;
            border-radius: 12px;
            font-size: 0.8em;
            margin: 2px;
        }
        .version-badge {
            background: #6c757d;
            font-size: 0.7em;
        }
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
            grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
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
        .stats {
            background: #e8f4fd;
            border-radius: 10px;
            padding: 20px;
            margin: 20px 0;
        }
        .stats h3 { margin-top: 0; color: #667eea; }
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
        <div style="margin-top: 15px;">
            <strong>Available versions:</strong><br>
EOF

# Add version badges to header
if [ -n "$versions" ]; then
    for version in $versions; do
        echo "            <span class=\"version-badge badge\">$version</span>" >> "$PAGES_OUTPUT_DIR/index.html"
    done
else
    echo "            <span class=\"version-badge badge\">$LATEST_VERSION</span>" >> "$PAGES_OUTPUT_DIR/index.html"
fi

cat >> "$PAGES_OUTPUT_DIR/index.html" << EOF
        </div>
    </div>

    <div class="instructions">
        <h2>📋 Quick Setup</h2>
        <p>Add this repository to your system:</p>
        <div class="code-block">sudo dnf config-manager --add-repo https://$REPO_OWNER.github.io/$REPO_NAME/flightctl.repo<br />sudo dnf install flightctl</div>
    </div>

    <div class="stats">
        <h3>📊 Repository Overview</h3>
        <p><strong>Total packages:</strong> $total_rpms RPMs</p>
        <p><strong>Supported platforms:</strong> $total_platforms</p>
        <p><strong>Available versions:</strong></p>
        <div style="margin-top: 10px;">
EOF

# Add version badges
if [ -n "$versions" ]; then
    for version in $versions; do
        echo "            <span class=\"version-badge badge\">$version</span>" >> "$PAGES_OUTPUT_DIR/index.html"
    done
else
    echo "            <span class=\"version-badge badge\">$LATEST_VERSION</span>" >> "$PAGES_OUTPUT_DIR/index.html"
fi

cat >> "$PAGES_OUTPUT_DIR/index.html" << EOF
        </div>
    </div>

    <h2>🖥️ Supported Platforms</h2>
    <div class="platforms">
EOF

# Add platform cards with all versions
for platform_dir in "$PAGES_OUTPUT_DIR"/*/; do
    if [ -d "$platform_dir" ]; then
        platform=$(basename "$platform_dir")

        # Skip repodata directories
        if [[ "$platform" == "repodata" ]]; then
            continue
        fi

        platform_rpms=$(find "$platform_dir" -name "*.rpm" | wc -l)
        display_name=$(echo "$platform" | sed 's/-/ /g' | sed 's/\b\w/\U&/g')

        # Get all versions for this platform
        platform_versions=""
        if [ $platform_rpms -gt 0 ]; then
            platform_versions=$(find "$platform_dir" -name "*.rpm" -exec basename {} \; | \
                sed -E 's/.*-([0-9]+\.[0-9]+\.[0-9]+[^-]*)-[0-9]+\..*/\1/' | \
                sort -V | uniq | tr '\n' ' ')
        fi

        cat >> "$PAGES_OUTPUT_DIR/index.html" << EOF
        <div class="platform-card">
            <h3>$display_name</h3>
            <p><strong>$platform_rpms packages</strong></p>
            <p><strong>Versions:</strong></p>
            <div style="margin: 10px 0;">
EOF

        # Add version badges for this platform
        if [ -n "$platform_versions" ]; then
            for version in $platform_versions; do
                echo "                <span class=\"version-badge badge\">$version</span>" >> "$PAGES_OUTPUT_DIR/index.html"
            done
        fi

        cat >> "$PAGES_OUTPUT_DIR/index.html" << EOF
            </div>
            <a href="$platform/">Browse Packages</a>
        </div>
EOF
    fi
done

cat >> "$PAGES_OUTPUT_DIR/index.html" << EOF
    </div>

    <div class="instructions">
        <h2>🔧 Installation Options</h2>

        <h3>Via Repository (Recommended)</h3>
        <div class="code-block">sudo dnf config-manager --add-repo https://$REPO_OWNER.github.io/$REPO_NAME/flightctl.repo<br />sudo dnf install flightctl</div>

        <h3>Platform-Specific Repository</h3>
        <div class="code-block"># For CentOS Stream 9<br />sudo dnf config-manager --add-repo https://$REPO_OWNER.github.io/$REPO_NAME/flightctl-centos9.repo<br /><br /># For Fedora<br />sudo dnf config-manager --add-repo https://$REPO_OWNER.github.io/$REPO_NAME/flightctl-fedora.repo</div>

        <h3>Direct Download</h3>
        <div class="code-block"># CentOS Stream 9
sudo dnf install https://$REPO_OWNER.github.io/$REPO_NAME/centos-stream-9-x86_64/flightctl-*.rpm

# Fedora
sudo dnf install https://$REPO_OWNER.github.io/$REPO_NAME/fedora-*-x86_64/flightctl-*.rpm</div>

        <h3>Specific Version</h3>
        <div class="code-block"># Install specific version
sudo dnf install https://$REPO_OWNER.github.io/$REPO_NAME/centos-stream-9-x86_64/flightctl-$LATEST_VERSION-*.rpm</div>
    </div>

    <div class="footer">
        <p>🔄 <strong>Last updated:</strong> $(date -u '+%Y-%m-%d %H:%M:%S UTC')</p>
        <p>📦 <strong>Latest addition:</strong> FlightCtl $LATEST_VERSION</p>
        <p>🏗️ <strong>Repository:</strong> <a href="https://github.com/$REPO_OWNER/$REPO_NAME">$REPO_OWNER/$REPO_NAME</a></p>
    </div>
</body>
</html>
EOF

# Summary
success "GitHub Pages structure created successfully!"
echo ""
echo "📊 Repository Summary:"
echo "  🔗 URL: https://$REPO_OWNER.github.io/$REPO_NAME/"
echo "  📦 Total packages: $total_rpms"
echo "  🖥️  Platforms: $total_platforms"
echo "  📂 Output directory: $PAGES_OUTPUT_DIR"
echo "  🏷️  Latest version: $LATEST_VERSION"
echo ""
echo "🚀 Ready for deployment!"
