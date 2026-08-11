FROM node:24-alpine AS builder
WORKDIR /src
COPY package.json package-lock.json ./
COPY apps/web/package.json apps/web/package.json
RUN npm ci --workspace @emby-auto/web --include-workspace-root=false
COPY scripts/web/ scripts/web/
COPY apps/web/ apps/web/
RUN npm run build --workspace @emby-auto/web

FROM nginx:1.29-alpine
LABEL org.opencontainers.image.licenses="MIT"
COPY deploy/docker/nginx.conf /etc/nginx/conf.d/default.conf
COPY deploy/nginx/locations.conf /etc/nginx/emby-auto-locations.conf
COPY LICENSE /usr/share/licenses/emby-auto/LICENSE
COPY --from=builder /src/apps/web/dist/ /usr/share/nginx/html/
EXPOSE 80
