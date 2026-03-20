#!/bin/bash
# update-version.sh - Update version across all project components
#
# Usage: ./tools/update-version.sh <new-version>
# Example: ./tools/update-version.sh 0.2.0
#
# Updates:
#   - version.json (single source of truth)
#   - tools/build-release.sh
#   - All go.mod files (module version comment)
#   - platforms/macos/Package.swift (appVersion constant)

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

NEW_VERSION="$1"

# Validate argument
if [ -z "$NEW_VERSION" ]; then
    echo "Usage: $0 <new-version>" >&2
    echo "Example: $0 0.2.0" >&2
    exit 1
fi

# Validate semver format (major.minor.patch with optional pre-release)
if ! echo "$NEW_VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$'; then
    echo "Error: version must be semver format (e.g., 1.2.3 or 1.2.3-beta.1)" >&2
    exit 1
fi

echo "Updating version to $NEW_VERSION..."

# 1. Update version.json
VERSION_JSON="$REPO_ROOT/version.json"
if [ ! -f "$VERSION_JSON" ]; then
    echo "Error: $VERSION_JSON not found" >&2
    exit 1
fi
# Use python3 as it's available on macOS without extra deps
python3 -c "
import json, sys
with open('$VERSION_JSON', 'r') as f:
    data = json.load(f)
data['version'] = '$NEW_VERSION'
with open('$VERSION_JSON', 'w') as f:
    json.dump(data, f, indent=2)
    f.write('\n')
"
echo "  updated version.json"

# 2. Update go.mod version comment (// app-version: x.y.z)
# This comment is a project convention for tracking app version in go.mod files.
for gomod in $(find "$REPO_ROOT/tools" "$REPO_ROOT/core" -name "go.mod"); do
    rel="${gomod#$REPO_ROOT/}"
    python3 -c "
import re
path = '$gomod'
ver = '$NEW_VERSION'
with open(path, 'r') as f:
    content = f.read()

if '// app-version:' in content:
    content = re.sub(r'// app-version:.*', f'// app-version: {ver}', content)
    action = 'version comment'
else:
    content = re.sub(r'(^module [^\n]*\n)', r'\1// app-version: ' + ver + r'\n', content, count=1, flags=re.MULTILINE)
    action = 'added version comment'

with open(path, 'w') as f:
    f.write(content)

print(f'  updated $rel ({action})')
"
done

# 3. Update Package.swift appVersion constant
PACKAGE_SWIFT="$REPO_ROOT/platforms/macos/Package.swift"
if [ -f "$PACKAGE_SWIFT" ]; then
    python3 -c "
import re, sys
path = '$PACKAGE_SWIFT'
ver = '$NEW_VERSION'
with open(path, 'r') as f:
    content = f.read()

if 'let appVersion' in content:
    content = re.sub(r'let appVersion = \"[^\"]*\"', f'let appVersion = \"{ver}\"', content)
    action = 'updated'
else:
    # Insert after swift-tools-version line
    content = re.sub(r'(// swift-tools-version:[^\n]*\n)', r'\1let appVersion = \"' + ver + r'\"\n', content, count=1)
    action = 'added'

with open(path, 'w') as f:
    f.write(content)

print(f'  {action} platforms/macos/Package.swift (appVersion)')
"
fi

echo ""
echo "Version updated to $NEW_VERSION"
echo ""
echo "Files changed:"
echo "  version.json"
for gomod in $(find "$REPO_ROOT/tools" "$REPO_ROOT/core" -name "go.mod"); do
    echo "  ${gomod#$REPO_ROOT/}"
done
echo "  platforms/macos/Package.swift"
echo ""
echo "Next steps:"
echo "  git add -A && git commit -m \"chore: bump version to $NEW_VERSION\""
echo "  git tag v$NEW_VERSION"
