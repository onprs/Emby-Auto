# Direct Linux Deployment

**English** | [简体中文](direct-deployment.md)

This guide covers Emby Auto on your own Linux device. The installer installs the application, FFmpeg, and ffprobe, and enables the application services at boot. Install and configure PostgreSQL, qBittorrent, and Emby Server separately. The application serves both the web interface and API; a reverse proxy is optional.

## Automatic installation

### Requirements

- Debian 12/13 or Ubuntu 22.04/24.04/26.04, 64-bit `amd64` or `arm64`, with systemd running.
- Root or sudo access, Bash and curl installed, and access to GitHub and the system package repositories.
- A reachable PostgreSQL database and a dedicated application account. PostgreSQL 17 is the project's database validation baseline.
- qBittorrent Web API, Emby Server, and a TMDb API Read Access Token.

Run:

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s --
```

As root, replace `sudo bash` with `bash`. Docker, Git, Go, and Node.js are not required. The installer preserves existing databases, reverse proxies, and their configuration.

For an existing media library, use its owner account:

```bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s -- --user media-account
```

By default, the installer creates a non-root `emby-auto` account and download, work, staging, and media directories under `/srv/emby-auto`. An existing account must have access to the selected directories. Existing media ownership and permissions are preserved.

### Open the setup wizard

Open `http://127.0.0.1:18080` on the device. For a remote device, first create an SSH tunnel from your computer:

```bash
ssh -L 18080:127.0.0.1:18080 user@device-address
```

Open the same address and enter:

1. Your PostgreSQL host, port, database, account, password, and SSL mode.
2. An administrator account.
3. qBittorrent, Emby, and TMDb settings.
4. Media directories and FFmpeg tool paths.
5. A transcode profile.

The application creates its tables in the selected database. The service account must be able to reach the database and access the media directories. Worker waits until setup is complete before processing jobs.

### Installer options

| Option | Purpose |
| --- | --- |
| `--version vVERSION` | Install a specific Release; defaults to the latest stable Release |
| `--user ACCOUNT` | Use an existing media owner; defaults to creating `emby-auto` |
| `--port PORT` | Shared loopback web and API port; defaults to `18080` |
| `--archive FILE`, `--checksums FILE` | Verify and use a downloaded archive and `SHA256SUMS` |
| `--no-start` | Prepare application files and services without changing their running state |
| `--finalize` | Verify prepared services and complete installation registration without restarting services or rewriting configuration |

For a port conflict, use a free port such as `--port 19080`, and adjust the browser address and SSH tunnel accordingly. Repeating the installer preserves the installed version, setup state, and runtime configuration. Follow the upgrade procedure to change versions.

## Directories and services

| Content | Location or endpoint |
| --- | --- |
| Current release | `/opt/emby-auto/current` |
| Release directories | `/opt/emby-auto/releases/` |
| Runtime environment | `/etc/emby-auto/runtime.env` |
| Installer record | `/etc/emby-auto/installer.env` |
| Setup configuration | `/var/lib/emby-auto/bootstrap.json` and its `.complete` marker |
| Web and API | `127.0.0.1:18080` by default |
| Application services | `emby-auto-api.service`, `emby-auto-worker.service` |
| Background control | `emby-auto-systemd-control.service`, `/etc/emby-auto/host-control.env` |

API and Worker run as separate processes. Automatic installation configures background control for Dashboard pause and resume. Pausing stops Worker and marks active application downloads for restoration; resuming restores those downloads before starting Worker. You manage PostgreSQL, the reverse proxy, qBittorrent, and Emby separately.

## Manual deployment

### 1. Prepare dependencies and the release

Prepare a PostgreSQL database, a dedicated account, FFmpeg, and ffprobe. The service units require systemd. Download and verify the release for your architecture:

