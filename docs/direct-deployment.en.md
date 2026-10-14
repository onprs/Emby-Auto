# Direct Linux Deployment

[简体中文](direct-deployment.md) | **English**

Install on your own Linux device using the automatic installer or a prebuilt release. Prepare qBittorrent and Emby Server separately.

## Automatic Installation

Supported systems: Debian 12/13 and Ubuntu 22.04/24.04/26.04, 64-bit `amd64` or `arm64`, running systemd. You need root or sudo access, Bash, curl, and connectivity to GitHub, distribution repositories, and the official PostgreSQL repository when required.

~~~bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s --
~~~

When running as root, replace `sudo bash` with `bash`. The installer downloads and verifies a prebuilt release, installs PostgreSQL 17, Nginx, FFmpeg and ffprobe, creates a non-root service account and storage directories, and enables services at boot. Git, Go, and Node.js are not required on the target device.

| Option | Default | Purpose |
| --- | --- | --- |
| `--version` | Latest stable Release | Select a release tag starting with `v` |
| `--user` | Create `emby-auto` | Use the existing media owner account |
| `--port` | `18080` | Web loopback port |
| `--api-port` | `18081` | API loopback port |
| `--database-port` | `18082` | Dedicated PostgreSQL loopback port |
| `--help` | — | Show help |

Append options to the command, for example:

~~~bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s -- --user media-owner
~~~

Ports must be distinct integers between `1024` and `65535`. Existing media permissions are preserved; the selected account needs read/write access. Hardware encoding may require membership in `render`, `video`, or device-specific groups.

The installer creates a dedicated PostgreSQL cluster named `17/emby_auto`, listening on `127.0.0.1:18082` by default. Database credentials are saved in restricted configuration files and omitted from installation logs. The web wizard skips database configuration.

### Open the Web Interface

Open `http://127.0.0.1:18080` on the device. For a remote device, establish an SSH tunnel from your computer:

~~~bash
ssh -L 18080:127.0.0.1:18080 user@device-address
~~~

Then open the same URL. Adjust the tunnel and browser URL if you changed the web port.

Create an administrator and enter qBittorrent Web UI credentials, an Emby URL and API key, and a TMDb API Read Access Token. Service addresses must be reachable from the deployment device. qBittorrent and the application must use identical absolute download paths. Mount remote download and library directories on the device before setup.

Default directories are `/srv/emby-auto/downloads`, `/srv/emby-auto/work`, `/srv/emby-auto/staging`, `/srv/emby-auto/media/anime`, and `/srv/emby-auto/media/movies`. For an existing library, enter its actual paths and use an account with the media owner's UID.

Configure HTTPS for network access, set `SESSION_COOKIE_SECURE=true` in `/etc/emby-auto/runtime.env`, and restart the API. The default loopback endpoint supports local access and SSH tunnels.

### Retry, Logs, and Backups

Repeat the same command and options after an interrupted installation. Generated credentials, configuration, and media directories are preserved. A completed installation checks its existing services and displays its address while retaining the installed version. The installer does not take over manual deployments. Follow the upgrade instructions below to update a version.

~~~bash
systemctl status emby-auto-api emby-auto-worker postgresql@17-emby_auto --no-pager
journalctl -u emby-auto-api -u emby-auto-worker --since today
~~~

Back up the application database, `/etc/emby-auto`, `/var/lib/emby-auto`, and media directories. For the automatically installed database:

~~~bash
sudo -u postgres pg_dump -p 18082 -Fc emby_auto > emby-auto.dump
~~~

Adjust the database port when customized. Encrypt backups and restrict access because they contain sensitive information.

## Manual Installation

The following steps are for deployments that manage dependencies and service accounts independently. Automatic installation already completes these steps; continue with HTTPS, backups, or upgrades as needed.

### Deployment Topology

| Component | Default location or port | Management |
| --- | --- | --- |
| API | `127.0.0.1:18081` | `emby-auto-api.service` |
| Worker | No listening port | `emby-auto-worker.service` |
| Web / Nginx | `127.0.0.1:18080` | Host Nginx |
| Release directories | `/opt/emby-auto/releases/<release-id>` | Read-only, root-managed |
| Active release | `/opt/emby-auto/current` | Atomic symbolic link |
| Runtime state | `/var/lib/emby-auto` | systemd `StateDirectory` |
| Runtime environment | `/etc/emby-auto/runtime.env` | Root-managed |

