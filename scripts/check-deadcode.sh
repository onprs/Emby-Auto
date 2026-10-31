#!/usr/bin/env bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)

# 加载集成测试的调用关系，不执行测试或连接数据库。
report=$(bash "$script_dir/dev/go-tool.sh" deadcode -test -tags=integration ./...)
if [ -n "$report" ]; then
  printf '%s\n' "$report" >&2
  exit 1
fi
