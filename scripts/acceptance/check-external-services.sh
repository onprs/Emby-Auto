#!/usr/bin/env bash
set -euo pipefail

: "${EMBY_API_KEY:?EMBY_API_KEY is required}"
: "${TMDB_API_TOKEN:?TMDB_API_TOKEN is required}"

EMBY_BASE_URL="${EMBY_BASE_URL:-http://127.0.0.1:8096/emby}"
TMDB_BASE_URL="${TMDB_BASE_URL:-https://api.themoviedb.org/3}"
EMBY_BASE_URL="${EMBY_BASE_URL%/}"
TMDB_BASE_URL="${TMDB_BASE_URL%/}"

temporary="$(mktemp -d)"
trap 'rm -rf "${temporary}"' EXIT

curl --fail --silent --show-error \
  -H "X-Emby-Token: ${EMBY_API_KEY}" \
  "${EMBY_BASE_URL}/System/Info" >"${temporary}/emby-info.json"
curl --fail --silent --show-error \
  -H "X-Emby-Token: ${EMBY_API_KEY}" \
  "${EMBY_BASE_URL}/Library/VirtualFolders" >"${temporary}/emby-libraries.json"
curl --fail --silent --show-error \
  -H "Authorization: Bearer ${TMDB_API_TOKEN}" \
  "${TMDB_BASE_URL}/configuration" >"${temporary}/tmdb-configuration.json"
curl --fail --silent --show-error --get \
  -H "Authorization: Bearer ${TMDB_API_TOKEN}" \
  --data-urlencode "query=Frieren: Beyond Journey's End" \
  --data-urlencode "include_adult=false" \
  "${TMDB_BASE_URL}/search/tv" >"${temporary}/tmdb-search.json"

node - "${temporary}" <<'NODE'
const fs = require('node:fs');
const path = require('node:path');
const root = process.argv[2];
const read = (name) => JSON.parse(fs.readFileSync(path.join(root, name), 'utf8'));
const emby = read('emby-info.json');
const libraries = read('emby-libraries.json');
const tmdb = read('tmdb-configuration.json');
const search = read('tmdb-search.json');
if (typeof emby.Version !== 'string' || !Array.isArray(libraries)) {
  throw new Error('Emby authenticated responses have an unexpected shape');
}
if (typeof tmdb.images?.secure_base_url !== 'string' || !Array.isArray(search.results)) {
  throw new Error('TMDb authenticated responses have an unexpected shape');
}
console.log(`emby_version=${emby.Version} libraries=${libraries.length}`);
console.log(`tmdb_images=ok search_results=${search.results.length}`);
NODE
