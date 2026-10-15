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

API and Worker run as separate processes. You manage the lifecycle of PostgreSQL, the reverse proxy, qBittorrent, and Emby. Use `systemctl` to manage native application services. Dashboard Worker controls that depend on a host controller require a separate host integration.

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

```bash
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service /etc/systemd/system/emby-auto-worker.service
```

The units default to `emby:emby`. For another account, create persistent drop-ins for both services and change the runtime environment file's group to the selected account's group:

```bash
sudo systemctl edit emby-auto-api.service
sudo systemctl edit emby-auto-worker.service
```

Use the following in both drop-ins:

```ini
[Service]
User=<media-user>
Group=<media-group>
```

After configuring the account, load and start the services:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-api.service emby-auto-worker.service
```

Open the setup wizard locally or through an SSH tunnel.

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
systemctl status emby-auto-api emby-auto-worker --no-pager
journalctl -u emby-auto-api -u emby-auto-worker -f
curl --fail http://127.0.0.1:18080/api/v1/setup/status
curl --fail http://127.0.0.1:18080/api/v1/health/ready
```

API readiness should pass after setup. The service account can run `emby-auto-worker --check` with its runtime environment to validate Worker configuration and database connectivity.

Back up PostgreSQL consistently, `/etc/emby-auto`, `/var/lib/emby-auto`, and media and work directories according to your storage policy. Preserve both `bootstrap.json` and `bootstrap.json.complete`. The encryption key is required to read stored service credentials. Encrypt backups and restrict access.

## Upgrades and existing deployments

Back up the database and application configuration. Pause background work and finish or pause downloads and transcoding through the existing deployment's controls. Download and verify the new release and install it in a new release directory.

```bash
new_release_id="<installed-release-id>"
sudo systemctl stop emby-auto-worker.service emby-auto-api.service
sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service /etc/systemd/system/emby-auto-worker.service
sudo systemctl daemon-reload
sudo systemctl start emby-auto-api.service
curl --fail http://127.0.0.1:18080/api/v1/health/ready
sudo systemctl start emby-auto-worker.service
```

Preserve the runtime environment, setup files, and account drop-ins. Adjust checks for a custom port. If database migration fails, keep Worker stopped and inspect API logs. Rolling back application binaries depends on database migration compatibility.

For deployments created by the older installer that provisioned PostgreSQL and Nginx, retain and manage those services and their data. The new installer rejects the old installer record. Follow manual deployment steps and preserve the existing configuration. Set `WEB_ROOT` in the API runtime environment to serve the web interface directly; update the proxy upstream if you change the application port.

For Docker migrations, preserve the application database, original encryption key, and setup files. Grant the service account access and replace container-only addresses with host-reachable addresses. Keep a single Worker during migration and validate the database, web interface, and media workflow before removing old resources.
