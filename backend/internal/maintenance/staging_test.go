package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestStagingOwnershipApplyRetryAndRestoreKeepFilesGroupAndMode(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("目录所有权变更在隔离 root 容器中验证")
	}
	root := t.TempDir()
	task := uuid.NewString()
	foreign := uuid.NewString()
	directory := filepath.Join(root, task, "series", "season")
	if err := os.MkdirAll(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	foreignDirectory := filepath.Join(root, foreign)
	if err := os.Mkdir(foreignDirectory, 0o750); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "video.mp4")
	if err := os.WriteFile(file, []byte("preserve media bytes"), 0o640); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	backup, planned, err := PlanStagingOwnership(context.Background(), root, []string{task}, 10001)
	if err != nil || planned.Tasks != 1 || planned.Directories != 3 {
		t.Fatalf("目录计划 = %#v，错误 %v", planned, err)
	}
	changed, err := ApplyStagingOwnership(context.Background(), backup, map[string]bool{task: true}, false)
	if err != nil || changed.Changed != 3 {
		t.Fatalf("应用目录所有权 = %#v，错误 %v", changed, err)
	}
	again, err := ApplyStagingOwnership(context.Background(), backup, map[string]bool{task: true}, false)
	if err != nil || again.Changed != 0 {
		t.Fatalf("重放不幂等：%#v，%v", again, err)
	}
	for _, item := range backup.Directories {
		info, err := os.Stat(filepath.Join(root, item.Relative))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := ownership(info, item.Relative)
		if err != nil || actual.UID != 10001 || actual.GID != item.GID || actual.Mode != item.Mode {
			t.Fatalf("目录组或模式漂移：%#v，%v", actual, err)
		}
	}
	outsideInfo, err := os.Stat(foreignDirectory)
	if err != nil {
		t.Fatal(err)
	}
	outside, err := ownership(outsideInfo, foreign)
	if err != nil || outside.UID != 0 {
		t.Fatal("修改了不属于目标任务的目录")
	}
	after, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(file)
	if err != nil || string(bytes) != "preserve media bytes" || before.Mode() != after.Mode() || !os.SameFile(before, after) {
		t.Fatal("目录迁移改变了媒体文件")
	}
	restored, err := ApplyStagingOwnership(context.Background(), backup, map[string]bool{task: true}, true)
	if err != nil || restored.Changed != 3 {
		t.Fatalf("恢复 = %#v，%v", restored, err)
	}
	for _, item := range backup.Directories {
		info, _ := os.Stat(filepath.Join(root, item.Relative))
		actual, err := ownership(info, item.Relative)
		if err != nil || actual != item {
			t.Fatalf("恢复后元数据与原值不一致：%v", err)
		}
	}
}

func TestStagingOwnershipRejectsLinksAndReplacedDirectories(t *testing.T) {
	root := t.TempDir()
	task := uuid.NewString()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, task)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PlanStagingOwnership(context.Background(), root, []string{task}, 10001); err == nil {
		t.Fatal("跟随了任务目录符号链接")
	}
	if os.Geteuid() != 0 {
		return
	}
	if err := os.Remove(filepath.Join(root, task)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, task), 0o750); err != nil {
		t.Fatal(err)
	}
	backup, _, err := PlanStagingOwnership(context.Background(), root, []string{task}, 10001)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, task), filepath.Join(root, "preserved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, task), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyStagingOwnership(context.Background(), backup, map[string]bool{task: true}, false); err == nil {
		t.Fatal("修改了已被替换的任务目录")
	}
	backup.Directories[0].Relative = "../outside"
	if _, err := ApplyStagingOwnership(context.Background(), backup, map[string]bool{task: true}, false); err == nil {
		t.Fatal("接受了越界恢复路径")
	}
}
