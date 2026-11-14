//go:build !windows

package worker

import (
	"context"
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
	if info.Mode().Perm() != importedLibraryFileMode {
		t.Fatalf("mode = %04o, want %04o", info.Mode().Perm(), importedLibraryFileMode)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		t.Fatalf("owner UID = %#v, want %d", info.Sys(), uid)
	}
	if stat.Gid != beforeStat.Gid {
		t.Fatalf("group GID = %d, want unchanged %d", stat.Gid, beforeStat.Gid)
	}
}

// 入库媒体不得全局可写：目录 0755、文件 0644，独立 Emby 账户保持只读访问。
func TestOSImportedLibraryAccessNeverWorldWritable(t *testing.T) {
	uid := os.Getuid()
	if uid == 0 {
		t.Skip("root UID is intentionally rejected as an Emby media owner")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "Series", "Season1"), 0o700); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "Series", "Season1", "episode.mkv")
	if err := os.WriteFile(video, []byte("media"), 0o600); err != nil {
		t.Fatal(err)
	}
	access, err := NewImportedLibraryAccess(uid)
	if err != nil {
		t.Fatal(err)
	}
	if err := access.ApplyTree(context.Background(), root); err != nil {
		t.Fatalf("ApplyTree() error = %v", err)
	}
	if err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		perm := info.Mode().Perm()
		want := importedLibraryFileMode
		if info.IsDir() {
			want = importedLibraryDirMode
		}
		if perm != want {
			t.Errorf("mode of %s = %04o, want %04o", path, perm, want)
		}
		if perm&0o022 != 0 {
			t.Errorf("%s is group/world writable: %04o", path, perm)
		}
		if !info.IsDir() && perm&0o111 != 0 {
			t.Errorf("%s grants execute to non-owner: %04o", path, perm)
		}
		return nil
	}); err != nil {
		t.Fatalf("walk imported library tree: %v", err)
	}
}
