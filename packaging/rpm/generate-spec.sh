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

# Process package files and extract build/install sections
build_sections=""
install_sections=""

for package_file in packages/*.spec; do
    if [ -f "$package_file" ]; then
        package_name=$(basename "$package_file" .spec)
        echo "Processing $package_name from $package_file"

        # Extract %build section if present
        if grep -q "^%build" "$package_file"; then
            build_content=$(awk '/^%build/{flag=1; next} /^%(install|files|pre|post|preun|postun|description|package)/{if(flag) exit} flag{print}' "$package_file")
            if [ -n "$build_content" ]; then
                build_sections="$build_sections
  # Build commands for $package_name
$build_content"
            fi
        fi

        # Extract %install section if present
        if grep -q "^%install" "$package_file"; then
            install_content=$(awk '/^%install/{flag=1; next} /^%(build|files|pre|post|preun|postun|description|package)/{if(flag) exit} flag{print}' "$package_file")
            if [ -n "$install_content" ]; then
                install_sections="$install_sections
  # Install commands for $package_name
$(echo "$install_content" | sed 's/^/  /')"
            fi
        fi

        # Remove build and install sections from package file content before including
        temp_package="/tmp/$(basename "$package_file")"
        awk '/^%build/{flag=1} /^%install/{flag=1} /^%(files|pre|post|preun|postun|description|package)/ && !/^%build/ && !/^%install/{if(flag) flag=0} !flag{print}' "$package_file" > "$temp_package"

        # Replace the corresponding include line with cleaned package contents
        sed -i "/^%include_package $package_name$/r $temp_package" "$OUTPUT"
        sed -i "/^%include_package $package_name$/d" "$OUTPUT"
        rm -f "$temp_package"
    fi
done

# Add aggregated build sections to %build section
if [ -n "$build_sections" ]; then
    echo "  # Execute modular build commands$build_sections" > /tmp/build_section.tmp
    # Replace just the build commands part, leaving %install intact
    sed -i '/# Execute modular build commands$/,/^$/c\
# REPLACE_BUILD_PLACEHOLDER' "$OUTPUT"
    sed -i '/# REPLACE_BUILD_PLACEHOLDER/r /tmp/build_section.tmp' "$OUTPUT"
    sed -i '/# REPLACE_BUILD_PLACEHOLDER/d' "$OUTPUT"
    rm -f /tmp/build_section.tmp
fi

# Add aggregated install sections to %install section
if [ -n "$install_sections" ]; then
    echo "  # Execute modular install commands$install_sections" > /tmp/install_section.tmp
    # Replace from "Execute modular install commands" through the macro calls
    sed -i '/# Execute modular install commands$/,/%{telemetry_gateway_install_commands}$/c\
# REPLACE_INSTALL_PLACEHOLDER' "$OUTPUT"
    sed -i '/# REPLACE_INSTALL_PLACEHOLDER/r /tmp/install_section.tmp' "$OUTPUT"
    sed -i '/# REPLACE_INSTALL_PLACEHOLDER/d' "$OUTPUT"
    rm -f /tmp/install_section.tmp
fi

echo "Generated $OUTPUT successfully"