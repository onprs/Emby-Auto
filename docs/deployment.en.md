# Docker Compose Deployment

[简体中文](deployment.md) | **English**

This guide describes how to deploy Emby Auto with the provided Docker Compose configuration. Compose manages PostgreSQL, the API, the Worker, and the Web interface. qBittorrent and Emby Server remain external services.

## Deployment Topology

| Service | Network entry point | Persistent data |
| --- | --- | --- |
| `postgres` | Compose network only | `postgres-data` named volume |
| `api` | Compose network only | `app-data` and media directories |
| `worker` | No listening port | `app-data` and media directories |
| `web` | `127.0.0.1:8080` by default | None |

The API and Worker are separate processes that share PostgreSQL, installation configuration, and media directories. Compose does not publish the PostgreSQL or API ports and does not mount the Docker socket or host init system into the application.

## Requirements

- A 64-bit Linux host.
- Docker Engine 24 or later.
- Docker Compose v2.
- Reachable qBittorrent Web API and Emby Server instances.
- A TMDb API Read Access Token.
- Five absolute directories writable by the application.

## 1. Configure the Environment

Copy the environment template and restrict its permissions:

```bash
cp deploy/.env.example deploy/.env
chmod 600 deploy/.env
```

Review at least the following settings:

| Variable | Purpose |
| --- | --- |
| `POSTGRES_PASSWORD` | A dedicated, long random PostgreSQL password |
| `APP_UID` / `APP_GID` | The non-root UID/GID that owns Emby media files |
| `DOWNLOAD_ROOT` | Shared qBittorrent download and application read path |
| `WORK_ROOT` | Intermediate media-processing directory |
| `STAGING_ROOT` | Pre-import staging directory |
| `ANIME_LIBRARY_ROOT` | Emby anime library directory |
| `MOVIE_LIBRARY_ROOT` | Emby movie library directory |
| `APP_BIND_ADDRESS` / `APP_PORT` | Host address and port for the Web service |
| `SESSION_COOKIE_SECURE` | Set to `true` after HTTPS is enabled |

A common way to inspect the Emby service account is:

```bash
id -u emby
id -g emby
```

`APP_UID` and `APP_GID` must both be greater than 0. Create application-specific directories before startup and grant that UID/GID read and write access. Existing media libraries should retain their established ownership policy; avoid indiscriminate recursive `chown` or `chmod` operations.

Every media directory must use an absolute path. Compose preserves the same path on the host and inside the API and Worker containers. When qBittorrent runs in another container, mount its download directory at that same container path.

The Web service uses host port `8080` by default. Set `APP_PORT` to an available port in `deploy/.env` when qBittorrent or another service already uses it.

## 2. Start and Complete First-Run Setup

Build and start the services:

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

Inspect service status and logs:

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
docker compose --env-file deploy/.env -f deploy/compose.yaml logs -f api worker
```

Open `http://127.0.0.1:<APP_PORT>` and complete the first-run wizard. Use these PostgreSQL values:

```text
Host       postgres
Port       5432
Database   value of POSTGRES_DB
Username   value of POSTGRES_USER
Password   value of POSTGRES_PASSWORD
SSL        disable
```

Use these media-tool and directory values:

```text
ffmpegPath       /usr/bin/ffmpeg
ffprobePath      /usr/bin/ffprobe
downloadRoot     value of DOWNLOAD_ROOT
workRoot         value of WORK_ROOT
stagingRoot      value of STAGING_ROOT
animeLibraryRoot value of ANIME_LIBRARY_ROOT
movieLibraryRoot value of MOVIE_LIBRARY_ROOT
```

Containers can reach external services on the same host through `host.docker.internal`, for example:

```text
qBittorrent URL  http://host.docker.internal:<qbittorrent-port>
Emby URL         http://host.docker.internal:<emby-port>/emby
```

Use the actual ports and Emby base path configured by those services.

The wizard stores the database connection and generated configuration-encryption key in `bootstrap.json` on the `app-data` volume. The Worker waits for setup to complete before claiming background jobs.

## 3. Configure HTTPS

LAN or internet access requires a TLS reverse proxy in front of the Web service. Keep `APP_BIND_ADDRESS=127.0.0.1` and use `http://127.0.0.1:<APP_PORT>` as the upstream.

The reverse proxy must preserve the following behavior:

- Long-lived, unbuffered SSE connections at `/api/v1/events`.
- `Range` and `If-Range` headers for `/api/v1/tasks/{taskId}/artifacts/{video|subtitle}`.
- All other `/api/` requests and Web routes forwarded to the Web service.

After validating TLS, set the following value in `deploy/.env`:

```dotenv
SESSION_COOKIE_SECURE=true
```

Recreate the backend services to apply the setting:

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --force-recreate api worker
```

## 4. Operations

### Service Management

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
docker compose --env-file deploy/.env -f deploy/compose.yaml logs --tail=200 api worker
docker compose --env-file deploy/.env -f deploy/compose.yaml stop worker
docker compose --env-file deploy/.env -f deploy/compose.yaml start worker
```

The standard Compose deployment does not include a host controller. Dashboard controls that depend on host-level Worker management are therefore unavailable; use Compose to manage the Worker lifecycle.

### Backups

Back up the following data before upgrades or maintenance:

- PostgreSQL data from `postgres-data`, including business data and installation state; use a consistent `pg_dump` archive whenever possible.
- `bootstrap.json` from `app-data`.
- `deploy/.env`.
- Media directories according to the storage system's independent backup policy.

Encrypt backups and restrict access whenever they contain database connections or external-service credentials.

### Upgrade

After confirming that the application is idle and backups are complete, run:

```bash
git pull --ff-only
docker compose --env-file deploy/.env -f deploy/compose.yaml build
docker compose --env-file deploy/.env -f deploy/compose.yaml stop worker
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --wait postgres api web
docker compose --env-file deploy/.env -f deploy/compose.yaml exec -T api \
  curl --fail --silent http://127.0.0.1:8080/api/v1/health/ready
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d worker
docker compose --env-file deploy/.env -f deploy/compose.yaml ps
```

The API applies application migrations and River migrations in sequence during startup. If readiness fails, leave the Worker stopped and inspect the API logs. Never run an old Worker against a failed or incompatible database migration.

### Stop and Remove Containers

```bash
docker compose --env-file deploy/.env -f deploy/compose.yaml down
```

This command retains named volumes. Use `down --volumes` only when permanently deleting the PostgreSQL data and installation configuration is intentional.

## Troubleshooting

- `api` is `unhealthy`: inspect PostgreSQL health, API logs, and the volume containing `bootstrap.json`.
- The Worker cannot read a download: verify that qBittorrent and the Worker use exactly the same absolute path.
- Library import reports permission errors: verify `APP_UID`, `APP_GID`, and media-directory ownership.
- The session disappears after HTTPS login: confirm `SESSION_COOKIE_SECURE=true`, preserve the original protocol through the proxy, and access the site over HTTPS.
