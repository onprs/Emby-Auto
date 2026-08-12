# Contributing Guide

[简体中文](CONTRIBUTING.md) | **English**

This guide defines the contribution workflow and quality requirements for Emby Auto. Every change must preserve the established API contracts, job boundaries, and media-safety rules.

## Before Starting

- Search existing issues and pull requests to avoid duplicate work.
- Describe defects or behavioral differences with a minimal, reproducible case.
- Open an issue before implementing changes with significant cross-module, API-compatibility, or data-model impact.
- Do not disclose security vulnerabilities in public issues; follow the [Security Policy](SECURITY.en.md).

## Development Workflow

1. Create a dedicated branch from the latest target branch.
2. Keep the change focused on one defined problem and exclude unrelated refactoring or formatting.
3. Update contracts or migrations before implementing API, Worker, and Web behavior.
4. Add tests that match the risk and behavioral scope of the change.
5. Run code generation and all applicable quality gates.
6. Open a pull request that documents behavior, compatibility, risk, and verification results.

## Architecture Contracts

The following constraints apply to all contributions:

- The API and Worker remain separate processes. The API does not perform downloads, transcoding, subtitle processing, file moves, imports, or cleanup.
- [`contracts/openapi.yaml`](contracts/openapi.yaml) is the single HTTP contract. Do not maintain duplicate handwritten client or server types.
- Only the backend state machine changes task state. State transitions, River job creation, and event recording belong in one database transaction.
- River jobs require stable idempotency keys, uniqueness, retries, timeouts, cancellation, heartbeats, and error recording.
- Only video transcoding uses dedicated concurrency slots. Other background work must not wait for transcode capacity.
- Deterministic rules own standard workflows, security boundaries, and output validation. Agent capabilities are available only as constrained fallbacks for atypical cases.
- Media output is written to a temporary file on the destination filesystem, validated, and atomically renamed.
- Do not introduce Redis, unnecessary services, Docker socket access, or host-level privileged dependencies.

## API, Database, and Generated Sources

### OpenAPI

Change `contracts/openapi.yaml` before implementing modified HTTP behavior, then run:

```bash
npm run generate
npm run check:generated
```

Do not edit these generated paths directly:

- `backend/internal/transport/httpapi/api.gen.go`
- `backend/db/sqlc`
- `apps/web/src/api/generated`

### Database

Database changes require a new sequential migration. Published migrations must not be rewritten or renumbered. Cover the upgrade path, required rollback semantics, and upgrades from supported historical versions with tests.

When sqlc queries change, edit their source under `backend/db/query` and rerun generation.

## Code Standards

- Format Go with `gofmt` and pass the repository-pinned `golangci-lint` version.
- Keep TypeScript strictly typed and use the generated OpenAPI SDK instead of duplicate handwritten types.
- Use comments to explain non-obvious constraints, failure semantics, and design reasons rather than restating code.
- Keep user-facing messages specific and actionable without exposing internal paths, stack traces, or raw upstream responses.
- Explain the necessity, maintenance status, license, and release-size impact of each new dependency.

## Testing and Verification

Expected values in tests must come from independent, concrete business cases rather than the implementation under test.

| Change scope | Minimum verification |
| --- | --- |
| Any code change | `npm run check` |
| Database, state machine, or durable jobs | `npm run check:integration` |
| Concurrency-sensitive Go code | `npm run test:race` |
| Web interactions | `npm run test:e2e --workspace @emby-auto/web` |
| Cross-process workflows | `npm run test:e2e:full` |
| Docker deployment templates | `npm run deploy:check`, `npm run deploy:build`, `npm run deploy:check-release` |
| Direct Linux templates | `npm run deploy:direct:check`, `npm run deploy:direct:build`, `npm run deploy:direct:check-release` |

Run full E2E tests only in an isolated test environment. Automated tests must not connect to production qBittorrent, Emby, or TMDb accounts or production media directories. Inject external-service acceptance credentials through the process environment only.

## Branch and Commit Conventions

- Branch names: `feat/<description>`, `fix/<description>`, `docs/<description>`, `chore/<description>`, `refactor/<description>`, `test/<description>`.
- Commit messages follow Conventional Commits (`feat:`, `fix:`, `docs:`, `chore:`, `test:`, `refactor:`) and stay specific and consistent with the change.
- Maintainers create short-lived branches in the repository; external contributors create branches in their own fork.

## Parallel Development

- Keep branches short-lived: one PR solves one focused problem and gets merged promptly; avoid long-lived placeholders or unrelated churn.
- Search Issues and existing PRs before starting, and announce what you are working on to avoid overlapping changes; parallel changes touching the same module or contract should be aligned via an Issue first.
- When PRs depend on each other, mention the dependency (PR/Issue numbers) in the description and merge the dependency first; never smuggle unmerged dependencies into another PR.
- Before merging, sync the branch to the latest `main` (via Update branch or a local rebase) and re-trigger CI; merge only when the branch is up to date and all checks pass.
- Resolve conflicts against the latest `main` and re-run the relevant checks; when a conflict involves someone else's change, confirm the intended semantics with the author.
- Releases do not freeze development: a `v*` tag can be created from `main` at any time, in parallel with regular merges.
- Remote branches are deleted automatically after merge; clean up local branches accordingly.

## Pull Request Requirements

A pull request must document:

- The purpose of the change and linked issue.
- User-visible behavior and API changes.
- Database migration, compatibility, and rollback impact.
- Primary risks and corresponding tests.
- The complete verification commands that were run.
- Any upgrade steps required from deployers.

Before submission, confirm that the worktree contains no unrelated generated drift, lockfile changes, or formatting noise.

## Merge Rules

- Every PR must pass all CI status checks: `verify`, `container-build`, `direct-build (amd64)`, and `direct-build (arm64)`.
- At least one maintainer review and approval is required, all conversations must be resolved, and the branch must be based on the latest `main`.
- Only squash merges are allowed to keep `main` linear; direct pushes to `main` are forbidden.
- Releases use `v0.x.y` tags; a `v*` tag triggers CI to build the public release bundles and attach them to the GitHub Release. Release cadence and content are decided by maintainers.

## Repository Data Boundary

Do not commit:

- Local `.env` runtime files (excluding sanitized `.env.example` templates), real passwords, tokens, cookies, database connections, private subscription URLs, or proxy configuration.
- Real media, subtitles, download caches, logs, database dumps, backups, or runtime state.
- Production host details, user data, raw external-service responses, or unredacted diagnostics.
- Unrelated local tool output, editor state, or temporary files.

## License

By contributing, you confirm that you have the right to provide the contribution and agree that it will be distributed under the project's [MIT License](LICENSE).
