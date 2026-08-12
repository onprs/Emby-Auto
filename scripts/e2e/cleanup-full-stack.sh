#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
DATABASE_NAME="${E2E_DATABASE_NAME:-emby_auto_e2e}"

set -a
if [[ -f "${ROOT_DIR}/.env" ]]; then
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env"
else
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/.env.example"
fi
set +a
BASE_DATABASE_URL="${TEST_DATABASE_URL:-${DATABASE_URL:-}}"
if [[ -z "${BASE_DATABASE_URL}" ]]; then
  echo "TEST_DATABASE_URL or DATABASE_URL is required" >&2
  exit 2
fi

go -C "${ROOT_DIR}/backend" run ./cmd/e2e-database \
  --action drop --base-url "${BASE_DATABASE_URL}" --name "${DATABASE_NAME}" >/dev/null
