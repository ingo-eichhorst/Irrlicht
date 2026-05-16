#!/usr/bin/env bash
# Regenerate platforms/macos/Irrlicht/Resources/AppIcon.icns from a source SVG.
#
# Usage:
#   tools/build-app-icon.sh                                    # default: gradient_purple
#   tools/build-app-icon.sh assets/irrlicht_flame_simple_white.svg
#
# Requires `rsvg-convert` (brew install librsvg) and `iconutil` (built-in).
# Falls back to `qlmanage` if rsvg-convert is missing, but qlmanage handles
# SVG filters poorly so the gradient flames will look flat. Install librsvg
# for production output.

set -euo pipefail

repo_root="$(cd "$(dirname "$0")/.." && pwd)"
src="${1:-$repo_root/assets/irrlicht_flame_gradient_purple.svg}"
dest="$repo_root/platforms/macos/Irrlicht/Resources/AppIcon.icns"

if [[ ! -f "$src" ]]; then
  echo "error: source SVG not found: $src" >&2
  exit 1
fi

if ! command -v iconutil >/dev/null; then
  echo "error: iconutil not found (run on macOS)" >&2
  exit 1
fi

renderer=""
if command -v rsvg-convert >/dev/null; then
  renderer="rsvg"
elif command -v qlmanage >/dev/null; then
  renderer="qlmanage"
  echo "warning: rsvg-convert not found; falling back to qlmanage (SVG filters may not render). Install with: brew install librsvg" >&2
else
  echo "error: no SVG renderer available. Install with: brew install librsvg" >&2
  exit 1
fi

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

iconset="$work/AppIcon.iconset"
mkdir -p "$iconset"

# Apple iconset spec: 5 logical sizes, each with @1x and @2x.
declare -a sizes=(
  "16:icon_16x16.png"
  "32:icon_16x16@2x.png"
  "32:icon_32x32.png"
  "64:icon_32x32@2x.png"
  "128:icon_128x128.png"
  "256:icon_128x128@2x.png"
  "256:icon_256x256.png"
  "512:icon_256x256@2x.png"
  "512:icon_512x512.png"
  "1024:icon_512x512@2x.png"
)

render() {
  local px="$1"
  local out="$2"
  case "$renderer" in
    rsvg)
      rsvg-convert -w "$px" -h "$px" "$src" -o "$out"
      ;;
    qlmanage)
      # qlmanage renders into its own filename derived from the input; redirect
      # via a temp dir and rename.
      local tmp="$work/ql_$px"
      mkdir -p "$tmp"
      qlmanage -t -s "$px" -o "$tmp" "$src" >/dev/null 2>&1
      mv "$tmp"/*.png "$out"
      ;;
  esac
}

echo "source:    $src"
echo "renderer:  $renderer"
echo "rendering $(( ${#sizes[@]} )) PNGs into $iconset"

for entry in "${sizes[@]}"; do
  px="${entry%%:*}"
  name="${entry##*:}"
  render "$px" "$iconset/$name"
done

iconutil -c icns "$iconset" -o "$dest"
echo "wrote: $dest"
ls -la "$dest"
