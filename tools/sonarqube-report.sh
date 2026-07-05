#!/usr/bin/env bash
# sonarqube-report.sh — fetch a SonarQube Cloud (sonarcloud.io) analysis
# report for this repo's connected project, for agents/scripts to read.
# Unlike CodeScene's API, this runs entirely locally: SONAR_TOKEN never
# needs to be a CI secret, so there's no GitHub Actions round-trip.
#
# Requires (auto-loaded from a repo-root .env if present, or already
# exported):
#   SONAR_TOKEN          Personal access token from SonarQube Cloud's
#                         account security settings — never commit it.
#   SONAR_ORGANIZATION   This repo's SonarQube Cloud organization key.
#   SONAR_PROJECT_KEY    This repo's SonarQube Cloud project key.
#
# Usage:
#   tools/sonarqube-report.sh                    # open issues, worst first
#   tools/sonarqube-report.sh "rules/show?key=go:S1192"  # any GET endpoint
#                                                  # under /api/ (with its
#                                                  # own querystring)
#
# Note: SonarQube Cloud has no equivalent of CodeScene's `run-analysis` —
# fresh data depends on a scanner (sonar-scanner / CI action) having run
# against this project already. This script only reads whatever the last
# scan uploaded.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -f "${REPO_ROOT}/.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${REPO_ROOT}/.env"
  set +a
fi

: "${SONAR_TOKEN:?SONAR_TOKEN env var required (set it in .env or export it)}"
: "${SONAR_ORGANIZATION:?SONAR_ORGANIZATION env var required (set it in .env or export it)}"
: "${SONAR_PROJECT_KEY:?SONAR_PROJECT_KEY env var required (set it in .env or export it)}"

API_BASE="https://sonarcloud.io/api"
DEFAULT_ENDPOINT="issues/search?organization=${SONAR_ORGANIZATION}&componentKeys=${SONAR_PROJECT_KEY}&resolved=false&ps=100&s=SEVERITY&asc=false"
ENDPOINT="${1:-$DEFAULT_ENDPOINT}"

curl -sf -H "Authorization: Bearer ${SONAR_TOKEN}" "${API_BASE}/${ENDPOINT}" | jq .