```bash
release_tag="<release-tag>"
target_arch="amd64"
archive="emby-auto-direct-${target_arch}-linux-${target_arch}.tar.gz"
curl -fLO "https://github.com/onprs/Emby-Auto/releases/download/${release_tag}/${archive}"
curl -fLO "https://github.com/onprs/Emby-Auto/releases/download/${release_tag}/SHA256SUMS"
awk -v name="${archive}" '$2 == name' SHA256SUMS | sha256sum --check --strict -
tar -xzf "${archive}"
cd "emby-auto-direct-${target_arch}"
sha256sum -c SHA256SUMS
```

Use `target_arch="arm64"` for an arm64 device. Confirm each command succeeds before continuing; download the release again if verification fails.

### 2. Prepare the database and media account

The database administrator must create a dedicated database and account with permission to create tables and extensions. The application uses `pgcrypto`; an administrator should enable it in advance on restricted managed databases. Use loopback for a local database, or a trusted network and an appropriate TLS mode for a remote database.

Run the application as the media owner. These examples use `emby`; replace the account and group when appropriate and query the actual UID:

```bash
id -u emby
id -g emby
```

The service UID must match the owner of imported media. It needs access to download, work, staging, and library directories. qBittorrent and the application must use the same absolute download path. Keep the existing permission policy for established libraries. API and Worker use the same non-root account with a nonzero UID and GID so they can share setup configuration with mode `0600`.

For a new deployment whose media directories do not exist yet, create them with the following example. Preserve permissions on existing directories.

```bash
sudo install -d -o emby -g emby -m 0770 \
  /srv/emby-auto/downloads \
  /srv/emby-auto/work \
  /srv/emby-auto/staging \
  /srv/emby-auto/media/anime \
  /srv/emby-auto/media/movies
```

For hardware transcoding, add the service account to the device-required `render`, `video`, or other groups, then restart services to apply group membership.

### 3. Install the release and runtime configuration

From the verified release directory:

```bash
release_source="$PWD"
release_id="$(date -u +%Y%m%d%H%M%S)"
sudo install -d -o root -g root -m 0755 "/opt/emby-auto/releases/${release_id}"
sudo cp -a "${release_source}/." "/opt/emby-auto/releases/${release_id}/"
sudo chown -R root:root "/opt/emby-auto/releases/${release_id}"
sudo ln -sfn "releases/${release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
sudo install -d -o root -g root -m 0755 /etc/emby-auto
sudo install -o root -g emby -m 0640 \
  /opt/emby-auto/current/config/runtime.env.example /etc/emby-auto/runtime.env
sudoedit /etc/emby-auto/runtime.env
```

Set `EMBY_MEDIA_OWNER_UID` to the service account's actual non-root UID. The default configuration is:

```dotenv
API_ADDRESS=127.0.0.1:18080
WEB_ROOT=/opt/emby-auto/current/web
BOOTSTRAP_CONFIG_PATH=/var/lib/emby-auto/bootstrap.json
SESSION_COOKIE_SECURE=false
```

Leave the database connection and encryption key unset so the setup wizard can configure them. `WEB_ROOT` selects the directory served by the application. If you host the web files separately, leave it unset and forward `/api/` to the application yourself.

### 4. Configure application services

Bind background control to the application UID and GID, then install all three units:

```bash
sudo install -o root -g root -m 0600 \
  /opt/emby-auto/current/config/host-control.env.example /etc/emby-auto/host-control.env
printf 'EMBY_AUTO_SERVICE_UID=%s\nEMBY_AUTO_SERVICE_GID=%s\n' "$(id -u emby)" "$(id -g emby)" | \
  sudo tee /etc/emby-auto/host-control.env >/dev/null
for service in api worker systemd-control; do
  sudo install -o root -g root -m 0644 \
    "/opt/emby-auto/current/config/emby-auto-${service}.service" \
    "/etc/systemd/system/emby-auto-${service}.service"
done
```

The control service runs as root and exposes fixed Worker operations and host status queries. Release directories and their parents must be root owned and must not be writable by the group or other users.

