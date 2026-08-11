# Emby Auto

[简体中文](README.md) | **English**

Emby Auto is a self-hosted media automation platform for Emby. It connects release discovery, RSS subscriptions, qBittorrent downloads, TMDb episode mapping, FFmpeg media processing, human review, and Emby library imports in one workflow, with a Web interface for task tracking and operational status.

## Features

- Discover anime series and movie releases and manage candidates through a unified task workflow.
- Poll RSS/Atom subscriptions continuously and filter entries by terms, episode coordinates, and current library occupancy.
- Manage qBittorrent enqueueing, file selection, progress synchronization, cancellation, retries, and cache cleanup.
- Map source episodes to regular TMDb episodes through a single-episode anchor.
- Produce one configured video file and one standalone ASS subtitle for every source video.
- Review video and subtitle artifacts before import, with checksums and atomic renames protecting destination files.
- Synchronize the Emby catalog and present tasks, background operations, failures, and live events in one interface.
- Use optional Agent capabilities only when deterministic rules cannot produce a unique result; every submission remains subject to backend validation.
- Configure the administrator, PostgreSQL, external services, and transcoding through a first-run wizard.

## Architecture

```text
Browser -> Web/Nginx -> API <-> PostgreSQL / River <-> Worker
                                                          |
                                                          +-> qBittorrent / TMDb / Emby / FFmpeg
```

| Component | Responsibility |
| --- | --- |
| Web / Nginx | Administration interface, static assets, API proxying, SSE, and media preview |
| API | Installation, authentication, queries, commands, and short transactions |
| Worker | Downloads, mapping, transcoding, subtitles, file organization, imports, and cleanup |
| PostgreSQL / River | Business state, events, configuration, and the durable job queue |

The API and Worker always run as separate processes. Only video transcoding is limited by dedicated concurrency slots; all other background work uses the general queue.

[`contracts/openapi.yaml`](contracts/openapi.yaml) is the single HTTP contract. Go server types and the TypeScript SDK are generated from it.

## Deployment

Emby Auto supports the following Linux deployment methods:

| Method | Intended environment | Managed components | Guide |
| --- | --- | --- | --- |
| Docker Compose | Application and PostgreSQL managed as containers | PostgreSQL, API, Worker, Web | [Docker Compose deployment](docs/deployment.en.md) |
| Direct installation | Host-managed systemd, Nginx, and PostgreSQL | API, Worker, static Web files | [Direct Linux deployment](docs/direct-deployment.en.md) |

qBittorrent and Emby Server remain external services in both deployment modes.

### Docker Compose Quick Start

```bash
cp deploy/.env.example deploy/.env
# Configure the database password, runtime UID/GID, and media directories.
docker compose --env-file deploy/.env -f deploy/compose.yaml up -d --build
```

The Web interface binds to `127.0.0.1:8080` by default. Set `APP_PORT` in `deploy/.env` when qBittorrent or another service already uses that port.

### Direct Linux Release

```bash
npm run deploy:direct:build
npm run deploy:direct:check-release
```

The build produces Linux binaries, static Web files, systemd and Nginx templates, deployment documentation, and SHA-256 checksums for `amd64` or `arm64`.

### Deployment Requirements

- qBittorrent, the Worker, and the host must use the same absolute download path.
- The Worker must run with the non-root UID that owns the Emby media files.
- The API and Worker share PostgreSQL and installation configuration while remaining separate processes.
- External access must use HTTPS with `SESSION_COOKIE_SECURE=true`.
- PostgreSQL, installation configuration, and media directories require independent backup policies.
- The application must not receive the Docker socket, host root, systemd bus, or unnecessary Linux capabilities.

## Local Development

### Requirements

- Go 1.26+
- Node.js 24+
- npm 11+
- PostgreSQL 17, or a local environment capable of running Docker Compose

Install dependencies and pinned development tools:

```bash
npm ci
npm run tools:download
npm run generate
```

Start the development database and apply the application and River migrations:

```bash
npm run db:start
npm run db:migrate
npm run db:river:migrate
```

Run the API, Worker, and Web interface in separate terminals:

```bash
npm run start:api
npm run start:worker
npm run dev --workspace @emby-auto/web
```

The development Web server listens on `http://127.0.0.1:5173` and proxies `/api` to `http://127.0.0.1:8080`. Store local credentials only in the untracked `.env` or the generated `bootstrap.json`.

### Code Generation

After changing the OpenAPI contract or database queries, run:

```bash
npm run generate
npm run check:generated
```

The following generated paths must not be edited directly:

- `backend/internal/transport/httpapi/api.gen.go`
- `backend/db/sqlc`
- `apps/web/src/api/generated`

## Quality Gates

| Command | Coverage |
| --- | --- |
| `npm run check` | Formatting, static analysis, unit tests, builds, and generated-file drift |
| `npm run check:integration` | Database migrations, constraints, and PostgreSQL integration tests |
| `npm run test:e2e --workspace @emby-auto/web` | Browser interface tests |
| `npm run deploy:check` | Docker Compose configuration and security constraints |
| `npm run deploy:direct:check` | Direct Linux deployment templates |
| `npm run deploy:direct:build` | Direct Linux release artifact |

## Repository Layout

```text
apps/web/       React/Vite administration interface
backend/        Go API, Worker, domain code, migrations, and sqlc queries
contracts/      OpenAPI contract
deploy/         Compose, systemd, Nginx, and runtime templates
docs/           Deployment and operations documentation
scripts/        Development, test, generation, release, and acceptance scripts
tools/          Pinned Go development-tool module
```

## Security

Administrator sessions use HttpOnly, SameSite cookies, and external-service secrets are encrypted in PostgreSQL. Report security issues privately according to the [Security Policy](SECURITY.en.md).

## Contributing

Read the [Contributing Guide](CONTRIBUTING.en.md) before opening an issue or pull request.

## Third-Party Services

Emby Auto does not provide or distribute media content. Deployers are responsible for ensuring that acquisition and processing comply with applicable law and third-party service terms. This is an independent project and is not affiliated with or endorsed by Emby or qBittorrent.

This product uses the TMDB API but is not endorsed or certified by TMDB.

## License

Emby Auto is released under the [MIT License](LICENSE).
