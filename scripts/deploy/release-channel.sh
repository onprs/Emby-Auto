#!/usr/bin/env bash
set -euo pipefail

# 根据发布标签输出 GitHub Release 类型。正式版本 vX.Y.Z 更新 latest；
# 带预发布后缀的标签（如 v0.2.0-rc.1）只创建预发布，不改变 releases/latest。
# 用法：release-channel.sh <标签>

tag="${1:-}"
if [[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'prerelease=false\nrelease_flag=--latest\n'
elif [[ "${tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+-[0-9A-Za-z][0-9A-Za-z.-]*$ ]]; then
  printf 'prerelease=true\nrelease_flag=--prerelease\n'
else
  printf '无效的发布标签：%s；正式版本使用 vX.Y.Z，预发布使用 vX.Y.Z-后缀。\n' "${tag}" >&2
  exit 2
fi