The units default to `emby:emby`. For another account, create persistent drop-ins for both services and change the runtime environment file's group to the selected account's group:

```bash
sudo systemctl edit emby-auto-api.service
sudo systemctl edit emby-auto-worker.service
```

Also set `/etc/emby-auto/host-control.env` to the selected account’s actual UID and GID. Use the following in both application drop-ins:

```ini
[Service]
User=<media-user>
Group=<media-group>
```

After configuring the account, load and start the services:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-systemd-control.service emby-auto-api.service emby-auto-worker.service
```

These startup commands are for new deployments. During migration, keep Worker stopped and preserve setup state using the procedure below. For a new deployment, open the setup wizard locally or through an SSH tunnel.

## HTTPS and reverse proxies

Local access and SSH tunnels can use the application directly. For LAN or internet access, choose Caddy, Nginx, Traefik, or another reverse proxy and configure your domain and TLS certificate. The upstream is `http://127.0.0.1:18080`; adjust it for a custom port.

The proxy must:

- Forward the web interface and `/api/` to the same application endpoint, preserving the original host and protocol.
- Support long-lived SSE at `/api/v1/events` without response buffering.
- Forward `Range` and `If-Range` for media previews and stream the response.

The release includes `config/nginx.conf` as an optional loopback proxy example on `127.0.0.1:18079`. Adapt it to your own Nginx site and certificate configuration, or use your selected proxy's configuration.

Once HTTPS works, set `SESSION_COOKIE_SECURE=true` in `/etc/emby-auto/runtime.env` and restart API:

```bash
sudo systemctl restart emby-auto-api.service
```

Keep the application bound to loopback and expose the reverse proxy. Confirm that session cookies include `HttpOnly`, `SameSite=Strict`, and `Secure`.

## Checks and backups

```bash
systemctl status emby-auto-api emby-auto-worker emby-auto-systemd-control --no-pager
journalctl -u emby-auto-api -u emby-auto-worker -u emby-auto-systemd-control -f
curl --fail http://127.0.0.1:18080/api/v1/setup/status
curl --fail http://127.0.0.1:18080/api/v1/health/ready
```

API readiness should pass after setup. The service account can run `emby-auto-worker --check` with its runtime environment to validate Worker configuration and database connectivity. Use this Bash helper to load the environment for maintenance commands:

```bash
emby_auto_maintenance() {
  sudo bash -c 'set -a; source /etc/emby-auto/runtime.env; set +a; command_name=$1; shift; exec "/opt/emby-auto/current/bin/$command_name" "$@"' -- "$@"
}
emby_auto_maintenance emby-auto-background-runtime --state status
emby_auto_maintenance emby-auto-release-preflight --require-idle
```

`verified=true` confirms Worker and download state. `transfers.active` counts active torrents; preflight `downloads.active` counts business records, which may remain after pausing. Status output includes counts, download digests, and progress.

Back up PostgreSQL consistently, `/etc/emby-auto`, `/var/lib/emby-auto`, and media and work directories according to your storage policy. Preserve both `bootstrap.json` and `bootstrap.json.complete`. The encryption key is required to read stored service credentials. Encrypt backups and restrict access.

## Upgrades and existing deployments

Back up the database and application configuration, verify the new archive, and install it in a new release directory. Pause background work with the maintenance helper above:

```bash
emby_auto_maintenance emby-auto-background-runtime --state stopped
emby_auto_maintenance emby-auto-release-preflight --require-idle
```

Confirm `state=stopped`, `verified=true`, and `transfers.active=0`. Check preflight for running operations, long tasks, and stale operations. For interrupted attempts, keep Worker stopped, wait at least five minutes, then preview recovery candidates:

```bash
emby_auto_maintenance emby-auto-recover-stale-operations --worker-stopped --stale-after 5m
```

Review the candidates, repeat the same command with `--apply`, and check runtime state again. Once the gates pass, switch releases:

