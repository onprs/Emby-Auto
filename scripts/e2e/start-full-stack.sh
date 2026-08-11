#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
RUNTIME_DIR="${ROOT_DIR}/runtime/e2e"
BIN_DIR="${RUNTIME_DIR}/bin"
LOG_DIR="${RUNTIME_DIR}/logs"
MEDIA_DIR="${RUNTIME_DIR}/media"
CONTROL_DIR="${RUNTIME_DIR}/control"
API_PORT="${E2E_API_PORT:-18081}"
FIXTURE_PORT="${E2E_FIXTURE_PORT:-19090}"
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

rm -rf "${RUNTIME_DIR}"
mkdir -p "${BIN_DIR}" "${LOG_DIR}" "${CONTROL_DIR}" "${MEDIA_DIR}"/{downloads,work,staging,library/anime,library/movies}

cleanup() {
  local exit_code=$?
  trap - EXIT INT TERM
  for pid in "${worker_pid:-}" "${api_pid:-}" "${fixture_pid:-}"; do
    if [[ -n "${pid}" ]]; then
      kill "${pid}" 2>/dev/null || true
    fi
  done
  sleep 0.5
  for pid in "${worker_pid:-}" "${api_pid:-}" "${fixture_pid:-}"; do
    if [[ -n "${pid}" ]] && kill -0 "${pid}" 2>/dev/null; then
      kill -9 "${pid}" 2>/dev/null || true
    fi
  done
  wait 2>/dev/null || true
  TEST_DATABASE_URL="${BASE_DATABASE_URL}" E2E_DATABASE_NAME="${DATABASE_NAME}" \
    "${ROOT_DIR}/scripts/e2e/cleanup-full-stack.sh" >/dev/null 2>&1 || true
  exit "${exit_code}"
}
trap cleanup EXIT INT TERM

DATABASE_URL="$(go -C "${ROOT_DIR}/backend" run ./cmd/e2e-database --action reset --base-url "${BASE_DATABASE_URL}" --name "${DATABASE_NAME}")"

go -C "${ROOT_DIR}/backend" build -o "${BIN_DIR}/api" ./cmd/api
go -C "${ROOT_DIR}/backend" build -o "${BIN_DIR}/worker" ./cmd/worker
go -C "${ROOT_DIR}/backend" build -o "${BIN_DIR}/fixture" ./cmd/e2e-fixture
go -C "${ROOT_DIR}/backend" build -o "${BIN_DIR}/media-tool.exe" ./cmd/e2e-media-tool

native_runtime="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${RUNTIME_DIR}")"
native_downloads="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${MEDIA_DIR}/downloads")"
native_work="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${MEDIA_DIR}/work")"
native_staging="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${MEDIA_DIR}/staging")"
native_anime_library="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${MEDIA_DIR}/library/anime")"
native_movie_library="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${MEDIA_DIR}/library/movies")"
native_media_tool="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${BIN_DIR}/media-tool.exe")"
native_control="$(node -e 'console.log(require("node:path").resolve(process.argv[1]))' "${CONTROL_DIR}")"

"${BIN_DIR}/fixture" --address "127.0.0.1:${FIXTURE_PORT}" \
  --anime-library-root "${native_anime_library}" --movie-library-root "${native_movie_library}" \
  --control-dir "${native_control}" >"${LOG_DIR}/fixture.log" 2>&1 &
fixture_pid=$!

export DATABASE_URL
export API_ADDRESS="127.0.0.1:${API_PORT}"
export BOOTSTRAP_CONFIG_PATH="${native_runtime}\\bootstrap.json"
export CONFIG_ENCRYPTION_KEY="MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
export SESSION_COOKIE_SECURE="false"
export SESSION_TTL="1h"
export OPERATION_HEARTBEAT_INTERVAL="250ms"
export DOWNLOAD_SYNC_INTERVAL="250ms"
export RIVER_GENERAL_WORKERS="12"
export RIVER_TRANSCODE_WORKERS="2"
export RSS_SCHEDULE_CONCURRENCY="2"
export TMDB_BASE_URL="http://127.0.0.1:${FIXTURE_PORT}/tmdb"
export SEARCH_PROVIDER_NAME="fixture"
export SEARCH_PROVIDER_URL_TEMPLATE="http://127.0.0.1:${FIXTURE_PORT}/search?query={query}"
export E2E_CONTROL_DIR="${native_control}"

start_api() {
  "${BIN_DIR}/api" >>"${LOG_DIR}/api.log" 2>&1 &
  api_pid=$!
}
start_api

wait_for_url() {
  local url=$1
  for _ in $(seq 1 120); do
    if curl --fail --silent "${url}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 0.25
  done
  echo "timed out waiting for ${url}" >&2
  return 1
}

