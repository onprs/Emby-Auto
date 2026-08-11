package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicDeploymentsKeepAPIAndWorkerIndependent(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	read := func(parts ...string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(contents)
	}

	compose := read("deploy", "compose.yaml")
	backendImage := read("deploy", "docker", "backend.Dockerfile")
	webImage := read("deploy", "docker", "web.Dockerfile")
	dockerNginx := read("deploy", "docker", "nginx.conf")
	nginxLocations := read("deploy", "nginx", "locations.conf")
	composeEnvironment := read("deploy", ".env.example")
	composeGuide := read("docs", "deployment.md")
	directEnvironment := read("deploy", "direct", "runtime.env.example")
	directAPIUnit := read("deploy", "direct", "emby-auto-api.service")
	directWorkerUnit := read("deploy", "direct", "emby-auto-worker.service")
	directNginx := read("deploy", "direct", "nginx.conf")
	directGuide := read("docs", "direct-deployment.md")
	workerSource := read("backend", "cmd", "worker", "main.go")
	license := read("LICENSE")
	readme := read("README.md")
	englishReadme := read("README.en.md")

	for _, required := range []string{
		"  api:\n",
		"  worker:\n",
		"command: [\"emby-auto-api\"]",
		"command: [\"emby-auto-worker\"]",
		"EMBY_MEDIA_OWNER_UID: ${APP_UID:-10001}",
		"BOOTSTRAP_CONFIG_PATH: /data/bootstrap.json",
		"host.docker.internal:host-gateway",
	} {
		if !strings.Contains(compose, required) {
			t.Fatalf("Compose deployment is missing %q", required)
		}
	}
	for _, forbidden := range []string{"/var/run/docker.sock", "/run/systemd", "network_mode: host"} {
		if strings.Contains(compose, forbidden) {
			t.Fatalf("Compose deployment contains forbidden host integration %q", forbidden)
		}
	}
	for _, variable := range []string{"DOWNLOAD_ROOT", "WORK_ROOT", "STAGING_ROOT", "ANIME_LIBRARY_ROOT", "MOVIE_LIBRARY_ROOT"} {
		mount := "${" + variable + ":-"
		if strings.Count(compose, mount) != 2 {
			t.Fatalf("%s must be mounted with the same host and container path", variable)
		}
		if !strings.Contains(composeEnvironment, variable+"=/") {
			t.Fatalf("deployment environment must give %s an absolute path", variable)
		}
	}

	if !strings.Contains(backendImage, "USER ${APP_UID}:${APP_GID}") ||
		!strings.Contains(backendImage, "./cmd/api") ||
		!strings.Contains(backendImage, "./cmd/worker") {
		t.Fatal("backend image must build both processes and run as a non-root user")
	}
	for name, image := range map[string]string{"backend": backendImage, "web": webImage} {
		if !strings.Contains(image, "org.opencontainers.image.licenses=\"MIT\"") ||
			!strings.Contains(image, "COPY LICENSE /usr/share/licenses/emby-auto/LICENSE") {
			t.Fatalf("%s image must carry MIT metadata and the license text", name)
		}
	}
	if !strings.Contains(webImage, "deploy/docker/nginx.conf") ||
		!strings.Contains(webImage, "deploy/nginx/locations.conf") ||
		!strings.Contains(dockerNginx, "include /etc/nginx/emby-auto-locations.conf;") {
		t.Fatal("web image must combine the Docker server and shared Nginx locations")
	}
	for _, required := range []string{
		"location = /api/v1/events",
		"proxy_buffering off;",
		"add_header X-Accel-Buffering \"no\" always;",
		"proxy_set_header Range $http_range;",
		"proxy_max_temp_file_size 0;",
		"try_files $uri $uri/ /index.html;",
	} {
		if !strings.Contains(nginxLocations, required) {
			t.Fatalf("shared Nginx locations are missing %q", required)
		}
	}

	for name, unit := range map[string]string{"api": directAPIUnit, "worker": directWorkerUnit} {
		for _, required := range []string{"User=emby", "Group=emby", "NoNewPrivileges=true", "CapabilityBoundingSet="} {
			if !strings.Contains(unit, required) {
				t.Fatalf("direct %s unit is missing %q", name, required)
			}
		}
		for _, forbidden := range []string{"User=root", "docker.sock", "/run/systemd"} {
			if strings.Contains(unit, forbidden) {
				t.Fatalf("direct %s unit contains forbidden integration %q", name, forbidden)
			}
		}
	}
	if !strings.Contains(directAPIUnit, "ExecStart=/opt/emby-auto/current/bin/emby-auto-api") ||
		!strings.Contains(directWorkerUnit, "ExecStart=/opt/emby-auto/current/bin/emby-auto-worker") {
		t.Fatal("direct deployment must run separate API and Worker binaries")
	}
	if !strings.Contains(directEnvironment, "EMBY_MEDIA_OWNER_UID=") ||
		!strings.Contains(directEnvironment, "BOOTSTRAP_CONFIG_PATH=/var/lib/emby-auto/bootstrap.json") {
		t.Fatal("direct environment must configure the media owner and shared bootstrap")
	}
	if !strings.Contains(directNginx, "server 127.0.0.1:18081;") ||
		!strings.Contains(directNginx, "listen 127.0.0.1:18080;") ||
		!strings.Contains(directNginx, "include /etc/nginx/emby-auto-locations.conf;") {
		t.Fatal("direct Nginx must expose a loopback Web endpoint and use shared locations")
	}
	for _, required := range []string{"systemctl", "PostgreSQL 17", "SESSION_COOKIE_SECURE", "sha256sum -c SHA256SUMS"} {
		if !strings.Contains(directGuide, required) {
			t.Fatalf("direct deployment guide is missing %q", required)
		}
	}

	if !strings.Contains(workerSource, "ResolveConfiguredImportedLibraryAccess") {
		t.Fatal("Worker must resolve the media owner from explicit configuration or the restricted host-control fallback")
	}
	for _, required := range []string{"docker compose", "postgres", "APP_UID", "SESSION_COOKIE_SECURE"} {
		if !strings.Contains(composeGuide, required) {
			t.Fatalf("Compose deployment guide is missing %q", required)
		}
	}
	if !strings.HasPrefix(license, "MIT License\n\nCopyright (c) 2026 onprs\n") {
		t.Fatal("repository must contain the approved MIT license and copyright holder")
	}
	if !strings.Contains(readme, "[English](README.en.md)") ||
		!strings.Contains(englishReadme, "[简体中文](README.md)") {
		t.Fatal("Chinese and English README files must link to each other")
	}
}
