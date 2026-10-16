#!/usr/bin/env bash
set -euo pipefail
export PATH=/usr/sbin:/usr/bin:/sbin:/bin

worker_unit=emby-auto-worker.service

status() {
  local state pid
  state="$(systemctl show "$worker_unit" --property=ActiveState --value)"
  pid="$(systemctl show "$worker_unit" --property=MainPID --value)"
  case "$state" in
    active) [[ "$pid" =~ ^[1-9][0-9]*$ ]] && printf '%s\n' running ;;
    inactive|failed) [[ "$pid" == 0 ]] && printf '%s\n' stopped ;;
    *) return 1 ;;
  esac
}

main() {
  [[ $# == 1 && "$EUID" == 0 ]] || return 1
  case "$1" in
    status) status ;;
    start)
      systemctl start "$worker_unit"
      [[ "$(status)" == running ]] || return 1
      printf '%s\n' running
      ;;
    stop)
      systemctl stop "$worker_unit"
      [[ "$(status)" == stopped ]] || return 1
      # systemd 在整个 cgroup 退出后才完成 stop；应用 FFmpeg 与 Worker 同属此组。
      local group
      group="$(systemctl show "$worker_unit" --property=ControlGroup --value)"
      if [[ -n "$group" && -r "/sys/fs/cgroup${group}/cgroup.procs" ]]; then
        local remaining
        remaining="$(<"/sys/fs/cgroup${group}/cgroup.procs")"
        [[ -z "$remaining" ]] || return 1
      fi
      printf '%s\n' stopped
      ;;
    *) return 1 ;;
  esac
}

if [[ "${BASH_SOURCE[0]:-$0}" == "$0" ]]; then
  main "$@"
fi
