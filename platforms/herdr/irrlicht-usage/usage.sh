#!/bin/sh
# The `usage` action of Irrlicht's herdr plugin (#2061): find irrlicht-ls and
# run its herdr usage mode, which reads HERDR_PANE_ID (and HERDR_SOCKET_PATH)
# from the environment herdr gives this action and prints one schema-1
# document on stdout.
#
# Which irrlicht-ls runs, in order:
#   1. $IRRLICHT_LS, when set — and only it: a path someone set on purpose
#      that is not executable is an error, never a cue to run another binary;
#   2. irrlicht-ls on PATH;
#   3. the places the Irrlicht installer and app link it, then the app bundle.
# Nothing found: one line on stderr and exit 127, which herdr records as the
# action's failure and Factory reports as unknown usage.

if [ -n "${IRRLICHT_LS:-}" ]; then
  if [ -x "$IRRLICHT_LS" ]; then
    exec "$IRRLICHT_LS" --herdr-usage
  fi
  echo "irrlicht-usage: IRRLICHT_LS=$IRRLICHT_LS is not an executable file" >&2
  exit 127
fi

if command -v irrlicht-ls >/dev/null 2>&1; then
  exec irrlicht-ls --herdr-usage
fi

for candidate in /usr/local/bin/irrlicht-ls /opt/homebrew/bin/irrlicht-ls \
  /Applications/Irrlicht.app/Contents/MacOS/irrlicht-ls; do
  if [ -x "$candidate" ]; then
    exec "$candidate" --herdr-usage
  fi
done

echo "irrlicht-usage: irrlicht-ls not found; install it from Irrlicht's settings, or set IRRLICHT_LS" >&2
exit 127
