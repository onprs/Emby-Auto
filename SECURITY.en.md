# Security Policy

[简体中文](SECURITY.md) | **English**

## Supported Versions

Emby Auto is currently in the `0.x` development stage. Security fixes target only the latest revision of the default branch.

| Version range | Security support |
| --- | --- |
| Latest `main` revision | Supported |
| Earlier revisions, third-party branches, and unofficial images | Not supported |

Deployers should upgrade promptly after completing backups and compatibility checks. Vulnerabilities in third-party components should be reported to the corresponding upstream project unless the issue is introduced by the Emby Auto integration.

## Report a Vulnerability Privately

Use **Report a vulnerability** on the repository's GitHub **Security** page whenever available. If private vulnerability reporting is unavailable, request a private security channel through the contact method listed on the maintainer's GitHub profile without including vulnerability details in the initial message.

Never publish the following information in an issue, discussion, pull request, log, or screenshot:

- Administrator passwords, session cookies, API tokens, database connections, or configuration-encryption material.
- Private RSS URLs, proxy configuration, real media details, or internal file paths.
- Directly exploitable requests, complete attack chains, or production infrastructure details.

## Report Contents

A useful report includes as much of the following information as possible:

- The affected version or commit.
- The affected component and deployment method.
- Minimal reproduction steps using redacted placeholders.
- Actual behavior, expected behavior, and potential impact.
- Any verified temporary mitigation.
- Confirmation that testing occurred in an owned or explicitly authorized environment.

Do not test third-party Emby Auto instances, external-service accounts, or infrastructure without explicit authorization.

## Response and Disclosure

Maintainers will acknowledge the report, assess impact, prepare a fix, and coordinate disclosure. Keep technical details private until a fix is released or an agreed disclosure date is reached.

A security fix may update application code, database migrations, container images, or deployment templates. The advisory will identify the affected scope, upgrade requirements, and available mitigations.

## Deployment Security Responsibilities

Emby Auto is self-hosted software. Deployers are responsible for:

- TLS termination, network access controls, firewall policy, and host security updates.
- Encrypted backups of PostgreSQL, installation configuration, and media data.
- Administrator accounts and credentials for qBittorrent, TMDb, Emby, Agent providers, and other external services.
- Non-root service accounts and appropriately scoped media-directory permissions.
- Preventing access to the Docker socket, host root, systemd bus, and unnecessary Linux capabilities.

Production deployments should follow the security requirements in the [Docker Compose guide](docs/deployment.en.md) or [Direct Linux guide](docs/direct-deployment.en.md).
