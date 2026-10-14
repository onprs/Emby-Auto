<p align="center">
  <img src="emby-auto-svg.svg" alt="Emby Auto - Self-hosted media automation" width="800">
</p>

# Emby Auto

[简体中文](README.md) | **English**

Emby Auto is a self-hosted media automation platform for Emby. It connects resource search, RSS subscriptions, qBittorrent downloads, TMDb episode mapping, FFmpeg processing, manual review, and Emby library imports through a web interface.

## Features

- Search anime episodes and movies, poll RSS/Atom feeds, and filter releases automatically.
- Manage qBittorrent downloads, file selection, progress, cancellation, retries, and cache cleanup.
- Map source episodes to TMDb episodes using episode anchors.
- Generate transcoded videos and separate ASS subtitles, with review before import.
- Synchronize Emby library directories and track tasks, failures, and live events.
- Configure administrators, external services, storage, and transcoding through the setup wizard.
- Optionally use an Agent to assist with ambiguous cases.

## One-command installation

No Docker, repository checkout, Go, or Node.js is required. The installer downloads a prebuilt release, installs PostgreSQL 17, Nginx, and FFmpeg, and enables services at boot.

Supported systems: Debian 12/13 and Ubuntu 22.04/24.04/26.04, 64-bit `amd64` or `arm64`, running systemd. You need root or sudo access, Bash, curl, and network access to GitHub and system package repositories.

Run on the target device:

~~~bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s --
~~~

Open `http://127.0.0.1:18080` on that device. For a remote device, establish an SSH tunnel from your computer, then open the same URL:

~~~bash
ssh -L 18080:127.0.0.1:18080 user@device-address
~~~

The database is configured automatically. Use the web wizard to create an administrator, enter your qBittorrent Web UI credentials, Emby URL and API key, TMDb API Read Access Token, and confirm storage paths.

For an existing media library, install using the account that owns its files:

~~~bash
curl -fsSL https://github.com/onprs/Emby-Auto/releases/latest/download/install.sh | sudo bash -s -- --user media-owner
~~~

Running the installer again preserves the installed version, configuration, and data. Use `--port PORT` if the web port is occupied. See [Linux direct deployment](docs/direct-deployment.en.md) for options, HTTPS, backups, and upgrades.

## Requirements for use

- Prepare qBittorrent and Emby Server as external services.
- The device must have access to download and library directories. qBittorrent and the application must use identical absolute download paths.
- The application account UID must match the owner of Emby media files.
- Access locally or through an SSH tunnel by default. Configure HTTPS and secure session cookies for network access.
- Back up the database, installation configuration, and media directories regularly.

## Other deployment options

- [Docker Compose](docs/deployment.en.md): deploy using containers.
- [Manual Linux installation](docs/direct-deployment.en.md#manual-installation): prepare dependencies and install a release yourself.
- [GitHub Releases](https://github.com/onprs/Emby-Auto/releases): download `amd64` or `arm64` bundles and checksums.

## Security and contributions

Sessions use HttpOnly and SameSite cookies. External service credentials are encrypted at rest. Report security issues privately using the [security policy](SECURITY.en.md). Read the [contribution guide](CONTRIBUTING.en.md) and [development documentation](docs/development.md) before contributing code.

## Third-party services

Emby Auto does not provide or distribute media content. Deployers are responsible for compliance with applicable laws and third-party terms. This project is independent and is not affiliated with or endorsed by Emby or qBittorrent.

This product uses the TMDB API but is not endorsed or certified by TMDB.

## License

[MIT License](LICENSE)