wait_for_url "http://127.0.0.1:${FIXTURE_PORT}/health"
wait_for_url "http://127.0.0.1:${API_PORT}/api/v1/health/live"

E2E_FIXTURE_URL="http://127.0.0.1:${FIXTURE_PORT}" \
E2E_DOWNLOADS="${native_downloads}" E2E_WORK="${native_work}" E2E_STAGING="${native_staging}" \
E2E_ANIME_LIBRARY="${native_anime_library}" E2E_MOVIE_LIBRARY="${native_movie_library}" E2E_MEDIA_TOOL="${native_media_tool}" \
node -e '
  const env = process.env;
  process.stdout.write(JSON.stringify({
    administrator: { username: "admin", password: "password123" },
    configuration: {
      qBittorrent: { url: env.E2E_FIXTURE_URL, username: "fixture", password: "fixture-password" },
      emby: { url: `${env.E2E_FIXTURE_URL}/emby`, apiKey: "fixture-key" },
      tmdb: { apiToken: "fixture-token" },
      paths: {
        downloadRoot: env.E2E_DOWNLOADS,
        workRoot: env.E2E_WORK,
        stagingRoot: env.E2E_STAGING,
        animeLibraryRoot: env.E2E_ANIME_LIBRARY,
        movieLibraryRoot: env.E2E_MOVIE_LIBRARY,
        ffmpegPath: env.E2E_MEDIA_TOOL,
        ffprobePath: env.E2E_MEDIA_TOOL,
      },
      transcode: {
        name: "e2e-h264", videoCodec: "h264", encoder: "libx264", container: "mp4",
        fileExtension: "mp4", qualityMode: "crf", qualityValue: 20,
        audioPolicy: "transcode", audioCodec: "aac", preset: "medium",
        pixelFormat: "yuv420p", threadCount: 0, maxConcurrency: 2,
      },
    },
  }));
' | curl --fail --silent --show-error \
  --header 'Content-Type: application/json' \
  --data-binary @- \
  "http://127.0.0.1:${API_PORT}/api/v1/setup/initialize" | grep -q '"state":"completed"'

curl --fail --silent "http://127.0.0.1:${API_PORT}/api/v1/setup/status" | grep -q '"state":"completed"'

"${BIN_DIR}/worker" >"${LOG_DIR}/worker.log" 2>&1 &
worker_pid=$!

E2E_MANIFEST_PATH="${RUNTIME_DIR}/manifest.json" \
E2E_API_PORT="${API_PORT}" E2E_FIXTURE_PORT="${FIXTURE_PORT}" \
E2E_DOWNLOADS="${native_downloads}" E2E_WORK="${native_work}" E2E_STAGING="${native_staging}" \
E2E_ANIME_LIBRARY="${native_anime_library}" E2E_MOVIE_LIBRARY="${native_movie_library}" E2E_MEDIA_TOOL="${native_media_tool}" \
node - <<'NODE'
const fs = require('node:fs');
const manifest = {
  apiURL: `http://127.0.0.1:${process.env.E2E_API_PORT}`,
  fixtureURL: `http://127.0.0.1:${process.env.E2E_FIXTURE_PORT}`,
  username: 'admin',
  password: 'password123',
  paths: {
    downloadRoot: process.env.E2E_DOWNLOADS,
    workRoot: process.env.E2E_WORK,
    stagingRoot: process.env.E2E_STAGING,
    animeLibraryRoot: process.env.E2E_ANIME_LIBRARY,
    movieLibraryRoot: process.env.E2E_MOVIE_LIBRARY,
    ffmpegPath: process.env.E2E_MEDIA_TOOL,
    ffprobePath: process.env.E2E_MEDIA_TOOL,
  },
};
fs.writeFileSync(process.env.E2E_MANIFEST_PATH, JSON.stringify(manifest, null, 2));
NODE

echo "full-stack E2E ready on API port ${API_PORT}"
while kill -0 "${worker_pid}" "${fixture_pid}" 2>/dev/null; do
  if [[ -f "${CONTROL_DIR}/api_restart" ]]; then
    kill "${api_pid}" 2>/dev/null || true
    wait "${api_pid}" 2>/dev/null || true
    sleep 3
    start_api
    wait_for_url "http://127.0.0.1:${API_PORT}/api/v1/health/live"
    rm -f "${CONTROL_DIR}/api_restart"
    printf 'restarted\n' >"${CONTROL_DIR}/api_restarted"
  elif ! kill -0 "${api_pid}" 2>/dev/null; then
    echo "the full-stack E2E API exited unexpectedly" >&2
    exit 1
  fi
  sleep 0.25
done
echo "a full-stack E2E worker or fixture process exited unexpectedly" >&2
exit 1
