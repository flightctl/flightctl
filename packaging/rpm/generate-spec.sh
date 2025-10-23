#!/bin/bash
# Generate flightctl.spec from template and package modules

set -e

SCRIPT_DIR="$(dirname "$0")"
cd "$SCRIPT_DIR"

TEMPLATE="flightctl.spec.template"
OUTPUT="flightctl.spec"

echo "Generating $OUTPUT from $TEMPLATE..."

# Create output with warning header
cat > "$OUTPUT" << 'EOF'
# WARNING: THIS FILE IS AUTO-GENERATED - DO NOT EDIT MANUALLY
#
# This file is generated from flightctl.spec.template and package_*.spec files
# To make changes, edit the template or package files, then run:
#   make generate
# or:
#   cd packaging/rpm && ./generate-spec.sh
#

EOF

# Append template content
cat "$TEMPLATE" >> "$OUTPUT"

# Replace the include section with actual package contents
for package_file in package_*.spec; do
    if [ -f "$package_file" ]; then
        package_name=$(basename "$package_file" .spec | sed 's/^package_//')
        echo "Including $package_name from $package_file"

        # Replace the corresponding include line with file contents
        sed -i "/^%include_package -p $package_name$/r $package_file" "$OUTPUT"
        sed -i "/^%include_package -p $package_name$/d" "$OUTPUT"
    fi
done

echo "Generated $OUTPUT successfully"