The standard direct deployment does not install a host controller. Dashboard controls that depend on host-level Worker management are unavailable; use `systemctl` to manage the service lifecycle.

### Requirements

The target host requires:

- 64-bit Linux (`amd64` or `arm64`) with systemd.
- PostgreSQL 17 and Nginx supplied by the distribution.
- FFmpeg and ffprobe with the encoders required by the selected transcode configuration.
- Reachable qBittorrent Web API and Emby Server instances.
- A TMDb API Read Access Token.
- A non-root service account matching the media owner; the manual steps use `emby`.
- Bash, curl, GNU coreutils, tar, and gzip.

### 1. Download and Verify a Release

Download the prebuilt release for the target architecture. These commands use `amd64`; set `target_arch=arm64` on an `arm64` device:

~~~bash
target_arch=amd64
archive="emby-auto-direct-${target_arch}-linux-${target_arch}.tar.gz"
release_url=https://github.com/onprs/Emby-Auto/releases/latest/download
curl -fL "${release_url}/${archive}" -o "${archive}"
curl -fL "${release_url}/SHA256SUMS" -o SHA256SUMS
awk -v name="${archive}" '$2 == name' SHA256SUMS | sha256sum --check --strict -
tar -xzf "${archive}"
cd "emby-auto-direct-${target_arch}"
sha256sum -c SHA256SUMS
~~~

Confirm each command succeeds before proceeding. Do not install a release that fails verification. Bundles include the application, web assets, service templates, installer, and deployment guides. To select a version, replace `latest/download` with `download/vVERSION` in the URL.