```bash
(
  set -e
  new_release_id="<installed-release-id>"
  sudo systemctl stop emby-auto-api.service emby-auto-systemd-control.service
  sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
  sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
  for service in api worker systemd-control; do
    sudo install -o root -g root -m 0644 \
      "/opt/emby-auto/current/config/emby-auto-${service}.service" \
      "/etc/systemd/system/emby-auto-${service}.service"
  done
  sudo systemctl daemon-reload
  sudo systemctl start emby-auto-systemd-control.service emby-auto-api.service
  curl --fail http://127.0.0.1:18080/api/v1/health/ready
  emby_auto_maintenance emby-auto-migration-check
  emby_auto_maintenance emby-auto-background-runtime --state running
)
```

Preserve the environment, setup files, control-service identity binding, and application account drop-ins. Adjust checks for a custom port. Confirm `verified=true` and `transfers.runtimeMarks=0` after resuming, and verify existing jobs. If recovery fails or its result is uncertain, inspect state read-only, then perform the formal `stopped` transition and verify the gates. Keep Worker stopped after a failed database migration; binary rollback depends on migration compatibility.

The installer rejects incompatible installation records. Follow manual deployment steps for older deployments, preserve the database, environment and setup files, and configure background control. PostgreSQL and Nginx created by older installers remain user managed.

### Migrating from Docker

1. Stop the old Worker through the existing deployment controls, confirm transcoding has exited, and pause application torrents while retaining restoration markers. Keep old services stopped so only one Worker handles jobs.
2. Save a consistent database backup, setup files and completion marker, environment, and old deployment definitions. Preserve the original encryption key, administrator, and absolute media paths. Check that the release contains the migrations required by the current database.
3. Prepare native files with `--no-start` and keep the new Worker stopped. Run this example from a directory containing the installer, matching architecture archive, and checksums. It uses this guide's `emby` account; add `--port` if needed:

```bash
sudo bash install.sh --no-start --user emby \
  --archive ./emby-auto-direct-amd64-linux-amd64.tar.gz \
  --checksums ./SHA256SUMS
```

4. Copy both original setup files to the native state directory, owned by the service account with mode `0600`. Configure a host-reachable database connection, check external service addresses and descendant directory permissions, and keep configuration changes under a single writer during migration.
5. Start control and API, then validate the database, web interface, and HTTPS. Check migration compatibility before applying any required database migration. Start the new Worker through formal background recovery, verify downloads, subtitles, transcoding, import, and cleanup, and retain old resources for rollback.
6. Once services pass checks, run `sudo bash install.sh --finalize`. It verifies release files and services without restarting services or rewriting configuration.

Two migration tools preview changes by default. `emby-auto-migrate-runtime-addresses` replaces only the selected host in qBittorrent and Emby URLs with a loopback IP. `emby-auto-migrate-staging-ownership` changes only root-owned directories within database-backed staging task trees, retaining group, mode, and file content. Review the preview before supplying a private backup path and `--apply`:

```bash
sudo install -d -o root -g root -m 0700 /var/backups/emby-auto-migration
emby_auto_maintenance emby-auto-migrate-runtime-addresses --container-host-address 127.0.0.1
emby_auto_maintenance emby-auto-migrate-staging-ownership --uid "$(id -u emby)" --container-host-address 127.0.0.1
```

The address tool matches `host.docker.internal` by default; use `--from-host` for other container hosts and check Agent and proxy settings separately. `--container-host-address` temporarily resolves that qBittorrent host only for the maintenance command; the database connection must already be host reachable. Supply `--backup` when applying, and constrain preview counts with `--expected-changes` or `--expected-tasks`. For rollback, preview `--restore` with the saved snapshot before adding `--apply`. Backup directories must be owned by the command user with mode `0700`; snapshots use mode `0600`. These commands require a stopped Worker, paused application torrents, and verifiable download references.
