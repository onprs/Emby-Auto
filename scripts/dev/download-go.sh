#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(CDPATH= cd -- "$script_dir/../.." && pwd)

GOWORK=off go -C "$repo_root/backend" mod download
GOWORK=off go -C "$repo_root/tools" mod download
