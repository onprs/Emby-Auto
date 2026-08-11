#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

if [ "$#" -eq 0 ]; then
  echo "usage: $0 tool [arguments...]" >&2
  exit 2
fi

tool_name=$1
shift

cd "$repo_root/backend"
if [ "$tool_name" = "migrate" ]; then
  GOWORK=off exec go run -modfile=../tools/go.mod -tags=postgres \
    github.com/golang-migrate/migrate/v4/cmd/migrate "$@"
fi
GOWORK=off exec go tool -modfile=../tools/go.mod "$tool_name" "$@"