See the [development and release guide](https://github.com/onprs/Emby-Auto/blob/main/docs/development.md) for source builds.

### 2. Prepare the Service Account and Storage

Confirm that the service account has a nonzero UID and GID:

```bash
id emby
id -u emby
id -g emby
```

When another account owns the media files, update all of the following consistently:

- `User` and `Group` for both systemd services.
- `EMBY_MEDIA_OWNER_UID` in `/etc/emby-auto/runtime.env`.
- Ownership of the state, download, work, staging, and media directories.

The API and Worker use the same service account so they can read one installation file with mode `0600`; the Worker also creates and repairs imported files as the media owner. Neither service requires root or `CAP_CHOWN`.

Create the example directories with:

```bash
sudo install -d -o emby -g emby -m 0770 \
  /srv/emby-auto/downloads \
  /srv/emby-auto/work \
  /srv/emby-auto/staging \
  /srv/emby-auto/media/anime \
  /srv/emby-auto/media/movies
```

Preserve the established ownership policy of existing media libraries and avoid indiscriminate recursive `chown` or `chmod` operations. The qBittorrent save path must exactly match the absolute path configured for the Worker. When qBittorrent runs in a container, use a same-path mount.

For VAAPI, NVENC, or another hardware encoder, add the service account to the required `render`, `video`, or device-specific group and restart the services to apply group membership.

### 3. Configure PostgreSQL

Create a dedicated role and database as a PostgreSQL administrator. Enter the password interactively:

```bash
sudo -u postgres createuser --pwprompt emby_auto
sudo -u postgres createdb --owner=emby_auto emby_auto
```

PostgreSQL should not be exposed to the internet. A database on the same host can use `127.0.0.1:5432`; a remote database should use a trusted network and an appropriate TLS mode.

The application applies application migrations and River migrations in sequence during first-run setup and subsequent startup. Migration tables are application-managed and must not be modified manually.

### 4. Install a Release Directory

From the verified release directory, run:

```bash
release_source="$PWD"
release_id="$(date -u +%Y%m%d%H%M%S)"

sudo install -d -o root -g root -m 0755 /opt/emby-auto/releases
sudo install -d -o root -g root -m 0755 "/opt/emby-auto/releases/${release_id}"
sudo cp -a "${release_source}/." "/opt/emby-auto/releases/${release_id}/"
sudo chown -R root:root "/opt/emby-auto/releases/${release_id}"

sudo ln -sfn "releases/${release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current
```

Root manages immutable release directories. The service account only needs to read binaries, templates, and Web files. systemd stores runtime state in `/var/lib/emby-auto`, outside the release directory.

### 5. Install the Runtime Environment and systemd Services

Create the runtime environment file:

```bash
sudo install -d -o root -g root -m 0755 /etc/emby-auto
sudo install -o root -g emby -m 0640 \
  /opt/emby-auto/current/config/runtime.env.example \
  /etc/emby-auto/runtime.env
sudoedit /etc/emby-auto/runtime.env
```

Set at least `EMBY_MEDIA_OWNER_UID`. A first-run setup performed over loopback or an SSH tunnel may temporarily keep `SESSION_COOKIE_SECURE=false`; set it to `true` after HTTPS becomes available.

Install the systemd units:

```bash
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service \
  /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service \
  /etc/systemd/system/emby-auto-worker.service
sudo systemctl daemon-reload
```

The default units use `emby:emby`. For another service account, create persistent drop-ins for both services instead of editing units in the release:

```bash
sudo systemctl edit emby-auto-api.service
sudo systemctl edit emby-auto-worker.service
```

Place the following content in both drop-ins:

```ini
[Service]
User=<media-user>
Group=<media-group>
```

Also update the group of `/etc/emby-auto/runtime.env` and set `EMBY_MEDIA_OWNER_UID` for the same account.

Load the final configuration and start both services:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now emby-auto-api.service
sudo systemctl enable --now emby-auto-worker.service
```

The Worker waits for installation configuration and does not claim background jobs before first-run setup completes.

### 6. Install Nginx

Install the shared location configuration and direct-deployment site:

```bash
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-locations.conf \
  /etc/nginx/emby-auto-locations.conf
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/nginx.conf \
  /etc/nginx/conf.d/emby-auto.conf
sudo nginx -t
sudo systemctl reload nginx
```

If the distribution does not load `/etc/nginx/conf.d/*.conf`, install the site in its enabled Nginx server directory instead. Preserve the include path for `/etc/nginx/emby-auto-locations.conf`.

The default site listens on `127.0.0.1:18080`, and the API listens on `127.0.0.1:18081`. Verify both entry points:

```bash
curl --fail http://127.0.0.1:18081/api/v1/health/live
curl --fail http://127.0.0.1:18080/api/v1/health/live
```

### 7. Complete First-Run Setup

For remote administration, forward the loopback Web endpoint over SSH:

```bash
ssh -L 8080:127.0.0.1:18080 user@example-host
```

Then open `http://127.0.0.1:8080` on the administration host. Typical same-host setup values are:

```text
PostgreSQL host   127.0.0.1
PostgreSQL port   5432
PostgreSQL db     emby_auto
PostgreSQL user   emby_auto
PostgreSQL SSL    disable (same-host loopback only)
qBittorrent URL   http://127.0.0.1:<qbittorrent-port>
Emby URL          http://127.0.0.1:<emby-port>/emby
ffmpegPath        /usr/bin/ffmpeg
ffprobePath       /usr/bin/ffprobe
downloadRoot      /srv/emby-auto/downloads
workRoot          /srv/emby-auto/work
stagingRoot       /srv/emby-auto/staging
animeLibraryRoot  /srv/emby-auto/media/anime
movieLibraryRoot  /srv/emby-auto/media/movies
```

The wizard writes the database connection and generated configuration-encryption key to `/var/lib/emby-auto/bootstrap.json` with mode `0600`. This file contains sensitive configuration and must not be copied to an untrusted location, written to logs, or committed to version control.

After setup, run the readiness checks:

```bash
curl --fail http://127.0.0.1:18081/api/v1/health/ready
sudo -u emby bash -c '
  set -a
  source /etc/emby-auto/runtime.env
  set +a
  exec /opt/emby-auto/current/bin/emby-auto-worker --check
'
```

Replace `emby` when using another service account. Confirm that both services remain active without recurring startup or connection errors:

```bash
systemctl status emby-auto-api emby-auto-worker --no-pager
journalctl -u emby-auto-api -u emby-auto-worker --since today
```

### 8. Configure HTTPS

LAN or internet access requires Caddy, Nginx, Traefik, or another TLS entry point in front of `127.0.0.1:18080`. The outer proxy must preserve:

- Long-lived, unbuffered SSE connections at `/api/v1/events`.
- `Range`, `If-Range`, and streaming behavior for artifact requests.
- `/api/` proxying, static asset caching, and the React SPA fallback.

After validating TLS, run:

```bash
sudoedit /etc/emby-auto/runtime.env
sudo systemctl restart emby-auto-api.service
```

Set `SESSION_COOKIE_SECURE=true` and verify that the session cookie contains `HttpOnly`, `SameSite=Strict`, and `Secure`. Do not expose API port `18081` through the firewall. Keep port `18080` bound to loopback and expose only the outer TLS entry point.

### 9. Backups and Monitoring

At minimum, back up:

- A consistent PostgreSQL archive.
- `/etc/emby-auto/runtime.env`.
- `/var/lib/emby-auto`, which contains installation configuration.
- Media and application work directories according to the storage policy.

Inspect runtime status with:

```bash
systemctl status emby-auto-api emby-auto-worker --no-pager
journalctl -u emby-auto-api -u emby-auto-worker -f
curl --fail http://127.0.0.1:18081/api/v1/health/ready
```

Backups may contain database connections and external-service credentials. Encrypt them and restrict access.

### 10. Upgrade

Download and verify a new GitHub Release, then install it into a new `/opt/emby-auto/releases/<release-id>` directory. Complete backups and allow active download processing, transcoding, imports, and Agent operations to finish before upgrading.

The automatic installer stores account settings in `installer.conf` systemd drop-ins, which remain active after unit updates. If you customized web or API ports, restore their values after copying the Nginx template below and before validating it.

Switch the active release in this order:

```bash
new_release_id="<installed-release-id>"

sudo systemctl stop emby-auto-worker.service
sudo systemctl stop emby-auto-api.service

sudo ln -sfn "releases/${new_release_id}" /opt/emby-auto/current.next
sudo mv -Tf /opt/emby-auto/current.next /opt/emby-auto/current

sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-api.service \
  /etc/systemd/system/emby-auto-api.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-worker.service \
  /etc/systemd/system/emby-auto-worker.service
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/emby-auto-locations.conf \
  /etc/nginx/emby-auto-locations.conf
sudo install -o root -g root -m 0644 \
  /opt/emby-auto/current/config/nginx.conf \
  /etc/nginx/conf.d/emby-auto.conf

sudo systemctl daemon-reload
sudo nginx -t
sudo systemctl start emby-auto-api.service
curl --fail http://127.0.0.1:18081/api/v1/health/ready
sudo systemctl start emby-auto-worker.service
sudo systemctl reload nginx
```

Do not overwrite `/etc/emby-auto/runtime.env` with a new template. systemd drop-ins remain effective after unit updates. If API readiness fails, leave the Worker stopped and inspect migration logs.

### 11. Rollback and Uninstall

An atomic switch back to an earlier release is valid only when the old binaries are compatible with the current database schema. After an incompatible schema migration, stop both services and restore PostgreSQL and `/var/lib/emby-auto` backups from the same point in time before switching releases.

Remove the services while retaining data:

```bash
sudo systemctl disable --now emby-auto-worker.service emby-auto-api.service
sudo rm -f /etc/systemd/system/emby-auto-worker.service
sudo rm -f /etc/systemd/system/emby-auto-api.service
sudo rm -rf /etc/systemd/system/emby-auto-worker.service.d
sudo rm -rf /etc/systemd/system/emby-auto-api.service.d
sudo rm -f /etc/nginx/conf.d/emby-auto.conf
sudo rm -f /etc/nginx/emby-auto-locations.conf
sudo systemctl daemon-reload
sudo nginx -t && sudo systemctl reload nginx
```

Delete the PostgreSQL database, `/var/lib/emby-auto`, `/etc/emby-auto`, and release directories only when the installation data is no longer required. Media libraries are not part of application uninstall.
