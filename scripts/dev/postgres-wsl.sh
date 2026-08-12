#!/usr/bin/env bash
set -euo pipefail

pg_major=17
pg_bin="/usr/lib/postgresql/$pg_major/bin"
state_root="${XDG_DATA_HOME:-$HOME/.local/share}/emby-auto/postgres-$pg_major"
data_dir="$state_root/data"
socket_dir="$state_root/socket"
log_file="$state_root/postgres.log"
port="${POSTGRES_PORT:-15432}"
db_name="${POSTGRES_DB:-emby_auto}"
db_user="${POSTGRES_USER:-emby_auto}"
db_password="${POSTGRES_PASSWORD:-emby_auto}"
action=${1:-status}

validate_identifier() {
  case "$1" in
    ''|*[!a-zA-Z0-9_]*)
      echo "Invalid PostgreSQL identifier: $1" >&2
      exit 2
      ;;
  esac
}

install_postgres() {
  if [ -x "$pg_bin/postgres" ]; then
    return
  fi

  sudo apt-get update
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl postgresql-common
  sudo install -d /usr/share/postgresql-common/pgdg
  sudo curl -fsSL -o /usr/share/postgresql-common/pgdg/apt.postgresql.org.asc \
    https://www.postgresql.org/media/keys/ACCC4CF8.asc
  echo "deb [signed-by=/usr/share/postgresql-common/pgdg/apt.postgresql.org.asc] https://apt.postgresql.org/pub/repos/apt jammy-pgdg main" \
    | sudo tee /etc/apt/sources.list.d/pgdg.list >/dev/null
  sudo sed -i 's/^#\?create_main_cluster.*/create_main_cluster = false/' /etc/postgresql-common/createcluster.conf
  sudo apt-get update
  sudo DEBIAN_FRONTEND=noninteractive apt-get install -y "postgresql-$pg_major" "postgresql-client-$pg_major"
}

initialize_database() {
  if [ -f "$data_dir/PG_VERSION" ]; then
    return
  fi

  mkdir -p "$state_root" "$socket_dir"
  chmod 700 "$state_root" "$socket_dir"
  password_file="$state_root/.init-password"
  umask 077
  printf '%s\n' "$db_password" > "$password_file"
  "$pg_bin/initdb" \
    --auth-host=scram-sha-256 \
    --auth-local=trust \
    --encoding=UTF8 \
    --locale=C.UTF-8 \
    --pwfile="$password_file" \
    --username="$db_user" \
    -D "$data_dir"
  rm -f "$password_file"
}

start_postgres() {
  install_postgres
  initialize_database
  mkdir -p "$socket_dir"

  if ! "$pg_bin/pg_ctl" -D "$data_dir" status >/dev/null 2>&1; then
    "$pg_bin/pg_ctl" -D "$data_dir" -l "$log_file" \
      -o "-h 127.0.0.1 -p $port -k $socket_dir" -w start
  fi

  if ! "$pg_bin/psql" -h "$socket_dir" -p "$port" -U "$db_user" -d postgres \
    -tAc "SELECT 1 FROM pg_database WHERE datname = '$db_name'" | grep -q 1; then
    "$pg_bin/createdb" -h "$socket_dir" -p "$port" -U "$db_user" "$db_name"
  fi

  PGPASSWORD="$db_password" "$pg_bin/psql" -h 127.0.0.1 -p "$port" -U "$db_user" -d "$db_name" -tAc 'SELECT 1' >/dev/null
  echo "PostgreSQL is ready at postgres://$db_user@127.0.0.1:$port/$db_name"
}

validate_identifier "$db_name"
validate_identifier "$db_user"

case "$action" in
  start)
    start_postgres
    ;;
  stop)
    if [ -x "$pg_bin/pg_ctl" ] && [ -f "$data_dir/PG_VERSION" ]; then
      "$pg_bin/pg_ctl" -D "$data_dir" -m fast -w stop
    else
      echo "PostgreSQL is not initialized."
    fi
    ;;
  status)
    if [ -x "$pg_bin/pg_ctl" ] && [ -f "$data_dir/PG_VERSION" ]; then
      "$pg_bin/pg_ctl" -D "$data_dir" status
    else
      echo "PostgreSQL is not initialized."
      exit 1
    fi
    ;;
  shell)
    start_postgres
    exec "$pg_bin/psql" -h "$socket_dir" -p "$port" -U "$db_user" -d "$db_name"
    ;;
  *)
    echo "usage: postgres-wsl.sh {start|stop|status|shell}" >&2
    exit 2
    ;;
esac
