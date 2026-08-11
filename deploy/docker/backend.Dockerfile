FROM golang:1.26-bookworm AS builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emby-auto-api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emby-auto-worker ./cmd/worker \
    && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/emby-auto-migration-check ./cmd/migration-check

FROM debian:bookworm-slim
LABEL org.opencontainers.image.licenses="MIT"
ARG APP_UID=10001
ARG APP_GID=10001
RUN test "${APP_UID}" -gt 0 \
    && test "${APP_GID}" -gt 0 \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates curl ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && install -d -o "${APP_UID}" -g "${APP_GID}" -m 0700 /data \
    && install -d -o "${APP_UID}" -g "${APP_GID}" -m 0770 \
        /media/downloads /media/work /media/staging /media/anime /media/movies \
    && install -d -o root -g root -m 0755 /usr/share/licenses/emby-auto
COPY LICENSE /usr/share/licenses/emby-auto/LICENSE
COPY --from=builder /out/emby-auto-api /usr/local/bin/emby-auto-api
COPY --from=builder /out/emby-auto-worker /usr/local/bin/emby-auto-worker
COPY --from=builder /out/emby-auto-migration-check /usr/local/bin/emby-auto-migration-check
ENV HOME=/tmp
USER ${APP_UID}:${APP_GID}
WORKDIR /data
CMD ["emby-auto-api"]
