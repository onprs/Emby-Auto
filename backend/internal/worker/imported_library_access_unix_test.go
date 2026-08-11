//go:build !windows

package worker

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOSImportedLibraryAccessSetsOwnerAndMode(t *testing.T) {
	uid := os.Getuid()
	if uid == 0 {
		t.Skip("root UID is intentionally rejected as an Emby media owner")
	}
	path := filepath.Join(t.TempDir(), "episode.mkv")
	if err := os.WriteFile(path, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	beforeStat, ok := beforeInfo.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("stat data = %#v", beforeInfo.Sys())
	}
	access, err := NewImportedLibraryAccess(uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := access.Apply(path); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != importedLibraryPathMode {
		t.Fatalf("mode = %04o, want %04o", info.Mode().Perm(), importedLibraryPathMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		t.Fatalf("owner UID = %#v, want %d", info.Sys(), uid)
	}
	if stat.Gid != beforeStat.Gid {
		t.Fatalf("group GID = %d, want unchanged %d", stat.Gid, beforeStat.Gid)
	}
}
