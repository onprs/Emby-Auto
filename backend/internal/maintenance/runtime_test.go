package maintenance

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/onprs/emby-auto/backend/internal/domain"
)

func TestPlanAddressesChangesOnlyTheTwoDeclaredServices(t *testing.T) {
	settings := domain.RuntimeSettings{
		QBittorrent:  domain.QBittorrentSettings{URL: "http://host.docker.internal:8080/qb", Username: "media-service"},
		Emby:         domain.EmbySettings{URL: "http://host.docker.internal:8096/emby"},
		NetworkProxy: domain.NetworkProxySettings{URL: "http://host.docker.internal:7890"},
		Agent:        domain.AgentSettings{BaseURL: "http://host.docker.internal:11434"},
	}
	updated, changes, err := PlanAddresses(settings, "host.docker.internal", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 || updated.QBittorrent.URL != "http://127.0.0.1:8080/qb" || updated.Emby.URL != "http://127.0.0.1:8096/emby" {
		t.Fatalf("迁移结果 = %#v，变更数量 %d", updated, len(changes))
	}
	if updated.NetworkProxy != settings.NetworkProxy || updated.Agent != settings.Agent || updated.QBittorrent.Username != settings.QBittorrent.Username {
		t.Fatal("地址迁移改变了范围外配置")
	}
	restored, reversed, err := RestoreAddresses(updated, AddressBackup{Format: 1, Version: 3, Changes: changes})
	if err != nil || restored != settings || len(reversed) != 2 {
		t.Fatalf("恢复未保留原配置：%v", err)
	}
	_, repeated, err := RestoreAddresses(restored, AddressBackup{Format: 1, Changes: changes})
	if err != nil || len(repeated) != 0 {
		t.Fatal("重复恢复没有保持幂等")
	}
	updated.Emby.URL = "http://127.0.0.1:8097/emby"
	if _, _, err := RestoreAddresses(updated, AddressBackup{Format: 1, Changes: changes}); err == nil {
		t.Fatal("恢复覆盖了并发修改的地址")
	}
}

func TestReplaceURLHostPreservesPortsPathsAndIPv6Syntax(t *testing.T) {
	for _, test := range []struct{ value, to, expected string }{
		{"http://host.docker.internal:8080/qb", "::1", "http://[::1]:8080/qb"},
		{"https://host.docker.internal/emby?mode=public", "127.0.0.1", "https://127.0.0.1/emby?mode=public"},
		{"http://other.example.test:8080", "127.0.0.1", "http://other.example.test:8080"},
	} {
		actual, _, err := ReplaceURLHost(test.value, "host.docker.internal", test.to)
		if err != nil || actual != test.expected {
			t.Fatalf("地址转换 = %q，期望 %q，错误 %v", actual, test.expected, err)
		}
	}
}

func TestPrivateBackupsRejectReplacementLinksAndPublicPermissions(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "addresses.json")
	value := AddressBackup{Format: 1, Version: 2, Changes: []AddressChange{{Field: "emby.url", Before: "before", After: "after"}}}
	if err := WritePrivateJSON(path, value); err != nil {
		t.Fatal(err)
	}
	var actual AddressBackup
	if err := ReadPrivateJSON(path, &actual); err != nil || !reflect.DeepEqual(actual, value) {
		t.Fatalf("读取私有快照：%v", err)
	}
	if err := WritePrivateJSON(path, value); err == nil {
		t.Fatal("覆盖了已有恢复点")
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReadPrivateJSON(path, &actual); err == nil {
		t.Fatal("接受了公开可读的维护快照")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{} {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReadPrivateJSON(path, &actual); err == nil {
		t.Fatal("接受了带有多余 JSON 内容的快照")
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if err := ReadPrivateJSON(link, &actual); err == nil {
		t.Fatal("读取了符号链接快照")
	}
}
