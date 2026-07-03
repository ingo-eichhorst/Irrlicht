#!/usr/bin/env bash
# codescene-report.sh — fetch a CodeScene cloud analysis report for this
# repo's connected project (api.codescene.io/v2), for agents/scripts to read.
#
# Requires:
#   CODESCENE_API_TOKEN    Bearer token from the CodeScene dashboard's API
#                          tokens page (repo secret in CI; never commit it).
#   CODESCENE_PROJECT_ID   This repo's CodeScene project id (82148 for
#                          ingo-eichhorst/Irrlicht).
#
# Usage:
#   tools/codescene-report.sh                        # latest analysis overview
#   tools/codescene-report.sh analyses/123/components # any endpoint under
#                                                      # /v2/projects/<id>/
set -euo pipefail

: "${CODESCENE_API_TOKEN:?CODESCENE_API_TOKEN env var required}"
: "${CODESCENE_PROJECT_ID:?CODESCENE_PROJECT_ID env var required}"

ENDPOINT="${1:-analyses/latest}"
API_BASE="https://api.codescene.io/v2/projects/${CODESCENE_PROJECT_ID}"

curl -sf -H "Authorization: Bearer ${CODESCENE_API_TOKEN}" "${API_BASE}/${ENDPOINT}" | jq .
